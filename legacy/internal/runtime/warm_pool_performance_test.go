//go:build integration && performance

package runtime

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"pooltune/internal/benchmark"
	"pooltune/internal/domain"
	"pooltune/internal/hostmetrics"
	"pooltune/internal/policy"
	"pooltune/internal/scheduler"
)

type warmTrial struct {
	Mode, Scenario          string
	Repeat, WarmConnections int
	DurationMS              float64
	Host                    *domain.Resources
	Critical, Background    benchmark.Summary
	Samples                 []benchmark.Sample
}
type offeredRequest struct {
	class string
	at    time.Duration
}

func warmTrace(scenario string) []offeredRequest {
	var out []offeredRequest
	switch scenario {
	case "steady":
		for i := 0; i < 60; i++ {
			c := "background"
			if i%2 == 0 {
				c = "critical"
			}
			out = append(out, offeredRequest{c, time.Duration(i) * 15 * time.Millisecond})
		}
	case "burst":
		for i := 0; i < 40; i++ {
			out = append(out, offeredRequest{"background", 0})
		}
		for i := 0; i < 8; i++ {
			out = append(out, offeredRequest{"critical", time.Duration(20+i*2) * time.Millisecond})
		}
	case "sustained":
		for i := 0; i < 160; i++ {
			c := "background"
			if i%4 == 0 {
				c = "critical"
			}
			out = append(out, offeredRequest{c, time.Duration(i) * 4 * time.Millisecond})
		}
	}
	return out
}

func TestWarmPoolComparison(t *testing.T) {
	path := os.Getenv("POOLTUNE_BENCH_OUTPUT")
	if !filepath.IsAbs(path) {
		t.Fatal("explicit absolute POOLTUNE_BENCH_OUTPUT required")
	}
	for _, name := range []string{"initdb", "pg_ctl", "postgrest"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatalf("missing dependency %s", name)
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	versions := map[string]string{"go": goruntime.Version(), "os": goruntime.GOOS, "arch": goruntime.GOARCH, "cpus": fmt.Sprint(goruntime.NumCPU())}
	for name, args := range map[string][]string{"postgrest": {"--version"}, "pg_config": {"--version"}, "git": {"rev-parse", "HEAD"}, "uname": {"-sm"}, "sysctl": {"-n", "machdep.cpu.brand_string"}} {
		b, e := exec.Command(name, args...).Output()
		if e == nil {
			versions[name] = strings.TrimSpace(string(b))
		}
	}
	b, e := exec.Command("git", "status", "--porcelain").Output()
	if e != nil {
		t.Fatal(e)
	}
	versions["dirty"] = fmt.Sprint(len(b) > 0)
	var trials []warmTrial
	modes := []string{"warm_pool", "application_priority", "pooltune_equal", "pooltune_priority"}
	for repeat := 0; repeat < 3; repeat++ {
		for _, scenario := range []string{"steady", "burst", "sustained"} {
			for j := 0; j < 4; j++ {
				mode := modes[(j+repeat)%4]
				if !t.Run(fmt.Sprintf("%d/%s/%s", repeat, scenario, mode), func(t *testing.T) { trials = append(trials, measureWarmTrial(t, mode, scenario, repeat)) }) {
					t.Fatal("invalid trial; partial output must not be used")
				}
			}
		}
	}
	report := struct {
		Profile  string
		At       time.Time
		Versions map[string]string
		Trials   []warmTrial
	}{"local-rest-warm-v1", time.Now().UTC(), versions, trials}
	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
}

func measureWarmTrial(t *testing.T, mode, scenario string, repeat int) warmTrial {
	_, db, cfg := restRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	_, err = admin.Exec(ctx, `CREATE FUNCTION api.bench_light() RETURNS SETOF api.items LANGUAGE plpgsql SECURITY INVOKER AS $$ BEGIN PERFORM pg_sleep(0.003); RETURN QUERY SELECT * FROM api.items; END $$;
 CREATE FUNCTION api.bench_work() RETURNS SETOF api.items LANGUAGE plpgsql SECURITY INVOKER AS $$ BEGIN PERFORM pg_sleep(0.04); RETURN QUERY SELECT * FROM api.items; END $$;
 GRANT EXECUTE ON FUNCTION api.bench_light(),api.bench_work() TO rest_user;`)
	admin.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.CapacitySafety = nil
	cfg.RecoveryDirectory = ""
	cfg.Capacity = 4
	cfg.MaxRequests = 200
	cfg.Scheduler.MaxActive = 4
	cfg.Scheduler.MaxTenantActive = 4
	cfg.Scheduler.MaxQueued = 200
	cfg.Scheduler.MaxTenantQueued = 200
	cfg.Registry.Workloads = nil
	cfg.Stable.Workloads = nil
	priority := mode == "application_priority" || mode == "pooltune_priority"
	if priority {
		cfg.Stable.ReserveFraction = .25
		cfg.Stable.ClassWeights = policy.ClassWeights{Critical: 4, Normal: 1, Background: 1}
	}
	for _, sub := range []string{"alice", "bob"} {
		class := domain.ClassNormal
		if priority {
			class = domain.ClassBackground
			if sub == "alice" {
				class = domain.ClassCritical
			}
		}
		cfg.Registry.Workloads = append(cfg.Registry.Workloads, policy.Workload{ID: sub, Selector: policy.Selector{Target: cfg.Target, Path: domain.PathREST, Principal: sub}, IdentityDefault: true, DefaultClass: class, AllowedClasses: []domain.Class{class}, LockedClass: class, LockedRuleID: "owner-" + sub, MaxQueueWait: time.Second, MaxQueueLimit: 200, MaxConcurrency: 4})
		cfg.Stable.Workloads = append(cfg.Stable.Workloads, policy.WorkloadSettings{WorkloadID: sub, QueueWait: time.Second, QueueLimit: 200, ConcurrencyCap: 4})
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.RawURLEncoding.EncodeToString
	jwks := fmt.Sprintf(`{"keys":[{"kty":"EC","crv":"P-256","alg":"ES256","use":"sig","kid":"bench","x":"%s","y":"%s"}]}`, b64(key.X.FillBytes(make([]byte, 32))), b64(key.Y.FillBytes(make([]byte, 32))))
	if err = os.WriteFile(cfg.JWKSFile, []byte(jwks), 0600); err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, sub := range []string{"alice", "bob"} {
		tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{"iss": cfg.Issuer, "aud": cfg.Audience, "sub": sub, "role": "rest_user", "exp": time.Now().Add(time.Hour).Unix()})
		tok.Header["kid"] = "bench"
		tokens[sub], err = tok.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
	}
	traffic, stop := serveRESTTest(t, ctx, cfg)
	defer stop()
	// Baseline terminates the same TLS and preserves original authorization to the
	// same real child pool. It has no extra application admission middleware.
	backend, _ := url.Parse("http://postgrest")
	proxy := httputil.NewSingleHostReverseProxy(backend)
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(cfg.Directory, "postgrest.sock"))
	}, MaxIdleConns: 200, MaxIdleConnsPerHost: 200, MaxConnsPerHost: 200}
	defer transport.CloseIdleConnections()
	proxy.Transport = transport
	oldDirector := proxy.Director
	proxy.Director = func(r *http.Request) { oldDirector(r); r.URL.Path = strings.TrimPrefix(r.URL.Path, "/rest/v1") }
	// A client timeout is a recorded result, not noisy server stderr with request URLs.
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, e error) { w.WriteHeader(http.StatusBadGateway) }
	cert, err := tls.LoadX509KeyPair(cfg.CertificateFile, cfg.PrivateKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: proxy, ReadHeaderTimeout: time.Second}
	defer server.Close()
	go server.Serve(tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}))
	directURL := "https://" + ln.Addr().String()
	targetURL := "https://" + cfg.Listen
	if mode == "warm_pool" || mode == "application_priority" {
		targetURL = directURL
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: db.TLSConfig.RootCAs, MinVersion: tls.VersionTLS12}, MaxIdleConns: 200, MaxIdleConnsPerHost: 200, MaxConnsPerHost: 200}}
	defer client.CloseIdleConnections()
	request := func(ctx context.Context, base, sub, function string) (int, error) {
		req, _ := http.NewRequestWithContext(ctx, "GET", base+"/rest/v1/rpc/"+function, nil)
		req.Header.Set("Authorization", "Bearer "+tokens[sub])
		res, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		defer res.Body.Close()
		body, err := io.ReadAll(io.LimitReader(res.Body, 4096))
		if err != nil {
			return res.StatusCode, err
		}
		if res.StatusCode == 200 {
			var rows []struct {
				ID    int    `json:"id"`
				Owner string `json:"owner"`
			}
			if json.Unmarshal(body, &rows) != nil || len(rows) != 1 || rows[0].Owner != sub {
				return 200, fmt.Errorf("authorization mismatch")
			}
		}
		return res.StatusCode, nil
	}
	// Warm four actual DB connections independently of a priority reserve.
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, e := request(ctx, directURL, "alice", "bench_work")
			if e != nil || code != 200 {
				errs <- fmt.Errorf("warm request status=%d error=%v", code, e)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	inv, err := inspectRESTDatabase(ctx, traffic.monitor, traffic.appRole, 4, traffic.applicationName, true)
	if err != nil || inv.Owned != 4 || inv.Idle != 4 {
		t.Fatalf("pool not warm: %+v %v", inv, err)
	}
	// Also warm the selected TLS/frontend path before the scheduled trace.
	if code, e := request(ctx, targetURL, "alice", "bench_light"); e != nil || code != 200 {
		t.Fatalf("frontend warm status=%d error=%v", code, e)
	}
	var appScheduler *scheduler.Scheduler
	compiled, err := policy.Compile(cfg.Stable, cfg.Guardrails, cfg.Registry)
	if err != nil {
		t.Fatal(err)
	}
	if mode == "application_priority" {
		appScheduler, err = scheduler.New(cfg.Scheduler, scheduler.Snapshot{Limits: domain.LimitSnapshot{Target: cfg.Target, Path: domain.PathREST, Revision: "initial", Effective: 4, CriticalReserve: 1, CohortBudgets: map[string]int{"stable": 4}, GuardrailVersion: "g1", Epoch: 1}, Policies: map[string]*policy.CompiledPolicy{"stable": compiled}})
		if err != nil {
			t.Fatal(err)
		}
	}
	trace := warmTrace(scenario)
	samples := make([]benchmark.Sample, len(trace))
	var host hostmetrics.Sampler
	host.Sample(ctx)
	start := time.Now().Add(30 * time.Millisecond)
	for i, item := range trace {
		wg.Add(1)
		go func(i int, item offeredRequest) {
			defer wg.Done()
			scheduled := start.Add(item.at)
			time.Sleep(time.Until(scheduled))
			duration := 600 * time.Millisecond
			sub := "bob"
			if item.class == "critical" {
				duration = 200 * time.Millisecond
				sub = "alice"
			}
			deadline := scheduled.Add(duration)
			run, cancel := context.WithDeadline(ctx, deadline)
			defer cancel()
			sample := benchmark.Sample{ID: i, Class: item.class, ScheduledMS: float64(item.at) / float64(time.Millisecond), DeadlineMS: float64(duration) / float64(time.Millisecond), StartLagMS: max(0, float64(time.Since(scheduled))/float64(time.Millisecond))}
			defer func() {
				sample.TotalMS = float64(time.Since(scheduled)) / float64(time.Millisecond)
				samples[i] = sample
			}()
			var permit *scheduler.Permit
			if appScheduler != nil {
				q := time.Now()
				c := policy.Classify(compiled, domain.RequestContext{Target: cfg.Target, Path: domain.PathREST, Principal: sub, ClientDeadline: deadline})
				c.Cohort = "stable"
				c.ActivationRevision = "initial"
				var e error
				permit, e = appScheduler.Acquire(run, c)
				sample.ApplicationQueueMS = float64(time.Since(q)) / float64(time.Millisecond)
				if e != nil {
					sample.Outcome = benchmark.QueueOutcome(e, run.Err())
					return
				}
			}
			function := "bench_work"
			if scenario == "steady" {
				function = "bench_light"
			}
			code, e := request(run, targetURL, sub, function)
			sample.Status = code
			outcome := scheduler.Finished
			switch {
			case e != nil:
				sample.Outcome = "failed"
				outcome = scheduler.Unknown
				if run.Err() != nil {
					sample.Outcome = "timeout"
				}
				if code == 200 {
					t.Error("response identity validation failed")
				}
			case code == 200:
				sample.Outcome = "success"
			case code == 429 || code == 503 || code == 504:
				sample.Outcome = "rejected"
				outcome = scheduler.Overloaded
			default:
				sample.Outcome = "failed"
				outcome = scheduler.Failed
			}
			if permit != nil {
				appScheduler.Release(permit, outcome)
			}
		}(i, item)
	}
	wg.Wait()
	result := warmTrial{Mode: mode, Scenario: scenario, Repeat: repeat, WarmConnections: inv.Owned, DurationMS: float64(time.Since(start)) / float64(time.Millisecond), Samples: samples, Critical: benchmark.Summarize(samples, "critical"), Background: benchmark.Summarize(samples, "background")}
	if h, e := host.Sample(ctx); e == nil {
		result.Host = &h
	}
	if result.Critical.Offered+result.Background.Offered != len(trace) {
		t.Fatal("incomplete trace")
	}
	for i, s := range samples {
		if s.ID != i || s.Outcome == "" {
			t.Fatal("missing observation")
		}
	}
	t.Logf("critical %d/%d p95 %.1fms; background %d/%d p95 %.1fms", result.Critical.Success, result.Critical.Offered, result.Critical.SuccessP95MS, result.Background.Success, result.Background.Offered, result.Background.SuccessP95MS)
	return result
}
