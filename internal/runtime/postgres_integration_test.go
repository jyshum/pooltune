//go:build integration

package runtime

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"pooltune/internal/control"
	"pooltune/internal/domain"
	"pooltune/internal/policy"
	"pooltune/internal/rollout"
	"pooltune/internal/scheduler"
	"pooltune/internal/store"
	"pooltune/internal/testpg"
	"testing"
	"time"
)

func TestLocalRuntimeRealSocketTrafficReadinessAndRestartQuarantine(t *testing.T) {
	ctx, db, cert := testpg.Start(t)
	dir, err := os.MkdirTemp("/tmp", "pt-run-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	private := filepath.Join(dir, "secrets")
	state := filepath.Join(dir, "traffic")
	os.Mkdir(private, 0700)
	os.Mkdir(state, 0700)
	save := func(name string, b []byte) string {
		t.Helper()
		path := filepath.Join(private, name)
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	certFile := save("server.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}))
	keyFile := save("server.key", pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(cert.PrivateKey.(*rsa.PrivateKey))}))
	key := make([]byte, 32)
	rand.Read(key)
	cohortFile := save("cohort.key", []byte(hex.EncodeToString(key)))
	monitorFile := save("monitor.dsn", []byte(db.ConnString()))
	reserve, err := ListenLocal("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := reserve.Addr().String()
	reserve.Close()
	cfg := Config{Schema: "local-pg-1", Target: "db", Database: "postgres", Upstream: net.JoinHostPort(db.Host, fmt.Sprint(db.Port)), Listen: listen, Directory: state, SecretRoot: private, CertificateFile: certFile, PrivateKeyFile: keyFile, CAFile: certFile, CohortKeyFile: cohortFile, MonitorFile: monitorFile, Epoch: 1, Capacity: 20, MaxSessions: 24,
		Guardrails: policy.Guardrails{Version: "g1", MaxChangedEntries: 5, MaxReserveFraction: .5, MinClassWeight: 1, MaxClassWeight: 10}, Registry: policy.Registry{Fallback: policy.Fallback{QueueWait: time.Second, QueueLimit: 100, ConcurrencyCap: 20}}, Stable: policy.Policy{SchemaVersion: "1", Revision: "p1", GuardrailVersion: "g1", ClassWeights: policy.ClassWeights{Critical: 1, Normal: 1, Background: 1}}, Scheduler: scheduler.Config{MaxActive: 20, MaxQueued: 100, MaxTenants: 100, MaxWorkloads: 10, MaxTenantQueued: 100, MaxTenantActive: 20, MaxQueuedBytes: 1 << 20, RatePerSecond: 10000, RateBurst: 10000}}
	body, _ := json.Marshal(cfg)
	profile := filepath.Join(dir, "runtime.json")
	os.WriteFile(profile, body, 0600)
	health, err := ListenLocal("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer health.Close()
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- RunFile(runCtx, profile, health) }()
	httpClient := &http.Client{Timeout: 100 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for {
		res, e := httpClient.Get("http://" + health.Addr().String() + "/readyz")
		if e == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			stop()
			select {
			case err := <-done:
				t.Fatalf("startup: %v", err)
			case <-time.After(time.Second):
				t.Fatal("runtime never ready")
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	client, err := control.NewClient(filepath.Join(state, "control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var v control.StateView
	for {
		v, err = client.View(ctx)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	app := db.Copy()
	app.User = "fixture_app"
	app.ChannelBinding = "disable"
	host, port, _ := net.SplitHostPort(listen)
	app.Host = host
	var nport int
	fmt.Sscan(port, &nport)
	app.Port = uint16(nport)
	conn, err := pgx.ConnectConfig(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	var user string
	if err = conn.QueryRow(ctx, "SELECT current_user").Scan(&user); err != nil || user != "fixture_app" {
		t.Fatal("original role not preserved")
	}
	p2 := cfg.Stable
	p2.Revision = "p2"
	p2.ExpectedPredecessor = "p1"
	p2.ClassWeights.Normal = 2
	controlDir := filepath.Join(dir, "control")
	os.Mkdir(controlDir, 0700)
	ledger, err := store.OpenStore(filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	coordinator, err := control.NewCoordinator("db", domain.PathPostgres, controlDir, ledger, client)
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.Close()
	if _, err = coordinator.Apply(ctx, v.Active); err != nil {
		t.Fatal(err)
	}
	driver, err := control.NewRolloutDriver("db", domain.PathPostgres, coordinator, client, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = driver.ConfigureMeasurements(100*time.Millisecond, "local-host"); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("runtime-experiment"))
	command := rollout.Command{ExperimentID: "runtime-experiment", Revision: "rollout-" + hex.EncodeToString(hash[:8]) + "-1", ExpectedRevision: string(v.Active.Revision), GuardrailVersion: "g1", CapacityRevision: "initial-capacity", Path: domain.PathPostgres, Epoch: 1, Phase: rollout.Measuring, Effective: 20, StableBudget: 18, CandidateBudget: 2, Fraction: .1, StablePolicy: cfg.Stable, CandidatePolicy: p2, Expires: time.Now().Add(20 * time.Second)}
	activated, err := driver.Apply(ctx, command)
	if err != nil || !activated.Active {
		t.Fatalf("driver activation: %+v %v", activated, err)
	}
	v, err = client.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	env := v.Active
	if err = client.BeginWindow(ctx, control.WindowRequest{Revision: env.Revision, Duration: 100 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err = conn.Exec(ctx, "SELECT 1"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = conn.Exec(ctx, "SELECT 1/0"); err == nil {
		t.Fatal("SQL failure missing")
	}
	var measured control.WindowSnapshot
	for {
		measured, err = client.ReadWindow(ctx)
		if err == nil && measured.Window.Complete && !measured.Resources.At.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("measurement window did not close")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if measured.Revision != env.Revision || !measured.Window.Complete || len(measured.Window.Groups) != 1 {
		t.Fatal("wrong measurement scope")
	}
	group := measured.Window.Groups[0]
	if group.Stable.Arrivals+group.Candidate.Arrivals != 11 || group.Stable.Successes+group.Candidate.Successes != 10 || group.Stable.Failed+group.Candidate.Failed != 1 || group.Stable.Pending+group.Candidate.Pending != 0 {
		t.Fatalf("real SQL outcome coverage: %+v", group)
	}
	if measured.Resources.Source != "local-host" || measured.Resources.CPU < 0 || measured.Resources.CPU > 1 || measured.Resources.Memory < 0 || measured.Resources.Memory > 1 {
		t.Fatal("invalid host resource sample")
	}
	t.Logf("Actual local-host pressure: CPU %.4f, memory %.4f; includes all local host processes, not managed database telemetry", measured.Resources.CPU, measured.Resources.Memory)
	t.Log("Measured private socket window: 11 arrivals, 10 successes, 1 SQL failure, no pending; success-only histograms and bounded aggregate payload.")
	experiment := rollout.Experiment{ID: command.ExperimentID, Target: "db", Path: domain.PathPostgres, Epoch: 1, StableRevision: "p1", CandidateRevision: "p2", GuardrailVersion: "g1", CapacityRevision: "initial-capacity", StageStarted: activated.At}
	metrics, err := driver.Metrics(ctx, experiment)
	if err != nil || !metrics.Complete || metrics.ResourceAt.IsZero() || metrics.ExperimentID != experiment.ID || metrics.Window.Start.Before(experiment.StageStarted) {
		t.Fatalf("actual rollout measurements unavailable: %v", err)
	}
	if _, err = driver.Abort(ctx, experiment.ID, "test_completed"); err != nil {
		t.Fatal(err)
	}
	t.Log("Real rollout driver: SQLite intent/confirmation, Unix activation, measured outcome window and local-host pressure, scoped rollback/history reconciliation.")
	if _, err = conn.Exec(ctx, "SELECT 2"); err != nil {
		t.Fatal(err)
	}
	conn.Close(ctx)
	v, err = client.View(ctx)
	if err != nil || v.Active.Revision == env.Revision || !v.Ready {
		t.Fatal("socket fallback missing")
	}
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runtime shutdown hung")
	}
	cfg.Epoch = 2
	if restarted, err := NewPostgres(ctx, cfg); err == nil {
		restarted.Close()
		t.Fatal("restart admitted without independent recovery proof")
	}
	t.Log("Actual runtime assembly: private secret files, verified TLS/SCRAM, SQL forwarding, readiness, Unix canary activation/rollback, clean shutdown, restart quarantine. No cloud/model calls.")
}
