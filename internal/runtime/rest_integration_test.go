//go:build integration

package runtime

import (
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
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"pooltune/internal/control"
	"pooltune/internal/controller"
	"pooltune/internal/domain"
	"pooltune/internal/policy"
	"pooltune/internal/scheduler"
	"pooltune/internal/testpg"
	"strings"
	"testing"
	"time"
)

func TestRESTLocalPostgRESTRealRLSControlAndRestartQuarantine(t *testing.T) {
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
	defer os.RemoveAll(dir)
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
	// A database ceiling must be verified independently of HTTP concurrency.
	bad := cfg
	bad.PhysicalAllocation = 3
	if r, e := NewREST(ctx, bad); e == nil {
		r.Close()
		t.Fatal("role limit exceeds physical allocation")
	}
	traffic, e := NewREST(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer traffic.Close()
	duplicate := cfg
	duplicate.Directory = filepath.Join(dir, "competing-rest")
	os.Mkdir(duplicate.Directory, 0700)
	if other, err := NewREST(ctx, duplicate); err == nil {
		other.Close()
		t.Fatal("second REST scheduler admitted using a distinct state directory")
	}

	health, _ := ListenLocal("127.0.0.1:0")
	defer health.Close()
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- traffic.Serve(runCtx, health) }()
	client, e := control.NewClient(filepath.Join(state, "control.sock"))
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	deadline := time.Now().Add(4 * time.Second)
	for {
		if _, e = client.View(ctx); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(e)
		}
		time.Sleep(time.Millisecond)
	}
	token := func(sub string) string {
		t.Helper()
		v := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{"iss": cfg.Issuer, "aud": cfg.Audience, "sub": sub, "role": "rest_user", "exp": time.Now().Add(time.Hour).Unix()})
		v.Header["kid"] = "k1"
		signed, e := v.SignedString(key)
		if e != nil {
			t.Fatal(e)
		}
		return signed
	}
	httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: db.TLSConfig.RootCAs, MinVersion: tls.VersionTLS12}}, Timeout: 3 * time.Second}
	defer httpClient.CloseIdleConnections()
	request := func(method, path, auth, body string) (int, string) {
		t.Helper()
		r, _ := http.NewRequest(method, "https://"+listen+"/rest/v1/"+path, strings.NewReader(body))
		if auth != "" {
			r.Header.Set("Authorization", "Bearer "+auth)
		}
		r.Header.Set("Content-Type", "application/json")
		res, e := httpClient.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		b, e := io.ReadAll(res.Body)
		if e != nil {
			t.Fatal(e)
		}
		return res.StatusCode, string(b)
	}
	// Root startup readiness is not database authorization evidence. Execute SQL through the frontend.
	status, body := request("GET", "items?select=id,owner", token("alice"), "")
	if status != 200 || !strings.Contains(body, "alice") || strings.Contains(body, "bob") {
		t.Fatalf("actual RLS read: %d %s", status, body)
	}
	status, _ = request("POST", "items", token("alice"), `{"id":3,"owner":"bob"}`)
	if status != 403 {
		t.Fatalf("RLS write accepted: %d", status)
	}
	status, _ = request("POST", "items", token("alice"), `{"id":3,"owner":"alice"}`)
	if status != 201 {
		t.Fatalf("allowed write failed: %d", status)
	}
	status, _ = request("GET", "items", token("alice")+"x", "")
	if status != 401 {
		t.Fatalf("forged signature accepted: %d", status)
	}
	// Exercise the same continuous observation service used by PostgreSQL.
	controlDir := filepath.Join(dir, "control")
	os.Mkdir(controlDir, 0700)
	ctrl, e := NewControl(ctx, ControlConfig{Schema: "local-control-1", Directory: controlDir, Socket: filepath.Join(state, "control.sock"), Service: control.ServiceConfig{Target: "db", Path: domain.PathREST, Guardrails: cfg.Guardrails, Registry: cfg.Registry, ResourceSource: "local-host", Window: 100 * time.Millisecond, MaxEvidenceAge: time.Second, Lease: time.Minute, Tick: 10 * time.Millisecond}})
	if e != nil {
		t.Fatal(e)
	}
	defer ctrl.Close()
	// Direct engine canary activation verifies routing and measurements independently of uncalibrated AI/evaluation.
	view, _ := client.View(ctx)
	env := view.Active
	env.Revision = "rest-canary"
	env.ExpectedPredecessor = view.Active.Revision
	env.CandidateFraction = .1
	env.LeaseExpiresAt = time.Now().Add(time.Minute)
	p2 := cfg.Stable
	p2.Revision = "p2"
	p2.ExpectedPredecessor = "p1"
	p2.ClassWeights.Normal = 2
	env.PolicyJSON, _ = json.Marshal(map[string]policy.Policy{"stable": cfg.Stable, "candidate": p2})
	env.Limits[0].Revision = env.Revision
	env.Limits[0].CohortBudgets = map[string]int{"stable": 18, "candidate": 2}
	env.Checksum = control.Checksum(env)
	if _, e = client.Activate(ctx, env); e != nil {
		t.Fatal(e)
	}
	if e = client.BeginWindow(ctx, control.WindowRequest{Revision: env.Revision, Duration: 500 * time.Millisecond}); e != nil {
		t.Fatal(e)
	}
	candidate, stable := "", ""
	for i := 0; i < 32 && (candidate == "" || stable == ""); i++ {
		sub := fmt.Sprintf("subject-%d", i)
		cl := traffic.Engine.Route(domain.RequestContext{Target: "db", Path: domain.PathREST, Method: "GET", Route: "/rest/v1/items", Principal: sub, Tenant: sub, TrustedTenant: true}, sub)
		if cl.Cohort == "candidate" {
			candidate = sub
		} else {
			stable = sub
		}
	}
	if candidate == "" || stable == "" {
		t.Fatal("cohort allocation missing")
	}
	for _, sub := range []string{candidate, stable} {
		for i := 0; i < 3; i++ {
			if status, _ = request("GET", "items", token(sub), ""); status != 200 {
				t.Fatal("cohort request failed")
			}
		}
	}
	status, _ = request("POST", "items", token(candidate), `{"id":4,"owner":"bob"}`)
	if status != 403 {
		t.Fatal("canary bypassed RLS")
	}
	var measured control.WindowSnapshot
	for {
		measured, e = client.ReadWindow(ctx)
		if e == nil && measured.Window.Complete && !measured.Resources.At.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no measured window")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var stableSuccess, candidateSuccess, candidateFailed, arrivals uint64
	for _, g := range measured.Window.Groups {
		stableSuccess += g.Stable.Successes
		candidateSuccess += g.Candidate.Successes
		candidateFailed += g.Candidate.Failed
		arrivals += g.Stable.Arrivals + g.Candidate.Arrivals
	}
	if stableSuccess != 3 || candidateSuccess != 3 || candidateFailed != 1 || arrivals != 7 {
		t.Fatalf("real REST outcomes: stable=%d candidate=%d failed=%d arrivals=%d", stableSuccess, candidateSuccess, candidateFailed, arrivals)
	}
	t.Log("Actual PostgreSQL RLS: subject-scoped reads, denied cross-owner insert, accepted own insert, invalid JWT rejection. REST window: stable 3 successes; candidate 3 successes and 1 SQL authorization failure.")
	if e = client.Rollback(ctx, env.Revision); e != nil {
		t.Fatal(e)
	}
	if status, _ = request("GET", "items", token("alice"), ""); status != 200 {
		t.Fatal("rollback interrupted REST")
	}
	if e = ctrl.Service.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	if !ctrl.Service.Ready() {
		t.Fatal("REST control not ready after rollback")
	}
	for i := 0; i < 3; i++ {
		request("GET", "items", token("alice"), "")
	}
	time.Sleep(110 * time.Millisecond)
	if e = ctrl.Service.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	read, e := ctrl.Service.Read(ctx, "state")
	if e != nil || read == nil {
		t.Fatal("real REST admin workload read unavailable")
	}
	// Full rollout must preserve anonymous fallback and stale scheduling-key
	// fallback, including while old active permits drain in either direction.
	held, e := traffic.Scheduler.Acquire(ctx, traffic.Engine.Classify(domain.RequestContext{Target: "db", Path: domain.PathREST, Principal: "alice"}, "stable"))
	if e != nil {
		t.Fatal(e)
	}
	fullView, _ := client.View(ctx)
	full := fullView.Active
	full.ExpectedPredecessor = full.Revision
	full.Revision = "rest-full"
	full.CandidateFraction = 1
	full.LeaseExpiresAt = time.Now().Add(time.Minute)
	full.PolicyJSON, _ = json.Marshal(map[string]policy.Policy{"stable": cfg.Stable, "candidate": p2})
	full.Limits[0].Revision = full.Revision
	full.Limits[0].CohortBudgets = map[string]int{"stable": 0, "candidate": 20}
	full.Checksum = control.Checksum(full)
	if _, e = client.Activate(ctx, full); e != nil {
		t.Fatal(e)
	}
	if traffic.Engine.View().Drained {
		t.Fatal("full stage ignored old stable permit")
	}
	fallback := traffic.Engine.RouteFallback(domain.RequestContext{Target: "db", Path: domain.PathREST, Principal: "alice", Tenant: "spoofed", TrustedTenant: true})
	if fallback.Class != domain.ClassNormal || fallback.Workload != "overflow" || fallback.FairnessKey != "overflow" || fallback.ConcurrencyCap != cfg.Registry.Fallback.ConcurrencyCap || fallback.Cohort != "candidate" {
		t.Fatal("fallback gained identity privileges or lost bounds")
	}
	if status, body = request("GET", "items", "", ""); status != 200 || !strings.Contains(body, "public") {
		t.Fatal("full-stage drain rejected authorized anonymous request")
	}
	traffic.Scheduler.Release(held, scheduler.Finished)
	if !traffic.Engine.View().Drained {
		t.Fatal("full observation never drained")
	}
	if status, _ = request("GET", "items", "", ""); status != 200 {
		t.Fatal("full observation rejected anonymous request")
	}
	timer := time.NewTimer(time.Until(cfg.KeysValidUntil) + 20*time.Millisecond)
	select {
	case <-timer.C:
	case <-ctx.Done():
		timer.Stop()
		t.Fatal("scheduling key expiry wait interrupted")
	}
	if status, body = request("GET", "items", token("alice"), ""); status != 200 || !strings.Contains(body, "alice") || strings.Contains(body, "bob") {
		t.Fatal("expired scheduling snapshot broke upstream authorization")
	}
	activeCandidate, e := traffic.Scheduler.Acquire(ctx, traffic.Engine.Classify(domain.RequestContext{Target: "db", Path: domain.PathREST}, "candidate"))
	if e != nil {
		t.Fatal(e)
	}
	accepted := full
	accepted.ExpectedPredecessor = full.Revision
	accepted.Revision = "rest-accepted"
	accepted.CandidateFraction = 0
	accepted.LeaseExpiresAt = time.Time{}
	accepted.PolicyJSON, _ = json.Marshal(map[string]policy.Policy{"stable": p2})
	accepted.Limits[0].Revision = accepted.Revision
	accepted.Limits[0].CohortBudgets = map[string]int{"stable": 20}
	accepted.Checksum = control.Checksum(accepted)
	if _, e = client.Activate(ctx, accepted); e != nil {
		t.Fatal(e)
	}
	if traffic.Engine.View().Drained {
		t.Fatal("accepted stage ignored old candidate permit")
	}
	for _, auth := range []string{"", token("alice")} {
		if status, _ = request("GET", "items", auth, ""); status != 200 {
			t.Fatal("acceptance drain broke fallback")
		}
	}
	traffic.Scheduler.Release(activeCandidate, scheduler.Finished)
	t.Log("Real anonymous RLS and valid upstream JWTs with expired scheduling keys survive full-stage drain, observation and acceptance; fallback retains normal-class bounds.")
	// Injected pressure is fault coverage, not evidence of measured host overload.
	pressureAt := time.Now()
	traffic.enforcePressure(pressureAt, domain.Resources{Source: "local-host", At: pressureAt, CPU: 1, Memory: .1})
	if traffic.Scheduler.Stats().Effective != 0 {
		t.Fatal("REST pressure stop not enforced")
	}
	if status, _ = request("GET", "items", token("alice"), ""); status != 503 {
		t.Fatal("stopped REST still admitted")
	}
	t.Log("Injected local-host pressure stops new REST admissions; this is a fault test, not measured pressure or calibrated benefit.")
	stop()
	select {
	case e = <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("REST shutdown hung")
	}
	traffic.Close()
	cfg.KeysValidUntil = time.Now().Add(time.Hour)
	cfg.Epoch = 2
	if restarted, e := NewREST(ctx, cfg); e == nil {
		restarted.Close()
		t.Fatal("restart admitted without independent proof")
	}
	// A separately initialized local runtime must stop admitting if its owned
	// PostgREST process dies. This does not reconcile the earlier runtime's epoch.
	cfg.KeysValidUntil = time.Now().Add(time.Hour)
	cfg.Directory = filepath.Join(dir, "traffic-fault")
	os.Mkdir(cfg.Directory, 0700)
	cfg.Epoch = 1
	fault, e := NewREST(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer fault.Close()
	faultHealth, _ := ListenLocal("127.0.0.1:0")
	defer faultHealth.Close()
	faultDone := make(chan error, 1)
	go func() { faultDone <- fault.Serve(ctx, faultHealth) }()
	if e = fault.child.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	select {
	case e = <-faultDone:
		if e == nil {
			t.Fatal("child death reported successful service")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("child death left frontend running")
	}
	if fault.Scheduler.Stats().Effective != 0 {
		t.Fatal("child death left admissions open")
	}
	t.Log("Actual PostgREST process kill shuts down the frontend and sets effective admission to zero.")
	t.Log("Local REST runtime: TLS frontend, private managed PostgREST process, verified database role ceiling 4 separate from HTTP concurrency 20, continuous-control assembly, Unix canary and rollback, measured outcomes, clean shutdown, independent restart remains quarantined. No cloud or model calls.")
}
