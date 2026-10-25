//go:build integration

package runtime

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"pooltune/internal/control"
	"pooltune/internal/controller"
	"pooltune/internal/domain"
	"pooltune/internal/hostmetrics"
	"strings"
	"sync"
	"testing"
	"time"
)

func serveRESTTest(t *testing.T, ctx context.Context, cfg RESTConfig) (*RESTTraffic, func()) {
	t.Helper()
	traffic, err := NewREST(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	health, _ := ListenLocal("127.0.0.1:0")
	run, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- traffic.Serve(run, health) }()
	client, _ := control.NewClient(filepath.Join(cfg.Directory, "control.sock"))
	defer client.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err = client.View(ctx); err == nil {
			break
		}
		if time.Now().After(deadline) {
			stop()
			traffic.Close()
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return traffic, func() { stop(); <-done; traffic.Close() }
}
func TestRESTGrowthWithMeasuredHTTPBaseline(t *testing.T) {
	ctx, db, cfg := restRecoveryFixture(t)
	ctx, cancelTest := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancelTest()
	admin, err := pgx.ConnectConfig(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	_, err = admin.Exec(ctx, `ALTER ROLE rest_login CONNECTION LIMIT 32; CREATE FUNCTION api.slow_items() RETURNS SETOF api.items LANGUAGE plpgsql SECURITY INVOKER AS $$ BEGIN PERFORM pg_sleep(0.05); RETURN QUERY SELECT * FROM api.items; END $$; GRANT EXECUTE ON FUNCTION api.slow_items() TO rest_user;`)
	admin.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Capacity = 32
	cfg.PhysicalAllocation = 32
	cfg.Scheduler.MaxActive = 32
	cfg.Scheduler.MaxTenantActive = 32
	cfg.Registry.Workloads = cfg.Registry.Workloads[:1]
	cfg.Registry.Workloads[0].MaxConcurrency = 32
	cfg.Stable.Workloads = cfg.Stable.Workloads[:1]
	cfg.Stable.Workloads[0].ConcurrencyCap = 32
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	b64 := base64.RawURLEncoding.EncodeToString
	jwks := fmt.Sprintf(`{"keys":[{"kty":"EC","crv":"P-256","alg":"ES256","use":"sig","kid":"growth","x":"%s","y":"%s"}]}`, b64(key.X.FillBytes(make([]byte, 32))), b64(key.Y.FillBytes(make([]byte, 32))))
	if err = os.WriteFile(cfg.JWKSFile, []byte(jwks), 0600); err != nil {
		t.Fatal(err)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{"iss": cfg.Issuer, "aud": cfg.Audience, "sub": "alice", "role": "rest_user", "exp": time.Now().Add(time.Hour).Unix()})
	token.Header["kid"] = "growth"
	signed, _ := token.SignedString(key)
	httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: db.TLSConfig.RootCAs, MinVersion: tls.VersionTLS12}}, Timeout: 2 * time.Second}
	defer httpClient.CloseIdleConnections()
	request := func(ctx context.Context) error {
		r, _ := http.NewRequestWithContext(ctx, "GET", "https://"+cfg.Listen+"/rest/v1/rpc/slow_items", nil)
		r.Header.Set("Authorization", "Bearer "+signed)
		response, err := httpClient.Do(r)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			return err
		}
		if response.StatusCode != 200 || !strings.Contains(string(body), "alice") || strings.Contains(string(body), "bob") {
			return fmt.Errorf("RLS request status %d", response.StatusCode)
		}
		return nil
	}
	calibration := cfg
	calibration.CapacitySafety = nil
	calibration.RecoveryDirectory = ""
	calibration.Directory = filepath.Join(filepath.Dir(cfg.Directory), "calibration")
	os.Mkdir(calibration.Directory, 0700)
	cal, stopCalibration := serveRESTTest(t, ctx, calibration)
	from := time.Now()
	var sum, squares float64
	// Same TLS frontend and scheduler path, without queueing. Timing includes
	// client transport; this is local correctness calibration, not G4 approval.
	for i := 0; i < 100; i++ {
		start := time.Now()
		if err = request(ctx); err != nil {
			stopCalibration()
			t.Fatal(err)
		}
		d := time.Since(start).Seconds()
		sum += d
		squares += d * d
	}
	_ = cal
	stopCalibration()
	httpClient.CloseIdleConnections()
	mean := sum / 100
	g := controller.Guardrails{Approved: true, Version: "g1", Floor: 1, Start: 25, Ceiling: 32, HTTPEnvelope: 32, CPU: controller.Thresholds{Grow: .998, Backoff: .999, Stop: .9999}, Memory: controller.Thresholds{Grow: .998, Backoff: .999, Stop: .9999}, MaxAge: 2 * time.Second, ErrorLimit: .1, Baselines: map[string]controller.Baseline{"alice": {Version: "local-measured-http", Approved: true, Samples: 100, MeanSeconds: mean, Variance: max(0, squares/100-mean*mean), From: from, To: time.Now(), Capacity: 32, MaxOccupancy: time.Second}}}
	cfg.CapacitySafety = &g
	traffic, stop := serveRESTTest(t, ctx, cfg)
	defer stop()
	client, _ := control.NewClient(filepath.Join(cfg.Directory, "control.sock"))
	defer client.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		m, _ := client.CapacityMetrics(ctx)
		if m.Ready.Valid {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("REST has no verified capacity telemetry")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if traffic.Scheduler.Stats().Effective != 25 {
		t.Fatal("REST ignored approved start")
	}
	dir := filepath.Join(filepath.Dir(cfg.Directory), "growth-control")
	os.Mkdir(dir, 0700)
	evidence, _ := json.Marshal(g.Baselines)
	file := filepath.Join(dir, "calibration.json")
	os.WriteFile(file, evidence, 0600)
	hash := sha256.Sum256(evidence)
	cc := ControlConfig{Schema: "local-control-1", Directory: dir, Socket: filepath.Join(cfg.Directory, "control.sock"), CapacityCalibrationFile: file, Service: control.ServiceConfig{Target: cfg.Target, Path: domain.PathREST, ResourceSource: "local-host", Guardrails: cfg.Guardrails, Registry: cfg.Registry, Tick: 100 * time.Millisecond, Window: 5 * time.Second, MaxEvidenceAge: 3 * time.Second, Capacity: &control.CapacityProfile{EvidenceSHA256: hex.EncodeToString(hash[:]), Guardrails: g}}}
	owner, err := NewControl(ctx, cc)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err = traffic.Engine.Constrain(20); err != nil {
		t.Fatal(err)
	}
	if err = owner.Service.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = owner.Service.Execute(ctx, "safety-resume", resumeCommand(t, ctx, owner, "early-ceiling-release")); err == nil {
		t.Fatal("reduced ceiling released without healthy interval")
	}
	awaitResume(t, ctx, owner)
	if _, err = owner.Service.Execute(ctx, "safety-resume", resumeCommand(t, ctx, owner, "recover-before-growth")); err != nil {
		t.Fatal(err)
	}
	if v := traffic.Engine.View(); v.SafetyCeiling != nil || v.Active.Limits[0].Effective != 20 {
		t.Fatal("ceiling recovery increased admission")
	}
	load, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	failures := make(chan error, 28)
	for i := 0; i < 28; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for load.Err() == nil {
				if err := request(load); err != nil {
					if load.Err() == nil {
						failures <- err
					}
					return
				}
			}
		}()
	}
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-failures:
			t.Fatal(err)
		default:
		}
		if err = owner.Service.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		v := traffic.Engine.View()
		if v.Active.Limits[0].Effective > 20 {
			if v.Active.Limits[0].Effective != 25 || !strings.HasPrefix(string(v.Active.Revision), "capacity-") {
				t.Fatal("unbounded REST growth")
			}
			cancel()
			workers.Wait()
			m, _ := client.CapacityMetrics(ctx)
			if m.Physical.RESTEnvelope != 32 || !m.Physical.RESTVerified {
				t.Fatal("physical allocation changed or unverified")
			}
			t.Logf("100 real unqueued TLS HTTP samples mean %.6fs; 28 concurrent RLS requests; nonzero ceiling recovered at unchanged admission 20; subsequent HTTP growth 20 -> 25; physical allocation fixed at 32", mean)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	m, _ := client.CapacityMetrics(ctx)
	t.Fatalf("REST growth absent: capacity=%d complete=%v omitted=%d ready=%.0f occupancy=%.2f groups=%+v", traffic.Scheduler.Stats().Effective, m.Local.Complete, m.Local.Omitted, m.Ready.Value, m.Local.MeanInFlight, m.Local.Groups)
}

func TestRESTCapacityEvidenceRejectsExternalWorkAndChildFailure(t *testing.T) {
	ctx, db, cfg := restRecoveryFixture(t)
	traffic, err := NewREST(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer traffic.Close()
	sampler := &hostmetrics.Sampler{}
	sampler.Sample(ctx)
	time.Sleep(120 * time.Millisecond)
	resources, err := sampler.Sample(ctx)
	if err != nil {
		t.Fatal(err)
	}
	traffic.recordHostSample(ctx, resources, nil)
	if m := traffic.capacityMetrics.Load(); m == nil || !m.Ready.Valid || !m.Physical.RESTVerified {
		t.Fatal("live owned capacity unavailable")
	}
	other, err := pgx.ConnectConfig(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = other.Exec(ctx, "BEGIN; SELECT 1"); err != nil {
		t.Fatal(err)
	}
	traffic.sampleCapacity(ctx, resources)
	if traffic.capacityMetrics.Load().Ready.Valid {
		t.Fatal("external old work permitted growth")
	}
	other.Close(ctx)
	traffic.sampleCapacity(ctx, resources)
	if !traffic.capacityMetrics.Load().Ready.Valid {
		t.Fatal("resolved external work retained stale failure")
	}
	traffic.child.Process.Kill()
	done := <-traffic.childDone
	traffic.childDone <- done
	traffic.sampleCapacity(ctx, resources)
	if traffic.capacityMetrics.Load().Ready.Valid {
		t.Fatal("dead child retained growth readiness")
	}
}
