//go:build managedlive

package tuning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"pooltune/internal/admin"
	"pooltune/internal/domain"
	"pooltune/internal/durable"
	"pooltune/internal/provider"
	"pooltune/internal/store"
	"strings"
	"sync"
	"testing"
	"time"
)

type liveRequest struct {
	Scheduled time.Time
	Phase     string
}
type liveOutcome struct {
	Phase     string
	LatencyMS float64
	Error     string
}
type livePoint struct {
	Seconds float64
	View    View
	Error   string
}
type liveTrial struct {
	Name       string
	Experiment liveExperimentProfile
	Outcomes   []liveOutcome
	Points     []livePoint
	Complete   bool
	Failure    string
}

func liveSQLConfig(t *testing.T, root string, port int, db string) *pgx.ConnConfig {
	t.Helper()
	c, e := pgx.ParseConfig(fmt.Sprintf("host=example.invalid port=%d user=pooltuneradmin dbname=%s sslmode=verify-full sslrootcert=/etc/ssl/cert.pem application_name=pooltune_managed_trial connect_timeout=8", port, db))
	if e != nil {
		t.Fatal("SQL config")
	}
	raw, e := os.ReadFile(filepath.Join(root, "private-cloud-credentials/azure-db-password.txt"))
	if e != nil {
		t.Fatal("private password file")
	}
	c.Host = liveTargetEnv(t, "POOLTUNE_TEST_AZURE_HOST")
	c.User = liveTargetEnv(t, "POOLTUNE_TEST_AZURE_USER")
	c.Password = strings.TrimSpace(string(raw))
	c.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	return c
}
func liveAzureSize(ctx context.Context, size int) error {
	cmd := exec.CommandContext(ctx, "/opt/homebrew/bin/az", "postgres", "flexible-server", "parameter", "set", "--subscription", os.Getenv("POOLTUNE_TEST_AZURE_SUBSCRIPTION"), "--resource-group", os.Getenv("POOLTUNE_TEST_AZURE_RESOURCE_GROUP"), "--server-name", os.Getenv("POOLTUNE_TEST_AZURE_INSTANCE"), "--name", "pgbouncer.default_pool_size", "--value", fmt.Sprint(size), "-o", "none")
	if _, e := cmd.Output(); e != nil {
		return fmt.Errorf("Azure setting request failed")
	}
	return nil
}
func liveError(e error) string {
	if e == nil {
		return ""
	}
	if p, ok := e.(*pgconn.PgError); ok {
		return "sqlstate:" + p.Code
	}
	if errors.Is(e, context.DeadlineExceeded) {
		return "deadline"
	}
	return "transport_or_context"
}

type liveJournal struct {
	*store.Store
	path string
}

func (j *liveJournal) Save(op provider.Operation) error {
	e := j.Store.Save(op)
	if e != nil {
		u, _ := url.Parse(op.ProviderID)
		path := ""
		if u != nil {
			path = u.Path
		}
		prior, _ := j.Store.Load(op.Target.ID)
		var priorMetadata map[string]any
		if prior != nil {
			priorPath := ""
			if u, e := url.Parse(prior.ProviderID); e == nil {
				priorPath = u.Path
			}
			priorMetadata = map[string]any{"state": prior.State, "provider_id_length": len(prior.ProviderID), "operation_path": priorPath}
		}
		raw, _ := json.Marshal(map[string]any{"state": op.State, "provider_id_length": len(op.ProviderID), "operation_path": path, "error_type": fmt.Sprintf("%T", e), "conflict": e == store.ErrConflict, "prior": priorMetadata, "proposed_before": op.Before, "proposed_time": op.StartedAt, "time_equal": prior != nil && prior.StartedAt == op.StartedAt})
		os.WriteFile(j.path, raw, 0600)
	}
	return e
}
func TestAzureManagedComparison(t *testing.T) {
	profile, profileErr := liveExperiment(os.Getenv("POOLTUNE_MANAGED_PROFILE"))
	if profileErr != nil {
		t.Fatal(profileErr)
	}
	root := os.Getenv("POOLTUNE_MANAGED_WORK")
	if root == "" || os.Getenv("POOLTUNE_MANAGED_WRITE") != "approved-test" {
		t.Skip("requires explicit isolated live test authorization")
	}
	window := filepath.Join(root, "managed-support")
	if v := os.Getenv("POOLTUNE_MANAGED_WINDOW"); v != "" {
		window = v
	}
	if _, e := os.Stat(filepath.Join(window, "azure-ready")); e != nil {
		t.Fatal("bounded Azure window not ready")
	}
	for _, key := range []string{"SUBSCRIPTION", "RESOURCE_GROUP", "INSTANCE", "HOST", "USER"} {
		liveTargetEnv(t, "POOLTUNE_TEST_AZURE_"+key)
	}
	tokenRaw, e := exec.Command("/opt/homebrew/bin/az", "account", "get-access-token", "--subscription", os.Getenv("POOLTUNE_TEST_AZURE_SUBSCRIPTION"), "--resource", "https://management.azure.com/", "--query", "accessToken", "-o", "tsv").Output()
	if e != nil {
		t.Fatal("token unavailable")
	}
	token := strings.TrimSpace(string(tokenRaw))
	api := provider.Config{Kind: provider.Azure, Target: provider.Target{ID: "azure-managed-test"}, Subscription: os.Getenv("POOLTUNE_TEST_AZURE_SUBSCRIPTION"), ResourceGroup: os.Getenv("POOLTUNE_TEST_AZURE_RESOURCE_GROUP"), Instance: os.Getenv("POOLTUNE_TEST_AZURE_INSTANCE"), Timeout: 10 * time.Second, Freshness: time.Second, Token: func(context.Context) (string, error) { return token, nil }}
	trials := []liveTrial{}
	suffix := os.Getenv("POOLTUNE_MANAGED_CASE")
	filename := "azure-comparison.json"
	if suffix != "" {
		filename = "azure-comparison-repaired.json"
	}
	if suffix == "fixed-warm-5" {
		filename = "azure-fixed5.json"
	}
	if profile.Name == "provider-default" {
		filename = "azure-provider-default.json"
		if suffix == "fixed-warm-64" {
			filename = "azure-provider-default-fixed64.json"
		}
	}
	save := func() {
		raw, _ := json.MarshalIndent(trials, "", "  ")
		os.WriteFile(filepath.Join(root, "managed-support/"+filename), raw, 0600)
	}
	defer save()
	// Window supervisor independently restores original5/min0 and removes its rule.
	cases := profile.Cases
	if (profile.Name == "legacy-undersized" && suffix == "fixed-warm-5") || (profile.Name == "provider-default" && suffix == "fixed-warm-64") {
		cases = []string{suffix}
	}
	if suffix != "" {
		found := false
		for _, mode := range cases {
			if mode == suffix {
				found = true
			}
		}
		if !found {
			t.Fatal("unknown case for experiment profile")
		}
	}
	for _, mode := range cases {
		if suffix != "" && mode != suffix {
			continue
		}
		trial := liveTrial{Name: mode, Experiment: profile}
		trials = append(trials, trial)
		out := &trials[len(trials)-1]
		save()
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		adaptive := strings.HasPrefix(mode, "adaptive-")
		size := profile.Maximum
		if adaptive || mode == "fixed-warm-5" || mode == "fixed-warm-50" {
			size = profile.Minimum
		}
		if e := func() error {
			if mode == "fixed-warm-5" {
				return nil
			}
			return liveAzureSize(ctx, size)
		}(); e != nil {
			out.Failure = "setup_configuration"
			cancel()
			t.Fatal(out.Failure)
		}
		dir, e := os.MkdirTemp(filepath.Join(root, "managed-support"), "azure-state-")
		if e != nil {
			cancel()
			t.Fatal(e)
		}
		os.Chmod(dir, 0700)
		files, e := durable.Open(dir)
		if e != nil {
			cancel()
			t.Fatal(e)
		}
		j, e := store.OpenStore(filepath.Join(dir, "control.db"))
		if e != nil {
			files.Close()
			cancel()
			t.Fatal(e)
		}
		journal := &liveJournal{Store: j, path: filepath.Join(root, "managed-support/azure-journal-diagnostic.json")}
		p, e := provider.NewAzurePool(provider.AzurePoolConfig{ExclusiveManagement: true, API: api, Admin: liveSQLConfig(t, root, 6432, "pgbouncer"), Monitor: liveSQLConfig(t, root, 5432, "postgres"), Role: "pooltuneradmin", Database: "postgres", Maximum: profile.Maximum, OtherReserve: 32, Journal: journal})
		if e != nil {
			t.Fatal(e)
		}
		b := Bounds{Minimum: profile.Minimum, Maximum: profile.Maximum, CPUGrow: .7, CPUBackoff: .85, MemoryGrow: .8, MemoryBackoff: .9}
		s, e := NewManaged(api.Target.ID, b, p, j, files, p.Source())
		if e != nil {
			t.Fatal(e)
		}
		clients := make([]*pgx.Conn, profile.Clients)
		for i := range clients {
			clients[i], e = pgx.ConnectConfig(ctx, liveSQLConfig(t, root, 6432, "postgres"))
			if e != nil {
				out.Failure = "client_connect"
				break
			}
		}
		if out.Failure != "" {
			t.Fatal(out.Failure)
		}
		var warm sync.WaitGroup
		for _, c := range clients {
			warm.Add(1)
			go func(c *pgx.Conn) { defer warm.Done(); c.Exec(ctx, "SELECT pg_sleep(0.2)") }(c)
		}
		warm.Wait()
		pressure, e := p.Pressure(ctx)
		if e != nil {
			out.Failure = "pressure_unavailable"
			t.Fatal(out.Failure)
		}
		if e = s.Tick(ctx, time.Now(), domain.Resources{At: pressure.At, Source: pressure.Source, CPU: pressure.CPU, Memory: pressure.Memory}); e != nil {
			out.Failure = "initial_observation: " + s.View(time.Now()).Status
			t.Fatal(out.Failure)
		}
		if s.View(time.Now()).Observation.Target != size {
			out.Failure = "baseline_setting_not_observed"
			t.Fatal(out.Failure)
		}
		if adaptive {
			v := s.View(time.Now())
			if _, e = s.Execute(ctx, "resume", admin.Command{ExpectedRevision: v.Revision, IdempotencyKey: "approved-live-test"}); e != nil {
				out.Failure = "resume_refused"
				t.Fatal(out.Failure)
			}
		}
		origin := time.Now()
		var mu sync.Mutex
		done := make(chan struct{})
		tickDone := make(chan struct{})
		go func() {
			defer close(tickDone)
			timer := time.NewTicker(time.Second)
			defer timer.Stop()
			lastPressure := time.Now()
			for {
				select {
				case <-done:
					return
				case <-ctx.Done():
					return
				case <-timer.C:
					if time.Since(lastPressure) > 5*time.Second {
						var e error
						pressure, e = p.Pressure(ctx)
						if e != nil {
							pressure = provider.Pressure{}
						}
						lastPressure = time.Now()
					}
					e := s.Tick(ctx, time.Now(), domain.Resources{At: pressure.At, Source: pressure.Source, CPU: pressure.CPU, Memory: pressure.Memory})
					point := livePoint{Seconds: time.Since(origin).Seconds(), View: s.View(time.Now())}
					if e != nil {
						point.Error = "observation_or_control_held"
					}
					mu.Lock()
					out.Points = append(out.Points, point)
					mu.Unlock()
				}
			}
		}()
		clientConfig := liveSQLConfig(t, root, 6432, "postgres")
		jobs := make(chan liveRequest, 64)
		var workers sync.WaitGroup
		for _, c := range clients {
			workers.Add(1)
			go func(c *pgx.Conn) {
				defer workers.Done()
				defer func() {
					if c != nil {
						c.Close(context.Background())
					}
				}()
				for job := range jobs {
					qctx, qcancel := context.WithDeadline(ctx, job.Scheduled.Add(3*time.Second))
					e := executeLiveRead(qctx, &c, clientConfig)
					qcancel()
					mu.Lock()
					out.Outcomes = append(out.Outcomes, liveOutcome{Phase: job.Phase, LatencyMS: float64(time.Since(job.Scheduled).Microseconds()) / 1000, Error: liveError(e)})
					mu.Unlock()
				}
			}(c)
		}
		phases := profile.Phases
		for _, phase := range phases {
			if profile.Name == "provider-default" {
				phaseStart := time.Now()
				for _, job := range liveArrivals(phaseStart, phase) {
					if delay := time.Until(job.Scheduled); delay > 0 {
						timer := time.NewTimer(delay)
						select {
						case <-timer.C:
						case <-ctx.Done():
							timer.Stop()
						}
					}
					mu.Lock()
					if ctx.Err() != nil {
						out.Outcomes = append(out.Outcomes, liveOutcome{Phase: job.Phase, Error: "trial_context_ended"})
					} else {
						select {
						case jobs <- job:
						default:
							out.Outcomes = append(out.Outcomes, liveOutcome{Phase: job.Phase, Error: "generator_queue_full"})
						}
					}
					mu.Unlock()
				}
				if remaining := time.Until(phaseStart.Add(phase.Duration)); remaining > 0 {
					timer := time.NewTimer(remaining)
					select {
					case <-timer.C:
					case <-ctx.Done():
						timer.Stop()
					}
				}
				continue
			}
			end := time.Now().Add(phase.Duration)
			if phase.Rate == 0 {
				select {
				case <-time.After(phase.Duration):
				case <-ctx.Done():
				}
				continue
			}
			timer := time.NewTicker(time.Second / time.Duration(phase.Rate))
			for now := range timer.C {
				if now.After(end) {
					break
				}
				job := liveRequest{Scheduled: now, Phase: phase.Name}
				select {
				case jobs <- job:
				default:
					mu.Lock()
					out.Outcomes = append(out.Outcomes, liveOutcome{Phase: phase.Name, Error: "generator_queue_full"})
					mu.Unlock()
				}
			}
			timer.Stop()
		}
		close(jobs)
		workers.Wait()
		close(done)
		<-tickDone
		for _, c := range clients {
			c.Close(context.Background())
		}
		p.Close()
		j.Close()
		files.Close()
		cancel()
		out.Complete = true
		save()
		t.Logf("%s complete: %d requests, %d observations", mode, len(out.Outcomes), len(out.Points))
	}
}

func TestLiveJournalDiagnosticDoesNotLeakOpaqueQuery(t *testing.T) {
	dir := t.TempDir()
	j, e := store.OpenStore(filepath.Join(dir, "control.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	op := provider.Operation{Target: provider.Target{ID: "db"}, Kind: provider.Azure, Revision: "op", Parameter: "pgbouncer.default_pool_size", Value: 7, Before: 5, StartedAt: time.Now().UTC(), State: "intent"}
	if e = j.Save(op); e != nil {
		t.Fatal(e)
	}
	op.State = "accepted"
	op.ProviderID = "https://management.azure.com/subscriptions/sub/operations/op?t=DO_NOT_EXPORT_SENTINEL"
	if e = j.Save(op); e != nil {
		t.Fatal(e)
	}
	wrapper := &liveJournal{Store: j, path: filepath.Join(dir, "diagnostic.json")}
	op.State = "complete"
	op.Value = 8
	if e = wrapper.Save(op); e == nil {
		t.Fatal("expected conflicting transition")
	}
	raw, e := os.ReadFile(wrapper.path)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(raw), "DO_NOT_EXPORT_SENTINEL") {
		t.Fatal("opaque query leaked through prior operation")
	}
}

func executeLiveRead(ctx context.Context, c **pgx.Conn, cfg *pgx.ConnConfig) error {
	if *c == nil || (*c).IsClosed() {
		next, e := pgx.ConnectConfig(ctx, cfg)
		if e != nil {
			return e
		}
		*c = next
	}
	_, e := (*c).Exec(ctx, "/*pooltune-managed-read-only*/ SELECT pg_sleep(0.2)")
	return e
}

// Live cloud targets are supplied explicitly by the operator, never embedded in source.
func liveTargetEnv(t *testing.T, key string) string {
	t.Helper()
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		t.Fatalf("required live target environment variable: %s", key)
	}
	return v
}
