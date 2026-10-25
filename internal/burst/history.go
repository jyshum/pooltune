package burst

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"pooltune/internal/admin"
	"regexp"
	"time"
)

const maxHistory = 4096
const visibleHistory = 200

var commandID = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

type Revision struct {
	ID           string    `json:"id"`
	At           time.Time `json:"at"`
	Actor        string    `json:"actor"`
	Status       string    `json:"status"`
	Reason       string    `json:"reason"`
	Before       int       `json:"before"`
	After        int       `json:"after"`
	RestoredFrom string    `json:"restored_from,omitempty"`
	// Request identity is retained privately for retry handling, never in Read DTOs.
	Key      string `json:"key,omitempty"`
	Action   string `json:"action,omitempty"`
	Expected string `json:"expected,omitempty"`
}
type CommandResult struct {
	Revision string `json:"revision"`
	Status   string `json:"status"`
}
type HistoryView struct {
	Items []Revision `json:"items"`
	Total int        `json:"total"`
	Limit int        `json:"limit"`
}

func revisionID() string {
	var raw [16]byte
	// crypto/rand.Read terminates the process on an entropy failure.
	_, _ = rand.Read(raw[:])
	return "burst-" + hex.EncodeToString(raw[:])
}
func configScope(c Config) string {
	raw, _ := json.Marshal(c)
	sum := sha256.Sum256(append(raw, []byte(c.historyScope)...))
	return hex.EncodeToString(sum[:])
}
func cloneState(s State) State { s.History = append([]Revision(nil), s.History...); return s }
func (b *Controller) save(s State) error {
	if b.persist != nil {
		return b.persist(cloneState(s))
	}
	return nil
}

// loadHistory imports records only. Admission, manual holds and safety latches
// are never replayed. Runtime checks the independent clean lifecycle first.
func (b *Controller) loadHistory(raw []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	var previous State
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&previous) != nil || d.Decode(&struct{}{}) != io.EOF {
		return ErrUnsafe
	}
	if previous.Version == 0 && previous.Scope == "" && previous.Revision == "" && len(previous.History) == 0 {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || len(fields) != 5 || previous.At.IsZero() || previous.Changes == 0 || previous.Reason == "" || len(previous.Reason) > 128 || previous.Effective < 0 || previous.Effective > 1000 {
			return ErrUnsafe
		}
		for _, name := range []string{"At", "Effective", "Reason", "Frozen", "Changes"} {
			if _, ok := fields[name]; !ok {
				return ErrUnsafe
			}
		}
		return nil
	}
	if previous.AuditFault || !commandID.MatchString(previous.Revision) || previous.ManualLimit < 0 || previous.ManualLimit > b.cfg.Ceiling || previous.Version != 1 || previous.Scope != b.state.Scope || len(previous.History) > maxHistory || previous.Changes != uint64(len(previous.History)) {
		return ErrUnsafe
	}
	seen := map[string]bool{}
	keys := map[string]bool{}
	for _, r := range previous.History {
		if !commandID.MatchString(r.ID) || seen[r.ID] || r.Status != "confirmed" || r.At.IsZero() || len(r.Reason) > 128 || r.After < 0 || r.After > b.cfg.Ceiling || r.After > 0 && r.After < b.cfg.Base || r.Before < 0 || r.Before > b.cfg.Ceiling || r.Actor != "owner" && r.Actor != "controller" {
			return ErrUnsafe
		}
		if r.Key != "" {
			if !commandID.MatchString(r.Key) || keys[r.Key] || !commandID.MatchString(r.Expected) || (r.Action != "revert" && r.Action != "resume" && r.Action != "freeze") {
				return ErrUnsafe
			}
			keys[r.Key] = true
		}
		if r.RestoredFrom != "" && !seen[r.RestoredFrom] {
			return ErrUnsafe
		}
		seen[r.ID] = true
	}
	if len(previous.History) > 0 {
		last := previous.History[len(previous.History)-1]
		if last.ID != previous.Revision || last.After != previous.Effective {
			return ErrUnsafe
		}
	}
	b.state.History = append([]Revision(nil), previous.History...)
	b.state.Changes = previous.Changes
	return nil
}

func (b *Controller) historyView() HistoryView {
	start := len(b.state.History) - visibleHistory
	if start < 0 {
		start = 0
	}
	items := append([]Revision{}, b.state.History[start:]...)
	for i := range items {
		items[i].Key = ""
		items[i].Action = ""
		items[i].Expected = ""
	}
	return HistoryView{Items: items, Total: len(b.state.History), Limit: maxHistory}
}

func (b *Controller) failTransition(next State) error {
	b.auditFailed = true
	b.latched = true
	b.enabled = false
	_ = b.Scheduler.InstallLimits(b.snapshot(0, next.Changes).Limits)
	next.Effective = 0
	next.Frozen = true
	next.Reason = "audit_uncertain"
	next.AuditFault = true
	if len(next.History) > 0 && next.Revision != b.state.Revision {
		next.History[len(next.History)-1].Status = "uncertain"
	}
	b.state = next
	// Preserve intent if storage is broken. Even a successful uncertain write
	// must not become a confirmed revision or authorize a restart.
	_ = b.save(next)
	return ErrUnsafe
}

func (b *Controller) transition(now time.Time, n int, reason, actor, action string, cmd admin.Command) error {
	if b.auditFailed {
		return b.failTransition(cloneState(b.state))
	}
	next := cloneState(b.state)
	next.At = now
	next.Effective = n
	next.Reason = reason
	next.Frozen = !b.enabled || b.latched
	next.ManualLimit = b.manualLimit
	if n == b.state.Effective && next.Frozen == b.state.Frozen && next.ManualLimit == b.state.ManualLimit && cmd.IdempotencyKey == "" {
		b.state = next
		return nil
	}
	if len(next.History) >= maxHistory {
		return b.failTransition(next)
	}
	next.Changes++
	next.Revision = revisionID()
	record := Revision{ID: next.Revision, At: now.UTC(), Actor: actor, Status: "intent", Reason: reason, Before: b.state.Effective, After: n, RestoredFrom: cmd.Revision, Key: cmd.IdempotencyKey, Action: action, Expected: cmd.ExpectedRevision}
	next.History = append(next.History, record)
	snap := b.snapshot(n, next.Changes)
	var err error
	if n > b.state.Effective {
		err = b.Scheduler.InstallDurably(snap, false, func() error {
			if e := b.save(next); e != nil {
				return e
			}
			// Storage can be slow. Do not publish growth using evidence that expired
			// while the intent was written. Scheduler is locked, so do not call Stats.
			if !b.valid(time.Now()) {
				return ErrUnsafe
			}
			return nil
		})
	} else {
		err = b.save(next)
		if err == nil {
			err = b.Scheduler.InstallLimits(snap.Limits)
		}
	}
	if err != nil {
		return b.failTransition(next)
	}
	next.History[len(next.History)-1].Status = "confirmed"
	if b.save(next) != nil {
		return b.failTransition(next)
	}
	b.state = next
	return nil
}

func (b *Controller) execute(ctx context.Context, action string, cmd admin.Command) (any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ctx.Err() != nil || b.persist == nil || !commandID.MatchString(cmd.IdempotencyKey) || !commandID.MatchString(cmd.ExpectedRevision) {
		return nil, admin.ErrDenied
	}
	if action != "freeze" && action != "resume" && action != "revert" || action != "revert" && cmd.Revision != "" || action == "revert" && !commandID.MatchString(cmd.Revision) || cmd.Setup != nil || len(cmd.Groups) != 0 || cmd.RuleID != "" || cmd.Locked || cmd.SemanticPreviewHash != "" {
		return nil, admin.ErrDenied
	}
	for _, r := range b.state.History {
		if r.Key == cmd.IdempotencyKey {
			if r.Action != action || r.RestoredFrom != cmd.Revision || r.Expected != cmd.ExpectedRevision {
				return nil, admin.ErrConflict
			}
			return CommandResult{r.ID, r.Status}, nil
		}
	}
	if cmd.ExpectedRevision != b.state.Revision {
		return nil, admin.ErrConflict
	}
	if b.auditFailed || b.closing {
		return nil, ErrUnsafe
	}
	now := time.Now()
	n := 0
	reason := "owner_freeze"
	switch action {
	case "freeze":
		b.enabled = false
	case "resume":
		if !b.safeToEnable(now) {
			return nil, ErrUnsafe
		}
		b.enabled = true
		b.latched = false
		b.manualLimit = 0
		b.lastBusy = now
		n = b.cfg.Base
		reason = "owner_resume"
	case "revert":
		// Restoring is not a way around a safety stop. Resume is a separate,
		// reviewed operation after evidence and uncertain work are checked.
		if !b.enabled || b.latched || !b.safeToEnable(now) {
			return nil, ErrUnsafe
		}
		found := false
		for _, r := range b.state.History {
			if r.ID == cmd.Revision && r.Status == "confirmed" && r.After >= b.cfg.Base && r.After <= b.cfg.Ceiling {
				n = r.After
				found = true
				break
			}
		}
		if !found {
			return nil, admin.ErrDenied
		}
		b.manualLimit = n
		reason = "owner_restore"
	}
	if err := b.transition(now, n, reason, "owner", action, cmd); err != nil {
		return nil, err
	}
	return CommandResult{b.state.Revision, "confirmed"}, nil
}
func (b *Controller) safeToEnable(now time.Time) bool {
	return !b.closing && !b.auditFailed && b.valid(now) && b.health.CPU < .7 && b.health.Memory < .8 && b.Scheduler.Stats().Uncertain == 0
}
