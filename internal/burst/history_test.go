package burst

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"pooltune/internal/admin"
	"pooltune/internal/durable"
	"testing"
	"time"
)

func journalController(t *testing.T, kind string, save func(State) error) *Controller {
	t.Helper()
	if save == nil {
		save = func(State) error { return nil }
	}
	c, err := NewController(Config{Target: "orders", Base: 1, Ceiling: 3, Queue: 8, QueueWait: time.Second, PressureSource: kind + ":private-resource", PressureMaxAge: 75 * time.Second}, save)
	if err != nil {
		t.Fatal(err)
	}
	freshHealth(c)
	if err = c.Resume(time.Now()); err != nil {
		t.Fatal(err)
	}
	return c
}
func freshHealth(c *Controller) {
	now := time.Now()
	c.Update(Health{At: now, PressureAt: now, Source: c.cfg.PressureSource, CPU: .1, Memory: .1, Warm: c.cfg.Ceiling, BudgetVerified: true})
}
func command(c *Controller, key, revision string) admin.Command {
	return admin.Command{ExpectedRevision: c.State().Revision, IdempotencyKey: key, Revision: revision}
}
func expand(t *testing.T, c *Controller) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.apply(time.Now(), 3, "warm_reserve"); err != nil {
		t.Fatal(err)
	}
}

func TestManagedRestoreHoldsLimitAndPreservesActiveWork(t *testing.T) {
	for _, kind := range []string{"azure", "google", "supabase"} {
		t.Run(kind, func(t *testing.T) {
			c := journalController(t, kind, nil)
			baseline := c.State().Revision
			expand(t, c)
			first, e := c.Scheduler.Acquire(context.Background(), c.Classify("u", "db", "one"))
			if e != nil {
				t.Fatal(e)
			}
			second, e := c.Scheduler.Acquire(context.Background(), c.Classify("u", "db", "two"))
			if e != nil {
				t.Fatal(e)
			}
			defer c.Scheduler.Release(first, 0)
			defer c.Scheduler.Release(second, 0)
			backend := DashboardBackend{Controller: c}
			req := command(c, "restore-one", baseline)
			v, e := backend.Execute(context.Background(), "revert", req)
			if e != nil {
				t.Fatal(e)
			}
			result := v.(CommandResult)
			if result.Status != "confirmed" || result.Revision == baseline {
				t.Fatalf("restore must create a new confirmed revision: %+v", result)
			}
			stats := c.Scheduler.Stats()
			if stats.Effective != 1 || stats.Active != 2 {
				t.Fatalf("restore disturbed active work: %+v", stats)
			}
			c.Tick(time.Now())
			if c.State().ManualLimit != 1 || c.State().Effective != 1 {
				t.Fatal("automation replaced owner hold")
			}
			history, e := backend.Read(context.Background(), "revisions")
			if e != nil {
				t.Fatal(e)
			}
			items := history.(HistoryView).Items
			if len(items) != 3 || items[2].RestoredFrom != baseline || items[2].Actor != "owner" {
				t.Fatalf("missing append-only history: %+v", items)
			}
			before := c.State().Revision
			again, e := backend.Execute(context.Background(), "revert", req)
			if e != nil || again.(CommandResult) != result || c.State().Revision != before {
				t.Fatal("retry repeated restore", e)
			}
			changed := req
			changed.Revision = before
			if _, e = backend.Execute(context.Background(), "revert", changed); !errors.Is(e, admin.ErrConflict) {
				t.Fatal("key reuse accepted")
			}
			stale := req
			stale.IdempotencyKey = "stale-tab"
			stale.Revision = ""
			if _, e = backend.Execute(context.Background(), "freeze", stale); !errors.Is(e, admin.ErrConflict) {
				t.Fatal("stale tab accepted")
			}
			if _, e = backend.Execute(context.Background(), "resume", command(c, "resume-auto", "")); e != nil {
				t.Fatal(e)
			}
			if c.State().ManualLimit != 0 {
				t.Fatal("resume did not clear hold")
			}
		})
	}
}

func TestRestoreCannotBypassManagedSafety(t *testing.T) {
	for _, scenario := range []string{"stale", "source", "budget", "warm", "moderate", "high", "uncertain", "stopped", "unknown-revision"} {
		t.Run(scenario, func(t *testing.T) {
			c := journalController(t, "supabase", nil)
			baseline := c.State().Revision
			expand(t, c)
			h := c.health
			switch scenario {
			case "stale":
				h.At = time.Now().Add(-3 * time.Second)
			case "source":
				h.Source = "wrong"
			case "budget":
				h.BudgetVerified = false
			case "warm":
				h.Warm = 2
			case "moderate":
				h.CPU = .7
			case "high":
				h.Memory = .95
			case "uncertain":
				p, e := c.Scheduler.Acquire(context.Background(), c.Classify("u", "d", "s"))
				if e != nil {
					t.Fatal(e)
				}
				c.Scheduler.Release(p, 1)
			case "stopped":
				c.Freeze()
			case "unknown-revision":
				baseline = "invented"
			}
			c.Update(h)
			before := c.State().Revision
			_, e := (DashboardBackend{c}).Execute(context.Background(), "revert", command(c, "unsafe-restore", baseline))
			if e == nil || c.State().Revision != before {
				t.Fatal("unsafe restore accepted")
			}
		})
	}
}

func TestManualHoldStillBacksOffAndStops(t *testing.T) {
	c := journalController(t, "google", nil)
	expand(t, c)
	high := c.State().Revision
	c.mu.Lock()
	_ = c.apply(time.Now(), 1, "base")
	c.mu.Unlock()
	if _, e := (DashboardBackend{c}).Execute(context.Background(), "revert", command(c, "hold-three", high)); e != nil {
		t.Fatal(e)
	}
	h := c.health
	h.CPU = .75
	c.Update(h)
	c.Tick(time.Now())
	if c.State().Effective != 1 || c.State().ManualLimit != 3 {
		t.Fatal("manual mode bypassed pressure backoff")
	}
	h.CPU = .1
	h.At = time.Now().Add(-3 * time.Second)
	c.Update(h)
	c.Tick(time.Now())
	if c.State().Effective != 0 {
		t.Fatal("manual mode bypassed expiry")
	}
	freshHealth(c)
	c.Tick(time.Now())
	if c.State().Effective != 3 {
		t.Fatal("held limit not restored after healthy readings")
	}
	h = c.health
	h.CPU = .9
	c.Update(h)
	c.Tick(time.Now())
	freshHealth(c)
	c.Tick(time.Now())
	if c.State().Effective != 0 || !c.State().Frozen {
		t.Fatal("hard safety stop unlatched automatically")
	}
}

func TestHistoryIntentAndConfirmationFailuresStopAdmission(t *testing.T) {
	for _, phase := range []int{1, 2} {
		t.Run(string(rune('0'+phase)), func(t *testing.T) {
			calls, fail := 0, 0
			var saved []byte
			c := journalController(t, "azure", func(s State) error {
				calls++
				if calls == fail {
					return errors.New("disk failure")
				}
				saved, _ = json.Marshal(s)
				return nil
			})
			base := c.State().Revision
			expand(t, c)
			fail = calls + phase
			_, e := (DashboardBackend{c}).Execute(context.Background(), "revert", command(c, "disk-failure", base))
			if e == nil || c.Scheduler.Stats().Effective != 0 || !c.State().Frozen || !c.auditFailed {
				t.Fatal("storage uncertainty left admission available")
			}
			freshHealth(c)
			if c.Resume(time.Now()) == nil {
				t.Fatal("resume cleared audit failure")
			}
			c.Tick(time.Now())
			if c.Ready() {
				t.Fatal("tick cleared audit failure")
			}
			if len(saved) == 0 {
				t.Fatal("missing retained history")
			}
		})
	}
}

func TestHistoryReloadIsScopedAndNeverReplaysCapacity(t *testing.T) {
	var saved []byte
	c := journalController(t, "azure", func(s State) error { saved, _ = json.Marshal(s); return nil })
	old := c.State().Revision
	expand(t, c)
	c.Freeze()
	restored, e := NewController(c.cfg, func(State) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	if e = restored.loadHistory(saved); e != nil {
		t.Fatal(e)
	}
	if restored.State().Effective != 0 || !restored.State().Frozen || restored.State().Revision == c.State().Revision || restored.Ready() {
		t.Fatal("restart reused runtime state")
	}
	if len(restored.State().History) != len(c.State().History) {
		t.Fatal("history lost on restart")
	}
	freshHealth(restored)
	if e = restored.Resume(time.Now()); e != nil {
		t.Fatal(e)
	}
	if _, e = (DashboardBackend{restored}).Execute(context.Background(), "revert", command(restored, "restore-prior-process", old)); e != nil {
		t.Fatal(e)
	}
	different := c.cfg
	different.PressureSource = "google:other"
	other, _ := NewController(different, func(State) error { return nil })
	if other.loadHistory(saved) == nil {
		t.Fatal("foreign profile history accepted")
	}
	var journal State
	_ = json.Unmarshal(saved, &journal)
	journal.History[len(journal.History)-1].Status = "intent"
	unresolved, _ := json.Marshal(journal)
	fresh, _ := NewController(c.cfg, func(State) error { return nil })
	if fresh.loadHistory(unresolved) == nil {
		t.Fatal("unresolved journal replayable")
	}
	if fresh.loadHistory([]byte(`{"Version":1}`)) == nil {
		t.Fatal("malformed journal accepted")
	}
}

func TestDashboardWithoutDurabilityRemainsReadOnly(t *testing.T) {
	c := journalController(t, "supabase", nil)
	c.persist = nil
	_, e := (DashboardBackend{c}).Execute(context.Background(), "freeze", command(c, "volatile", ""))
	if e == nil {
		t.Fatal("volatile dashboard accepted control")
	}
}

func TestFullJournalStopsWithoutRewritingConfirmedHistory(t *testing.T) {
	c := journalController(t, "azure", nil)
	last := c.state.History[0]
	c.state.History = make([]Revision, maxHistory)
	for i := range c.state.History {
		c.state.History[i] = last
	}
	_, err := (DashboardBackend{c}).Execute(context.Background(), "freeze", command(c, "journal-full", ""))
	if err == nil || c.Scheduler.Stats().Effective != 0 || !c.auditFailed {
		t.Fatal("full journal did not stop")
	}
	if c.state.History[maxHistory-1].Status != "confirmed" {
		t.Fatal("full journal rewrote a confirmed outcome")
	}
}

func TestShutdownCannotBeUndoneByDashboard(t *testing.T) {
	c := journalController(t, "google", nil)
	c.Shutdown()
	freshHealth(c)
	if _, e := (DashboardBackend{c}).Execute(context.Background(), "resume", command(c, "resume-during-shutdown", "")); e == nil {
		t.Fatal("dashboard restarted a draining runtime")
	}
	if c.Resume(time.Now()) == nil || c.Ready() {
		t.Fatal("shutdown was reversible")
	}
}

func TestAuditFailureCannotMarkRuntimeClean(t *testing.T) {
	c := journalController(t, "azure", nil)
	c.persist = func(State) error { return errors.New("disk unavailable") }
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	files, e := durable.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	if e = files.Save("lifecycle", []byte("running")); e != nil {
		t.Fatal(e)
	}
	r := Runtime{Controller: c, files: files}
	if e = r.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := durable.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	marker, e := reopened.Load("lifecycle")
	if e != nil {
		t.Fatal(e)
	}
	if string(marker) == "clean" {
		t.Fatal("failed audit allowed automatic clean restart")
	}
}

func TestShutdownRejectsLateFreezeWithoutWritingHistory(t *testing.T) {
	writes := 0
	c := journalController(t, "azure", func(State) error { writes++; return nil })
	c.Shutdown()
	before := writes
	if _, e := (DashboardBackend{c}).Execute(context.Background(), "freeze", command(c, "late-freeze", "")); e == nil {
		t.Fatal("late freeze was accepted during shutdown")
	}
	if writes != before {
		t.Fatal("shutdown allowed new audit writes")
	}
}
func TestMalformedLegacyHistoryIsRejected(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"At":"2026-01-01T00:00:00Z"}`, `{"At":"2026-01-01T00:00:00Z","Effective":1,"Reason":"ready","Frozen":false,"Changes":0}`} {
		c := journalController(t, "azure", nil)
		if c.loadHistory([]byte(raw)) == nil {
			t.Fatalf("malformed legacy state accepted: %s", raw)
		}
	}
	c := journalController(t, "azure", nil)
	if e := c.loadHistory([]byte(`{"At":"2026-01-01T00:00:00Z","Effective":1,"Reason":"ready","Frozen":false,"Changes":1}`)); e != nil {
		t.Fatal("valid prior snapshot rejected", e)
	}
}

func TestGrowthRechecksFreshnessAfterIntentStorage(t *testing.T) {
	var c *Controller
	expire := false
	c = journalController(t, "azure", func(State) error {
		if expire {
			c.health.At = time.Now().Add(-3 * time.Second)
		}
		return nil
	})
	expire = true
	c.mu.Lock()
	e := c.apply(time.Now(), 3, "warm_reserve")
	c.mu.Unlock()
	if e == nil || c.Scheduler.Stats().Effective != 0 {
		t.Fatal("growth published after evidence expired during storage")
	}
}

func TestConcurrentReviewedCommandsApplyOnlyOnce(t *testing.T) {
	for _, kind := range []string{"azure", "google", "supabase"} {
		t.Run(kind, func(t *testing.T) {
			c := journalController(t, kind, nil)
			baseline := c.State().Revision
			expand(t, c)
			expected := c.State().Revision
			before := len(c.State().History)
			start := make(chan struct{})
			results := make(chan error, 2)
			for _, key := range []string{"review-a", "review-b"} {
				go func(key string) {
					<-start
					_, err := c.execute(context.Background(), "revert", admin.Command{ExpectedRevision: expected, IdempotencyKey: key, Revision: baseline})
					results <- err
				}(key)
			}
			close(start)
			first, second := <-results, <-results
			if !((first == nil && errors.Is(second, admin.ErrConflict)) || (second == nil && errors.Is(first, admin.ErrConflict))) {
				t.Fatalf("expected one applied command and one stale conflict: %v / %v", first, second)
			}
			if len(c.State().History) != before+1 || c.State().ManualLimit != 1 {
				t.Fatal("concurrent reviews did not produce exactly one held revision")
			}
		})
	}
}
