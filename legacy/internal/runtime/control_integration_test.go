//go:build integration

package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"pooltune/internal/control"
	"pooltune/internal/domain"
	"pooltune/internal/policy"
	"pooltune/internal/scheduler"
	"pooltune/internal/testpg"
	"strings"
	"testing"
	"time"
)

func TestContinuousControlRealPostgresAdminAndControlRestart(t *testing.T) {
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

	cfg.Registry.Workloads = []policy.Workload{{ID: "lookup", Selector: policy.Selector{Target: "db", Path: domain.PathPostgres, Principal: "fixture_app"}, DefaultClass: domain.ClassNormal, AllowedClasses: []domain.Class{domain.ClassNormal, domain.ClassCritical}, MaxQueueWait: time.Second, MaxQueueLimit: 100, MaxConcurrency: 20}}
	cfg.Stable.Workloads = []policy.WorkloadSettings{{WorkloadID: "lookup", QueueWait: time.Second, QueueLimit: 100, ConcurrencyCap: 20}}
	traffic, err := NewPostgres(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer traffic.Close()
	duplicate := cfg
	duplicate.Directory = filepath.Join(dir, "competing-pg")
	os.Mkdir(duplicate.Directory, 0700)
	if other, err := NewPostgres(ctx, duplicate); err == nil {
		other.Close()
		t.Fatal("second PG scheduler admitted using a distinct state directory")
	}

	health, err := ListenLocal("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	trafficCtx, stopTraffic := context.WithCancel(ctx)
	defer stopTraffic()
	trafficDone := make(chan error, 1)
	go func() { trafficDone <- traffic.Serve(trafficCtx, health) }()
	client, err := control.NewClient(filepath.Join(state, "control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	deadline := time.Now().Add(4 * time.Second)
	for {
		if _, err = client.View(ctx); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("traffic socket unavailable")
		}
		time.Sleep(5 * time.Millisecond)
	}
	adminReserve, err := ListenLocal("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	adminAddress := adminReserve.Addr().String()
	adminReserve.Close()
	claimFile := save("claim", []byte(strings.Repeat("x", 43)))
	root := filepath.Join(dir, "control")
	os.Mkdir(root, 0700)
	assets := filepath.Join(dir, "assets")
	os.Mkdir(assets, 0700)
	os.WriteFile(filepath.Join(assets, "index.html"), []byte("local runtime test"), 0600)
	controlConfig := ControlConfig{Schema: "local-control-1", Directory: root, Socket: filepath.Join(state, "control.sock"), Service: control.ServiceConfig{Target: "db", Path: domain.PathPostgres, Registry: cfg.Registry, Guardrails: cfg.Guardrails, Tick: 20 * time.Millisecond, Window: 100 * time.Millisecond, MaxEvidenceAge: 3 * time.Second, ResourceSource: "local-host"}, Admin: &AdminConfig{Listen: adminAddress, Origin: "http://" + adminAddress, Assets: assets, SecretRoot: private, ClaimFile: claimFile, ClaimExpires: time.Now().Add(time.Hour), SessionLifetime: time.Hour}}
	profile := filepath.Join(dir, "control.json")
	raw, _ := json.Marshal(controlConfig)
	os.WriteFile(profile, raw, 0600)
	startControl := func() (context.CancelFunc, chan error) {
		t.Helper()
		health, err := ListenLocal("127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { defer health.Close(); done <- RunControlFile(runCtx, profile, health) }()
		return cancel, done
	}
	stopControl, controlDone := startControl()
	defer func() { stopControl() }()
	jar, _ := cookiejar.New(nil)
	httpClient := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	endpoint := "http://" + adminAddress
	deadline = time.Now().Add(4 * time.Second)
	for {
		r, err := httpClient.Get(endpoint + "/api/setup/status")
		if err == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				break
			}
		}
		select {
		case err := <-controlDone:
			t.Fatalf("control startup: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("admin did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	post := func(path string, input any, csrf string) map[string]any {
		t.Helper()
		body, _ := json.Marshal(input)
		req, _ := http.NewRequestWithContext(ctx, "POST", endpoint+"/api/"+path, bytes.NewReader(body))
		req.Header.Set("Origin", endpoint)
		req.Header.Set("Content-Type", "application/json")
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		r, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatalf("%s: status %d", path, r.StatusCode)
		}
		var out map[string]any
		if json.NewDecoder(r.Body).Decode(&out) != nil {
			t.Fatal("invalid JSON")
		}
		return out
	}
	login := post("setup/claim", map[string]string{"claim_token": strings.Repeat("x", 43), "password": "fixture-private-password"}, "")
	csrf := login["csrf"].(string)
	get := func(resource string) map[string]any {
		t.Helper()
		r, err := httpClient.Get(endpoint + "/api/" + resource)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatalf("%s: status %d", resource, r.StatusCode)
		}
		var out map[string]any
		if json.NewDecoder(r.Body).Decode(&out) != nil {
			t.Fatal("invalid state")
		}
		return out
	}
	app := db.Copy()
	app.User = "fixture_app"
	app.ChannelBinding = "disable"
	host, port, _ := net.SplitHostPort(listen)
	app.Host = host
	var n int
	fmt.Sscan(port, &n)
	app.Port = uint16(n)
	conn, err := pgx.ConnectConfig(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	deadline = time.Now().Add(4 * time.Second)
	var observed map[string]any
	for {
		if _, err = conn.Exec(ctx, "SELECT 1"); err != nil {
			t.Fatal(err)
		}
		observed = get("state")
		if observed["metrics"].(map[string]any)["p95_ms"] != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("real measured admin window unavailable")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Log("Actual PostgreSQL outcomes and local-host measurements reached continuous control and authenticated admin API; no synthetic runtime backend.")
	post("freeze", map[string]string{"expected_revision": observed["revision"].(string), "idempotency_key": "owner-freeze"}, csrf)
	frozen := get("state")
	if frozen["frozen"] != true {
		t.Fatal("freeze not applied")
	}
	if _, err = conn.Exec(ctx, "SELECT 2"); err != nil {
		t.Fatal("freeze interrupted SQL")
	}
	stopControl()
	if err = <-controlDone; err != nil {
		t.Fatal(err)
	}
	stopControl, controlDone = startControl()
	deadline = time.Now().Add(4 * time.Second)
	for {
		r, err := httpClient.Get(endpoint + "/api/state")
		if err == nil {
			var value map[string]any
			json.NewDecoder(r.Body).Decode(&value)
			r.Body.Close()
			if r.StatusCode == 200 {
				if value["frozen"] != true {
					t.Fatal("restart lost freeze")
				}
				frozen = value
				break
			}
		}
		select {
		case err := <-controlDone:
			t.Fatalf("control restart: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("control restart unavailable")
		}
		time.Sleep(10 * time.Millisecond)
	}
	post("revert", map[string]string{"expected_revision": frozen["revision"].(string), "idempotency_key": "owner-restore", "revision": "initial"}, csrf)
	current, err := client.View(ctx)
	if err != nil || current.Active.Revision == "initial" {
		t.Fatal("restore did not create a new activation")
	}
	records := get("revisions")
	if len(records["items"].([]any)) < 6 {
		t.Fatal("real ledger history absent")
	}
	if _, err = conn.Exec(ctx, "SELECT 3"); err != nil {
		t.Fatal(err)
	}
	conn.Close(ctx)
	stopControl()
	if err = <-controlDone; err != nil {
		t.Fatal(err)
	}
	stopTraffic()
	if err = <-trafficDone; err != nil {
		t.Fatal(err)
	}
	t.Log("Actual admin claim/session, freeze, control restart with persisted session/freeze, restore-as-new revision, SQL continuity and clean shutdown. Traffic restart remains gated; no model calls.")
}
