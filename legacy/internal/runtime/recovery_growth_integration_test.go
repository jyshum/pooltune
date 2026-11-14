//go:build integration

package runtime

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"pooltune/internal/control"
	"pooltune/internal/controller"
	"pooltune/internal/domain"
	"pooltune/internal/pgwire"
	"pooltune/internal/policy"
	"pooltune/internal/scheduler"
	"pooltune/internal/store"
	"pooltune/internal/testpg"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func recoveryFixture(t *testing.T) (context.Context, *pgx.ConnConfig, Config) {
	ctx, db, cert := testpg.Start(t)
	return recoveryFixtureFor(t, ctx, db, cert)
}
func recoveryFixtureFor(t *testing.T, ctx context.Context, db *pgx.ConnConfig, cert tls.Certificate) (context.Context, *pgx.ConnConfig, Config) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pt-run-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
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

	cfg.Registry.Workloads = []policy.Workload{{ID: "lookup", Selector: policy.Selector{Target: "db", Path: domain.PathPostgres, Principal: "fixture_app"}, DefaultClass: domain.ClassNormal, AllowedClasses: []domain.Class{domain.ClassNormal, domain.ClassCritical}, MaxQueueWait: time.Second, MaxQueueLimit: 100, MaxConcurrency: 20}}
	cfg.Stable.Workloads = []policy.WorkloadSettings{{WorkloadID: "lookup", QueueWait: time.Second, QueueLimit: 100, ConcurrencyCap: 20}}

	cfg.RecoveryDirectory = filepath.Join(dir, "independent-anchor")
	os.Mkdir(cfg.RecoveryDirectory, 0700)
	return ctx, db, cfg
}

func TestIndependentLocalRecoveryChecksControlAndRealOrphan(t *testing.T) {
	ctx, db, cfg := recoveryFixture(t)
	traffic, err := NewPostgres(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cdir := filepath.Join(filepath.Dir(cfg.Directory), "control")
	os.Mkdir(cdir, 0700)
	ledger, err := store.OpenStore(filepath.Join(cdir, "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	intents := filepath.Join(cdir, "intents")
	os.Mkdir(intents, 0700)
	// Engine adapter supplies the same private operations as the Unix client.
	coord, err := control.NewCoordinator(cfg.Target, domain.PathPostgres, intents, ledger, engineRecoveryClient{traffic.Engine})
	if err != nil {
		t.Fatal(err)
	}
	if err = coord.AdoptInitial(ctx); err != nil {
		t.Fatal(err)
	}
	if err = traffic.Engine.Constrain(3); err != nil {
		t.Fatal(err)
	}
	if _, err = coord.SyncFallback(ctx); err != nil {
		t.Fatal(err)
	}
	coord.Close()
	traffic.Close()
	orphan, err := pgx.ConnectConfig(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = orphan.Exec(ctx, "BEGIN; SELECT 1"); err != nil {
		t.Fatal(err)
	}
	cfg.Epoch = 2
	traffic, err = NewPostgres(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer traffic.Close()
	if traffic.Engine.Ready() || traffic.Scheduler.Stats().Effective != 0 {
		t.Fatal("restart admitted before independent recovery")
	}
	coord, err = control.NewCoordinator(cfg.Target, domain.PathPostgres, intents, ledger, engineRecoveryClient{traffic.Engine})
	if err != nil {
		t.Fatal(err)
	}
	defer coord.Close()
	if err = coord.Recover(ctx); err == nil {
		t.Fatal("live old transaction accepted")
	}
	orphan.Close(ctx)
	// PostgreSQL may take a moment to remove the disconnected backend.
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = coord.Recover(ctx)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !traffic.Engine.Ready() || traffic.Engine.View().Active.Epoch != 2 || traffic.Scheduler.Stats().Effective != 3 || traffic.Engine.View().SafetyCeiling == nil || *traffic.Engine.View().SafetyCeiling != 3 {
		t.Fatal("verified recovery not installed")
	}
	if _, err = coord.SyncFallback(ctx); err != nil {
		t.Fatal(err)
	}
	coord.Close()
	traffic.Close()
	cfg.Epoch = 3
	traffic, err = NewPostgres(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer traffic.Close()
	coord, err = control.NewCoordinator(cfg.Target, domain.PathPostgres, intents, ledger, engineRecoveryClient{traffic.Engine})
	if err != nil {
		t.Fatal(err)
	}
	defer coord.Close()
	if err = coord.Recover(ctx); err != nil {
		t.Fatal("observed recovery payload not reusable", err)
	}
}

type engineRecoveryClient struct{ *control.Engine }

func (e engineRecoveryClient) View(context.Context) (control.StateView, error) {
	return e.Engine.View(), nil
}
func (e engineRecoveryClient) AcknowledgeFallback(_ context.Context, r domain.RevisionID, c string) error {
	return e.Engine.AcknowledgeFallback(r, c)
}
func (e engineRecoveryClient) Status(_ context.Context, r domain.RevisionID) (control.ActivationAck, error) {
	return e.Engine.Status(r), nil
}

func TestContinuousCapacityGrowthWithMeasuredPostgresBaseline(t *testing.T) {
	ctx, db, cfg := recoveryFixture(t)
	cfg.Capacity = 32
	cfg.StartCapacity = 20
	cfg.MaxSessions = 32
	cfg.Scheduler.MaxActive = 32
	cfg.Scheduler.MaxTenantActive = 32
	cfg.Registry.Workloads[0].MaxConcurrency = 32
	cfg.Stable.Workloads[0].ConcurrencyCap = 32
	baselineDB, stopCalibration := calibrationProxy(t, ctx, db, cfg)
	baselineConn, err := pgx.ConnectConfig(ctx, baselineDB)
	if err != nil {
		t.Fatal(err)
	}
	from := time.Now()
	var sum, sumSquares float64
	for i := 0; i < 100; i++ {
		start := time.Now()
		if _, err = baselineConn.Exec(ctx, "SELECT pg_sleep(0.05)"); err != nil {
			t.Fatal(err)
		}
		d := time.Since(start).Seconds()
		sum += d
		sumSquares += d * d
	}
	baselineConn.Close(ctx)
	stopCalibration()
	mean := sum / 100
	g := controller.Guardrails{Approved: true, Version: "g1", Floor: 1, Start: 20, Ceiling: 32, CPU: controller.Thresholds{Grow: .95, Backoff: .98, Stop: .999}, Memory: controller.Thresholds{Grow: .95, Backoff: .98, Stop: .999}, MaxAge: 3 * time.Second, ErrorLimit: .1, Baselines: map[string]controller.Baseline{"lookup": {Version: "local-measured-sleep", Approved: true, Samples: 100, MeanSeconds: mean, Variance: max(0, sumSquares/100-mean*mean), From: from, To: time.Now(), Capacity: 1, MaxOccupancy: time.Second}}}
	cfg.CapacitySafety = &g
	traffic, err := NewPostgres(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer traffic.Close()
	health, err := ListenLocal("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- traffic.Serve(runCtx, health) }()
	defer func() { stop(); <-done }()
	client, _ := control.NewClient(filepath.Join(cfg.Directory, "control.sock"))
	defer client.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err = client.View(ctx); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	dir := filepath.Join(filepath.Dir(cfg.Directory), "control")
	os.Mkdir(dir, 0700)
	evidence, _ := json.Marshal(g.Baselines)
	artifact := filepath.Join(dir, "capacity-evidence.json")
	os.WriteFile(artifact, evidence, 0600)
	hash := sha256.Sum256(evidence)
	cc := ControlConfig{Schema: "local-control-1", Directory: dir, Socket: filepath.Join(cfg.Directory, "control.sock"), CapacityCalibrationFile: artifact, Service: control.ServiceConfig{Target: cfg.Target, Path: domain.PathPostgres, ResourceSource: "local-host", Guardrails: cfg.Guardrails, Registry: cfg.Registry, Tick: 100 * time.Millisecond, Window: 5 * time.Second, MaxEvidenceAge: 3 * time.Second, Lease: time.Minute, Capacity: &control.CapacityProfile{EvidenceSHA256: hex.EncodeToString(hash[:]), Guardrails: g}}}
	owner, err := NewControl(ctx, cc)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	upstream := db.Copy()
	host, port, _ := net.SplitHostPort(cfg.Listen)
	upstream.Host = host
	n, _ := strconv.Atoi(port)
	upstream.Port = uint16(n)
	upstream.User = "fixture_app"
	upstream.ChannelBinding = "disable"
	loadCtx, cancelLoad := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() { cancelLoad(); workers.Wait() }()
	for i := 0; i < 28; i++ {
		conn, e := pgx.ConnectConfig(ctx, upstream)
		if e != nil {
			t.Fatal(e)
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer conn.Close(context.Background())
			for loadCtx.Err() == nil {
				if _, e := conn.Exec(loadCtx, "SELECT pg_sleep(0.05)"); e != nil {
					return
				}
			}
		}()
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err = owner.Service.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		v := traffic.Engine.View()
		if v.Active.Limits[0].Effective > 20 {
			if !strings.HasPrefix(string(v.Active.Revision), "capacity-") || v.Active.Limits[0].Effective != 25 {
				t.Fatal("growth exceeded verified sessions")
			}
			t.Logf("measured %d baseline samples, mean %.6fs; admission grew 20 -> %d with 28 authenticated sessions", 100, mean, v.Active.Limits[0].Effective)
			cancelLoad()
			workers.Wait()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	m, _ := client.CapacityMetrics(ctx)
	t.Fatalf("growth absent: capacity=%d cpu=%.3f memory=%.3f ready=%.0f complete=%v omitted=%d occupancy=%.3f groups=%+v", traffic.Scheduler.Stats().Effective, m.CPU.Value, m.Memory.Value, m.Ready.Value, m.Local.Complete, m.Local.Omitted, m.Local.MeanInFlight, m.Local.Groups)
}

// This helper runs in a genuinely separate OS process. The parent uses SIGKILL,
// so no Go cleanup or shared Engine state can provide recovery evidence.
func TestRecoveryProcessHelper(t *testing.T) {
	path := os.Getenv("POOLTUNE_TEST_RECOVERY_PROFILE")
	if path == "" {
		return
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	traffic, err := NewPostgres(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer traffic.Close()
	health, err := ListenLocal("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err = traffic.Serve(context.Background(), health); err != nil {
		t.Fatal(err)
	}
}
func TestIndependentRecoveryAfterTrafficProcessKill(t *testing.T) {
	ctx, _, cfg := recoveryFixture(t)
	path := filepath.Join(filepath.Dir(cfg.Directory), "process-profile.json")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var child *exec.Cmd
	stop := func() {
		if child != nil {
			_ = child.Process.Kill()
			_ = child.Wait()
			child = nil
		}
	}
	defer stop()
	client, _ := control.NewClient(filepath.Join(cfg.Directory, "control.sock"))
	defer client.Close()
	start := func() {
		t.Helper()
		raw, _ := json.Marshal(cfg)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		child = exec.Command(executable, "-test.run=^TestRecoveryProcessHelper$")
		child.Env = append(os.Environ(), "POOLTUNE_TEST_RECOVERY_PROFILE="+path)
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			v, err := client.View(ctx)
			if err == nil && v.Epoch == cfg.Epoch {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("traffic subprocess did not start")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	start()
	dir := filepath.Join(filepath.Dir(cfg.Directory), "process-control")
	os.Mkdir(dir, 0700)
	cc := ControlConfig{Schema: "local-control-1", Directory: dir, Socket: filepath.Join(cfg.Directory, "control.sock"), RecoverLocalPostgres: true, Service: control.ServiceConfig{Target: cfg.Target, Path: domain.PathPostgres, ResourceSource: "local-host", Guardrails: cfg.Guardrails, Registry: cfg.Registry, Tick: 100 * time.Millisecond, Window: 5 * time.Second, MaxEvidenceAge: 3 * time.Second}}
	owner, err := NewControl(ctx, cc)
	if err != nil {
		t.Fatal(err)
	}
	owner.Close()
	stop()
	cfg.Epoch = 2
	start()
	view, err := client.View(ctx)
	if err != nil || view.Ready {
		t.Fatal("new process skipped quarantine")
	}
	owner, err = NewControl(ctx, cc)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	view, err = client.View(ctx)
	if err != nil || !view.Ready || view.Active.Revision != "recovery-2" {
		t.Fatal("independent process recovery failed")
	}
	t.Log("SIGKILL, fresh traffic process, separately reopened SQLite control ledger, Unix recovery, epoch 2 verified")
}

// Baselines include the same TLS proxy/admission path, at zero queue pressure.
func calibrationProxy(t *testing.T, ctx context.Context, db *pgx.ConnConfig, cfg Config) (*pgx.ConnConfig, func()) {
	t.Helper()
	compiled, err := policy.Compile(cfg.Stable, cfg.Guardrails, cfg.Registry)
	if err != nil {
		t.Fatal(err)
	}
	limits := domain.LimitSnapshot{Target: cfg.Target, Path: domain.PathPostgres, Revision: "calibration", CapacityRevision: "calibration", GuardrailVersion: cfg.Guardrails.Version, Epoch: 1, Effective: 1, CohortBudgets: map[string]int{"stable": 1}}
	sched, err := scheduler.New(cfg.Scheduler, scheduler.Snapshot{Limits: limits, Policies: map[string]*policy.CompiledPolicy{"stable": compiled}})
	if err != nil {
		t.Fatal(err)
	}
	certBytes, _ := os.ReadFile(cfg.CertificateFile)
	keyBytes, _ := os.ReadFile(cfg.PrivateKeyFile)
	cert, err := tls.X509KeyPair(certBytes, keyBytes)
	if err != nil {
		t.Fatal(err)
	}
	server, err := pgwire.NewServer(pgwire.TargetConfig{Target: cfg.Target, Database: cfg.Database, Upstream: cfg.Upstream, ServerTLS: &tls.Config{Certificates: []tls.Certificate{cert}}, UpstreamTLS: db.TLSConfig, MaxSessions: 4, MaxHandshakes: 4, MaxMessageBytes: 1 << 20, HandshakeTimeout: time.Second, IOTimeout: time.Minute, Classify: func(user, database string) domain.Classification {
		c := policy.Classify(compiled, domain.RequestContext{Target: cfg.Target, Path: domain.PathPostgres, Principal: user})
		c.Cohort = "stable"
		c.ActivationRevision = "calibration"
		return c
	}}, sched)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := ListenLocal("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	running, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- server.Serve(running, listener) }()
	conn := db.Copy()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	conn.Host = host
	n, _ := strconv.Atoi(port)
	conn.Port = uint16(n)
	conn.User = "fixture_app"
	conn.ChannelBinding = "disable"
	return conn, func() { cancel(); <-done }
}

func TestPostgresSafetyStopResumeUsesRealIdleSessions(t *testing.T) {
	for _, ceiling := range []int{0, 1} {
		t.Run(fmt.Sprint(ceiling), func(t *testing.T) { testPostgresSafetyRecovery(t, ceiling) })
	}
}
func testPostgresSafetyRecovery(t *testing.T, ceiling int) {
	expected := ceiling
	if expected == 0 {
		expected = 2
	}
	ctx, db, cfg := recoveryFixture(t)
	cfg.Capacity = 4
	cfg.StartCapacity = 2
	cfg.MaxSessions = 4
	cfg.Scheduler.MaxActive = 4
	cfg.CapacitySafety = &controller.Guardrails{Approved: true, Version: "g1", Floor: 1, Start: 2, Ceiling: 4, CPU: controller.Thresholds{Grow: .998, Backoff: .999, Stop: .9999}, Memory: controller.Thresholds{Grow: .998, Backoff: .999, Stop: .9999}, MaxAge: 2 * time.Second, ErrorLimit: .1}
	traffic, err := NewPostgres(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer traffic.Close()
	health, _ := ListenLocal("127.0.0.1:0")
	run, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- traffic.Serve(run, health) }()
	defer func() { stop(); <-done }()
	owner := startRecoveryControl(t, ctx, cfg.Target, domain.PathPostgres, cfg.Directory, cfg.Registry, cfg.Guardrails)
	defer owner.Close()
	app := db.Copy()
	host, port, _ := net.SplitHostPort(cfg.Listen)
	app.Host = host
	n, _ := strconv.Atoi(port)
	app.Port = uint16(n)
	app.User = "fixture_app"
	app.ChannelBinding = "disable"
	conn, err := pgx.ConnectConfig(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	second, err := pgx.ConnectConfig(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(ctx)
	if err = traffic.Engine.Constrain(ceiling); err != nil {
		t.Fatal(err)
	}
	if err = owner.Service.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = owner.Service.Execute(ctx, "safety-resume", resumeCommand(t, ctx, owner, "early-pg")); err == nil {
		t.Fatal("resume skipped evidence interval")
	}
	awaitResume(t, ctx, owner)
	if _, err = owner.Service.Execute(ctx, "safety-resume", resumeCommand(t, ctx, owner, "verified-pg-resume")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond) // Observe later real pressure sampling ticks.
	if traffic.Scheduler.Stats().Effective != expected {
		t.Fatal("PG resume did not survive subsequent healthy ticks")
	}
	var value int
	if err = conn.QueryRow(ctx, "SELECT 1").Scan(&value); err != nil || value != 1 {
		t.Fatal("SQL failed after resume", err)
	}
	t.Logf("real host and pg_stat_activity evidence; two idle authenticated sessions; ceiling %d recovered at admission %d; later pressure ticks and SQL preserved", ceiling, expected)
}
