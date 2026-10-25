//go:build integration

package runtime

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/jackc/pgx/v5"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"pooltune/internal/admin"
	"pooltune/internal/control"
	"pooltune/internal/controller"
	"pooltune/internal/domain"
	"pooltune/internal/policy"
	"pooltune/internal/scheduler"
	"pooltune/internal/store"
	"pooltune/internal/testpg"
	"strconv"
	"testing"
	"time"
)

func restRecoveryFixture(t *testing.T) (context.Context, *pgx.ConnConfig, RESTConfig) {
	binary, e := exec.LookPath("postgrest")
	if e != nil {
		t.Skip("PostgREST executable unavailable")
	}
	if output, e := exec.Command(binary, "--version").CombinedOutput(); e != nil {
		t.Fatalf("PostgREST binary check: %s", output)
	}
	t.Setenv("PGRST_DB_URI", "postgresql://127.0.0.1:1/wrong")
	t.Setenv("PGRST_DB_POOL", "1000")
	ctx, db, cert := testpg.Start(t)
	dir, e := os.MkdirTemp("/tmp", "pt-rest-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	private := filepath.Join(dir, "secrets")
	state := filepath.Join(dir, "traffic")
	os.Mkdir(private, 0700)
	os.Mkdir(state, 0700)
	save := func(name string, b []byte) string {
		t.Helper()
		p := filepath.Join(private, name)
		if e := os.WriteFile(p, b, 0600); e != nil {
			t.Fatal(e)
		}
		return p
	}
	certFile := save("server.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}))
	keyFile := save("server.key", pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(cert.PrivateKey.(*rsa.PrivateKey))}))
	cohort := make([]byte, 32)
	for i := range cohort {
		cohort[i] = byte(i)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	b64 := base64.RawURLEncoding.EncodeToString
	jwks := fmt.Sprintf(`{"keys":[{"kty":"EC","crv":"P-256","alg":"ES256","use":"sig","kid":"k1","x":"%s","y":"%s"}]}`, b64(key.X.FillBytes(make([]byte, 32))), b64(key.Y.FillBytes(make([]byte, 32))))
	admin, e := pgx.ConnectConfig(ctx, db)
	if e != nil {
		t.Fatal(e)
	}
	_, e = admin.Exec(ctx, `CREATE ROLE rest_anon NOLOGIN; CREATE ROLE rest_user NOLOGIN; CREATE ROLE rest_login LOGIN NOINHERIT CONNECTION LIMIT 4 PASSWORD '`+db.Password+`'; GRANT rest_anon,rest_user TO rest_login;
 CREATE SCHEMA api; GRANT USAGE ON SCHEMA api TO rest_anon,rest_user;
 CREATE TABLE api.items(id integer primary key, owner text not null); INSERT INTO api.items VALUES (1,'alice'),(2,'bob'),(99,'public'); ALTER TABLE api.items ENABLE ROW LEVEL SECURITY;
 GRANT SELECT,INSERT ON api.items TO rest_user; GRANT SELECT ON api.items TO rest_anon; CREATE POLICY public_items ON api.items FOR SELECT TO rest_anon USING (owner='public');
 CREATE POLICY own_items ON api.items TO rest_user USING (owner=current_setting('request.jwt.claims',true)::json->>'sub') WITH CHECK (owner=current_setting('request.jwt.claims',true)::json->>'sub');`)
	if e != nil {
		t.Fatal(e)
	}
	admin.Close(ctx)
	restURI := url.URL{Scheme: "postgresql", Host: net.JoinHostPort(db.Host, fmt.Sprint(db.Port)), Path: "/" + db.Database, User: url.UserPassword("rest_login", db.Password)}
	reserve, _ := ListenLocal("127.0.0.1:0")
	listen := reserve.Addr().String()
	reserve.Close()
	cfg := RESTConfig{Schema: "local-rest-1", Target: "db", Database: db.Database, Upstream: net.JoinHostPort(db.Host, fmt.Sprint(db.Port)), Listen: listen, Directory: state, SecretRoot: private, CertificateFile: certFile, PrivateKeyFile: keyFile, CAFile: certFile, CohortKeyFile: save("cohort.key", []byte(hex.EncodeToString(cohort))), MonitorFile: save("monitor.dsn", []byte(db.ConnString())), PostgRESTDatabaseFile: save("rest.dsn", []byte(restURI.String())), PostgRESTExecutable: binary, DatabaseSchema: "api", AnonymousRole: "rest_anon", Issuer: "https://local.test", Audience: "authenticated", JWKSFile: save("jwks.json", []byte(jwks)), KeysValidUntil: time.Now().Add(8 * time.Second), Epoch: 1, Capacity: 20, PhysicalAllocation: 4, MaxRequests: 64, MaxHeaderBytes: 8192, MaxBodyBytes: 1 << 20, Timeout: 2 * time.Second,
		Guardrails: policy.Guardrails{Version: "g1", MaxChangedEntries: 5, MaxReserveFraction: .5, MinClassWeight: 1, MaxClassWeight: 10}, Registry: policy.Registry{Fallback: policy.Fallback{QueueWait: time.Second, QueueLimit: 100, ConcurrencyCap: 20}}, Stable: policy.Policy{SchemaVersion: "1", Revision: "p1", GuardrailVersion: "g1", ClassWeights: policy.ClassWeights{Critical: 1, Normal: 1, Background: 1}}, Scheduler: scheduler.Config{MaxActive: 20, MaxQueued: 100, MaxTenants: 100, MaxWorkloads: 10, MaxTenantQueued: 100, MaxTenantActive: 20, MaxQueuedBytes: 1 << 20, RatePerSecond: 10000, RateBurst: 10000}}
	for i := -1; i < 32; i++ {
		sub := fmt.Sprintf("subject-%d", i)
		if i < 0 {
			sub = "alice"
		}
		cfg.Registry.Workloads = append(cfg.Registry.Workloads, policy.Workload{ID: sub, Selector: policy.Selector{Target: "db", Path: domain.PathREST, Principal: sub}, IdentityDefault: true, DefaultClass: domain.ClassNormal, AllowedClasses: []domain.Class{domain.ClassNormal}, MaxQueueWait: time.Second, MaxQueueLimit: 100, MaxConcurrency: 20})
		cfg.Stable.Workloads = append(cfg.Stable.Workloads, policy.WorkloadSettings{WorkloadID: sub, QueueWait: time.Second, QueueLimit: 100, ConcurrencyCap: 20})
	}
	cfg.Scheduler.MaxWorkloads = 64
	cfg.CapacitySafety = &controller.Guardrails{Approved: true, Version: "g1", Floor: 1, Ceiling: 20, HTTPEnvelope: 20, Start: 20, CPU: controller.Thresholds{Grow: .998, Backoff: .999, Stop: .9999}, Memory: controller.Thresholds{Grow: .998, Backoff: .999, Stop: .9999}, MaxAge: 2 * time.Second, ErrorLimit: .1, WarmRatio: .1}

	cfg.KeysValidUntil = time.Now().Add(time.Hour)
	cfg.CapacitySafety.Start = 2
	cfg.RecoveryDirectory = filepath.Join(dir, "anchor")
	os.Mkdir(cfg.RecoveryDirectory, 0700)
	return ctx, db, cfg
}
func TestRESTAnchoredRecoveryPreservesHistoryAndRejectsOldWork(t *testing.T) {
	ctx, db, cfg := restRecoveryFixture(t)
	traffic, err := NewREST(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(filepath.Dir(cfg.Directory), "control")
	os.Mkdir(dir, 0700)
	ledger, err := store.OpenStore(filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	intents := filepath.Join(dir, "intents")
	os.Mkdir(intents, 0700)
	coord, err := control.NewCoordinator(cfg.Target, domain.PathREST, intents, ledger, engineRecoveryClient{traffic.Engine})
	if err != nil {
		t.Fatal(err)
	}
	if err = coord.AdoptInitial(ctx); err != nil {
		t.Fatal(err)
	}
	traffic.Engine.Quarantine()
	coord.Close()
	traffic.Close()
	orphan, err := pgx.ConnectConfig(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	orphan.Exec(ctx, "BEGIN; SELECT 1")
	cfg.Epoch = 2
	if next, err := NewREST(ctx, cfg); err == nil {
		next.Close()
		t.Fatal("REST recovery admitted over old transaction")
	}
	orphan.Close(ctx)
	traffic, err = NewREST(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer traffic.Close()
	if traffic.Engine.Ready() || traffic.Scheduler.Stats().Effective != 0 {
		t.Fatal("REST restart skipped quarantine")
	}
	coord, err = control.NewCoordinator(cfg.Target, domain.PathREST, intents, ledger, engineRecoveryClient{traffic.Engine})
	if err != nil {
		t.Fatal(err)
	}
	defer coord.Close()
	if err = coord.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if traffic.Scheduler.Stats().Effective != 2 || traffic.Engine.View().Active.Epoch != 2 {
		t.Fatal("REST recovery failed bounded start")
	}
}

func TestRESTSafetyStopResumeUsesRealHealthAndPostgREST(t *testing.T) {
	for _, ceiling := range []int{0, 1} {
		t.Run(fmt.Sprint(ceiling), func(t *testing.T) { testRESTSafetyRecovery(t, ceiling) })
	}
}
func testRESTSafetyRecovery(t *testing.T, ceiling int) {
	expected := ceiling
	if expected == 0 {
		expected = 2
	}
	ctx, db, cfg := restRecoveryFixture(t)
	traffic, err := NewREST(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer traffic.Close()
	health, _ := ListenLocal("127.0.0.1:0")
	run, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- traffic.Serve(run, health) }()
	defer func() { stop(); <-done }()
	owner := startRecoveryControl(t, ctx, cfg.Target, domain.PathREST, cfg.Directory, cfg.Registry, cfg.Guardrails)
	defer owner.Close()
	if err = traffic.Engine.Constrain(ceiling); err != nil {
		t.Fatal(err)
	}
	if err = owner.Service.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = owner.Service.Execute(ctx, "safety-resume", resumeCommand(t, ctx, owner, "too-early")); err == nil {
		t.Fatal("resumed before healthy window")
	}
	awaitResume(t, ctx, owner)
	if _, err = owner.Service.Execute(ctx, "safety-resume", resumeCommand(t, ctx, owner, "verified-rest-resume")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond) // Observe later real pressure sampling ticks.
	if traffic.Scheduler.Stats().Effective != expected || traffic.Engine.View().SafetyCeiling != nil {
		t.Fatal("REST resume did not survive subsequent healthy ticks")
	}
	httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: db.TLSConfig.RootCAs, MinVersion: tls.VersionTLS12}}, Timeout: time.Second}
	defer httpClient.CloseIdleConnections()
	response, err := httpClient.Get("https://" + cfg.Listen + "/rest/v1/items")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 || !bytes.Contains(body, []byte("public")) || bytes.Contains(body, []byte("alice")) {
		t.Fatal("resumed REST lost RLS")
	}
	t.Logf("real five-second host/backend evidence; ceiling %d recovered at HTTP admission %d; later pressure ticks and anonymous RLS preserved", ceiling, expected)
}
func startRecoveryControl(t *testing.T, ctx context.Context, target string, path domain.PathKind, traffic string, registry policy.Registry, bounds policy.Guardrails) *Control {
	t.Helper()
	socket := filepath.Join(traffic, "control.sock")
	client, _ := control.NewClient(socket)
	defer client.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := client.View(ctx); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("control socket unavailable")
		}
		time.Sleep(10 * time.Millisecond)
	}
	dir := filepath.Join(filepath.Dir(traffic), "running-control")
	os.Mkdir(dir, 0700)
	c := ControlConfig{Schema: "local-control-1", Directory: dir, Socket: socket, RecoverLocalPostgres: path == domain.PathPostgres, RecoverLocalREST: path == domain.PathREST, Service: control.ServiceConfig{Target: target, Path: path, ResourceSource: "local-host", Registry: registry, Guardrails: bounds, Tick: 100 * time.Millisecond, Window: 5 * time.Second, MaxEvidenceAge: 3 * time.Second}}
	owner, err := NewControl(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	return owner
}
func resumeCommand(t *testing.T, ctx context.Context, owner *Control, key string) admin.Command {
	t.Helper()
	value, err := owner.Service.Read(ctx, "state")
	if err != nil {
		t.Fatal(err)
	}
	return admin.Command{ExpectedRevision: value.(map[string]any)["revision"].(string), IdempotencyKey: key}
}
func awaitResume(t *testing.T, ctx context.Context, owner *Control) {
	t.Helper()
	deadline := time.Now().Add(9 * time.Second)
	for {
		if err := owner.Service.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		v, err := owner.Service.Read(ctx, "state")
		if err != nil {
			t.Fatal(err)
		}
		if v.(map[string]any)["safety"].(map[string]any)["resume_ready"] == true {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("healthy resume evidence unavailable")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestRESTCrashProcessHelper(t *testing.T) {
	path := os.Getenv("POOLTUNE_TEST_REST_PROFILE")
	if path == "" {
		return
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadREST(file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	traffic, err := NewREST(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer traffic.Close()
	if err = os.WriteFile(filepath.Join(cfg.Directory, "test-child.pid"), []byte(strconv.Itoa(traffic.child.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}
	health, _ := ListenLocal("127.0.0.1:0")
	if err = traffic.Serve(context.Background(), health); err != nil {
		t.Fatal(err)
	}
}
func TestRESTProcessKillFencesSurvivingChildThenRecovers(t *testing.T) {
	ctx, db, cfg := restRecoveryFixture(t)
	profile := filepath.Join(filepath.Dir(cfg.Directory), "rest-process.json")
	raw, _ := json.Marshal(cfg)
	os.WriteFile(profile, raw, 0600)
	exe, _ := os.Executable()
	parent := exec.Command(exe, "-test.run=^TestRESTCrashProcessHelper$")
	parent.Env = append(os.Environ(), "POOLTUNE_TEST_REST_PROFILE="+profile)
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { parent.Process.Kill(); parent.Wait() }()
	owner := startRecoveryControl(t, ctx, cfg.Target, domain.PathREST, cfg.Directory, cfg.Registry, cfg.Guardrails)
	owner.Close()
	pidBytes, err := os.ReadFile(filepath.Join(cfg.Directory, "test-child.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(pidBytes))
	if err != nil || pid <= 0 {
		t.Fatal("invalid owned child PID")
	}
	child, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Kill()
	if err = parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	parent.Wait()
	// Even with the old socket removed (as a backup restore may do), a live child
	// retains cluster ownership. Its idle pool is not proof of process death.
	if err = os.Remove(filepath.Join(cfg.Directory, "postgrest.sock")); err != nil {
		t.Fatal(err)
	}
	cfg.Epoch = 2
	if replacement, err := NewREST(ctx, cfg); err == nil {
		replacement.Close()
		t.Fatal("live orphan child lost ownership after parent SIGKILL")
	}
	child.Kill()
	deadline := time.Now().Add(3 * time.Second)
	for {
		lease, err := acquireLocalDatabaseOwner(ctx, db)
		if err == nil {
			lease.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owned child did not release cluster lease")
		}
		time.Sleep(20 * time.Millisecond)
	}
	replacement, err := NewREST(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if replacement.Engine.Ready() {
		t.Fatal("REST restart skipped independent ledger")
	}
	health, _ := ListenLocal("127.0.0.1:0")
	run, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- replacement.Serve(run, health) }()
	defer func() { stop(); <-done }()
	owner = startRecoveryControl(t, ctx, cfg.Target, domain.PathREST, cfg.Directory, cfg.Registry, cfg.Guardrails)
	defer owner.Close()
	if !replacement.Engine.Ready() || replacement.Engine.View().Active.Revision != "recovery-2" {
		t.Fatal("REST independent recovery failed")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: db.TLSConfig.RootCAs}}, Timeout: time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Get("https://" + cfg.Listen + "/rest/v1/items")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 || !bytes.Contains(body, []byte("public")) || bytes.Contains(body, []byte("alice")) {
		t.Fatal("recovered child lost SQL/RLS behavior")
	}
	t.Log("actual traffic process SIGKILL; surviving PostgREST fenced despite deleted socket; owned child termination; new child + independent SQLite confirmation + RLS")
}

func TestRESTRecoveryRejectsDeadChild(t *testing.T) {
	ctx, _, cfg := restRecoveryFixture(t)
	traffic, err := NewREST(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer traffic.Close()
	traffic.Engine.Quarantine()
	traffic.child.Process.Kill()
	done := <-traffic.childDone
	traffic.childDone <- done
	if err = traffic.Engine.Recover(ctx); err == nil {
		t.Fatal("recovery admitted without live PostgREST")
	}
	if traffic.Engine.Ready() {
		t.Fatal("dead child reported ready")
	}
}
