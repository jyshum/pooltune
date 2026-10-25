//go:build integration && performance

package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/benchmark"
	"pooltune/internal/control"
	"pooltune/internal/controller"
	"pooltune/internal/domain"
	"pooltune/internal/hostmetrics"
	"pooltune/internal/testpool"
)

type sizingObservation struct {
	AtMS                                                                                               float64
	Connections, Idle, Connecting, CancellationSockets, Maximum, Minimum, Admission, Queued, Uncertain int
	Valid                                                                                              bool
	TickError                                                                                          bool
	Revision                                                                                           string
	Host                                                                                               *domain.Resources
}
type sizingTrial struct {
	Mode                   string
	ValidationErrors       []string
	Baseline               controller.Baseline
	Samples                []benchmark.Sample
	Observations           []sizingObservation
	Summaries              map[string]benchmark.Summary
	ReconnectMS, ElapsedMS float64
}
type sizingRequest struct {
	at    time.Duration
	phase string
}

func sizingTrace() []sizingRequest {
	var out []sizingRequest
	for _, phase := range []struct {
		name       string
		start, end time.Duration
		rate       int
	}{{"steady", 0, 3 * time.Second, 40}, {"burst", 3 * time.Second, 9 * time.Second, 320}, {"rebound", 79 * time.Second, 85 * time.Second, 500}, {"saturation", 85 * time.Second, 91 * time.Second, 600}} {
		for i := 0; ; i++ {
			at := phase.start + time.Duration(i)*time.Second/time.Duration(phase.rate)
			if at >= phase.end {
				break
			}
			out = append(out, sizingRequest{at, phase.name})
		}
	}
	return out
}
func sizingRows(ctx context.Context, c *pgx.Conn, sql string) ([]map[string]string, error) {
	rows, err := c.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]string
	for rows.Next() {
		vs, err := rows.Values()
		if err != nil {
			return nil, err
		}
		r := map[string]string{}
		for i, f := range rows.FieldDescriptions() {
			if vs[i] != nil {
				r[f.Name] = fmt.Sprint(vs[i])
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func TestPoolSizingComparison(t *testing.T) {
	output := os.Getenv("POOLTUNE_SIZING_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("absolute POOLTUNE_SIZING_OUTPUT required")
	}
	for _, b := range []string{"initdb", "pg_ctl", "pgbouncer"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Fatalf("missing %s", b)
		}
	}
	f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	versions := map[string]string{"go": goruntime.Version(), "os": goruntime.GOOS, "arch": goruntime.GOARCH, "cpus": fmt.Sprint(goruntime.NumCPU())}
	for name, args := range map[string][]string{"pgbouncer": {"--version"}, "pg_config": {"--version"}, "git": {"rev-parse", "HEAD"}, "sysctl": {"-n", "machdep.cpu.brand_string"}} {
		b, e := exec.Command(name, args...).Output()
		if e == nil {
			versions[name] = strings.TrimSpace(string(b))
		}
	}
	b, err := exec.Command("git", "status", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	versions["dirty"] = fmt.Sprint(len(b) > 0)
	report := struct {
		Profile  string
		Complete bool
		At       time.Time
		Versions map[string]string
		Trials   []sizingTrial
	}{Profile: "local-pg-sizing-v1", At: time.Now().UTC(), Versions: versions}
	for _, mode := range []string{"automatic", "fixed_warm", "fixed_native_idle", "fixed_same_path"} {
		passed := t.Run(mode, func(t *testing.T) { report.Trials = append(report.Trials, measureSizing(t, mode)) })
		if err := benchmark.WriteEvidence(f, report); err != nil {
			t.Fatal(err)
		}
		if !passed {
			t.Fatal("invalid setup; completed trials preserved in incomplete artifact")
		}
		trial := report.Trials[len(report.Trials)-1]
		if len(trial.ValidationErrors) > 0 {
			t.Fatalf("invalid trial preserved: %v", trial.ValidationErrors)
		}
	}
	report.Complete = true
	if err := benchmark.WriteEvidence(f, report); err != nil {
		t.Fatal(err)
	}
}

func measureSizing(t *testing.T, mode string) sizingTrial {
	_, adminCfg, app, db, cert := testpool.Start(t)
	ctx, cancel := context.WithTimeout(t.Context(), 115*time.Second)
	defer cancel()
	app.RuntimeParams["client_encoding"] = "UTF8"
	admin, err := pgx.ConnectConfig(ctx, adminCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	for _, q := range []string{"SET default_pool_size = 20", "SET min_pool_size = 20"} {
		if _, err = admin.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	dbConn, err := pgx.ConnectConfig(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	_, err = dbConn.Exec(ctx, "ALTER ROLE poolapp CONNECTION LIMIT 20")
	dbConn.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	keeper, err := pgx.ConnectConfig(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	defer keeper.Close(context.Background())
	sql := "SELECT current_user FROM (SELECT pg_sleep(0.05)) work"
	from := time.Now()
	var sum, squares float64
	for i := 0; i < 100; i++ {
		start := time.Now()
		var who string
		if err := keeper.QueryRow(ctx, sql).Scan(&who); err != nil || who != "poolapp" {
			t.Fatalf("calibration identity/query failed: %v", err)
		}
		v := time.Since(start).Seconds()
		sum += v
		squares += v * v
	}
	mean := sum / 100
	baseline := controller.Baseline{Version: "measured-local-sizing-v1", Approved: true, Samples: 100, MeanSeconds: mean, Variance: max(0, squares/100-mean*mean), From: from, To: time.Now(), Capacity: 20, MaxOccupancy: 2 * time.Second}
	g := controller.Guardrails{Approved: true, Version: "g1", Floor: 2, Ceiling: 20, Start: 20, WarmRatio: 0, CPU: controller.Thresholds{Grow: .95, Backoff: .98, Stop: .999}, Memory: controller.Thresholds{Grow: .95, Backoff: .98, Stop: .999}, MaxAge: 2 * time.Second, ErrorLimit: .1, Baselines: map[string]controller.Baseline{"lookup": baseline}}
	clientCfg := app.Copy()
	var traffic *Traffic
	var ctl *Control
	if mode == "fixed_same_path" || mode == "automatic" {
		_, _, cfg := recoveryFixtureFor(t, ctx, app, cert)
		save := func(name string, b []byte) string {
			p := filepath.Join(cfg.SecretRoot, name)
			if e := os.WriteFile(p, b, 0600); e != nil {
				t.Fatal(e)
			}
			return p
		}
		dsn := func(c *pgx.ConnConfig) []byte {
			u := url.URL{Scheme: "postgres", User: url.UserPassword(c.User, c.Password), Host: net.JoinHostPort(c.Host, fmt.Sprint(c.Port)), Path: "/" + c.Database}
			return []byte(u.String())
		}
		cfg.Schema = "local-pg-pool-1"
		cfg.RecoveryDirectory = ""
		cfg.StartCapacity = 20
		cfg.MaxSessions = 52
		cfg.MonitorFile = save("monitor.dsn", []byte(db.ConnString()))
		cfg.Pool = &LocalPoolConfig{AdminFile: save("pool-admin.dsn", dsn(adminCfg)), ApplicationFile: save("pool-app.dsn", dsn(app)), DatabaseUpstream: net.JoinHostPort(db.Host, fmt.Sprint(db.Port))}
		cfg.Registry.Workloads[0].Selector.Principal = "poolapp"
		cfg.Registry.Workloads[0].AllowedClasses = []domain.Class{domain.ClassNormal}
		cfg.CapacitySafety = &g
		traffic, err = NewPostgres(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		run, stop := context.WithCancel(ctx)
		health, e := ListenLocal("127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		done := make(chan error, 1)
		go func() { done <- traffic.Serve(run, health) }()
		defer func() { stop(); <-done; traffic.Close() }()
		socket := filepath.Join(cfg.Directory, "control.sock")
		for deadline := time.Now().Add(3 * time.Second); ; {
			if _, e := os.Stat(socket); e == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("control unavailable")
			}
			time.Sleep(10 * time.Millisecond)
		}
		cdir := filepath.Join(filepath.Dir(cfg.Directory), "control")
		if e := os.Mkdir(cdir, 0700); e != nil {
			t.Fatal(e)
		}
		raw, e := json.Marshal(baseline)
		if e != nil {
			t.Fatal(e)
		}
		cal := save("measured-baseline.json", raw)
		hash := sha256.Sum256(raw)
		ctl, err = NewControl(ctx, ControlConfig{Schema: "local-control-1", Directory: cdir, Socket: socket, CapacityCalibrationFile: cal, Service: control.ServiceConfig{Target: cfg.Target, Path: domain.PathPostgres, Guardrails: cfg.Guardrails, Registry: cfg.Registry, ResourceSource: "local-host", Tick: time.Second, Window: 5 * time.Second, MaxEvidenceAge: 2 * time.Second, Capacity: &control.CapacityProfile{Guardrails: g, EvidenceSHA256: hex.EncodeToString(hash[:])}}})
		if err != nil {
			t.Fatal(err)
		}
		defer ctl.Close()
		if mode == "fixed_same_path" {
			if err := ctl.Service.SetFrozen(ctx, true); err != nil {
				t.Fatal(err)
			}
		}
		host, port, _ := net.SplitHostPort(cfg.Listen)
		clientCfg.Host = host
		fmt.Sscan(port, &clientCfg.Port)
	}
	clients := make(chan *pgx.Conn, 48)
	openClients := func() {
		for i := 0; i < 48; i++ {
			c, e := pgx.ConnectConfig(ctx, clientCfg)
			if e != nil {
				t.Fatal("client setup", e)
			}
			clients <- c
		}
	}
	closeClients := func() {
		for {
			select {
			case c := <-clients:
				if c != nil {
					c.Close(context.Background())
				}
			default:
				return
			}
		}
	}
	openClients()
	defer closeClients()
	// Force20 actually open servers before changing the native minimum to2.
	var wg sync.WaitGroup
	warmErrors := make(chan error, 48)
	for i := 0; i < 48; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := <-clients
			defer func() { clients <- c }()
			var who string
			e := c.QueryRow(ctx, sql).Scan(&who)
			if e != nil || who != "poolapp" {
				warmErrors <- fmt.Errorf("warm identity/query failed: %v", e)
			}
		}()
	}
	wg.Wait()
	close(warmErrors)
	for e := range warmErrors {
		t.Fatal(e)
	}
	rows, err := sizingRows(ctx, admin, "SHOW SERVERS")
	if err != nil || len(rows) != 20 {
		t.Fatalf("not20 warmed backends: %d %v", len(rows), err)
	}
	if mode == "fixed_native_idle" {
		if _, e := admin.Exec(ctx, "SET min_pool_size = 2"); e != nil {
			t.Fatal(e)
		}
	}
	result := sizingTrial{Mode: mode, Baseline: baseline, Summaries: map[string]benchmark.Summary{}}
	trace := sizingTrace()
	result.Samples = make([]benchmark.Sample, len(trace))
	start := time.Now().Add(100 * time.Millisecond)
	obsCtx, obsStop := context.WithCancel(ctx)
	obsDone := make(chan struct{})
	go func() {
		defer close(obsDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var host hostmetrics.Sampler
		host.Sample(obsCtx)
		for {
			select {
			case <-obsCtx.Done():
				return
			case <-ticker.C:
				sample := sizingObservation{AtMS: float64(time.Since(start)) / float64(time.Millisecond), Admission: 20}
				if ctl != nil {
					sample.TickError = ctl.Service.Tick(obsCtx) != nil
				}
				probe, cancel := context.WithTimeout(obsCtx, 800*time.Millisecond)
				servers, e := sizingRows(probe, admin, "SHOW SERVERS")
				config, e2 := sizingRows(probe, admin, "SHOW CONFIG")
				sample.Valid = e == nil && e2 == nil
				if sample.Valid {
					inventory := benchmark.CountPoolInventory(servers)
					sample.Valid = sample.Valid && inventory.Valid
					sample.Connections = inventory.Connections
					sample.Idle = inventory.Idle
					sample.Connecting = inventory.Connecting
					sample.CancellationSockets = inventory.CancellationSockets
					for _, r := range config {
						switch r["key"] {
						case "default_pool_size":
							sample.Maximum, _ = strconv.Atoi(r["value"])
						case "min_pool_size":
							sample.Minimum, _ = strconv.Atoi(r["value"])
						}
					}
				}
				cancel()
				if traffic != nil {
					stats := traffic.Scheduler.Stats()
					sample.Admission = stats.Effective
					sample.Queued = stats.Queued
					sample.Uncertain = stats.Uncertain
					sample.Revision = string(traffic.Engine.View().Active.Revision)
				}
				if r, e := host.Sample(obsCtx); e == nil {
					sample.Host = &r
				}
				result.Observations = append(result.Observations, sample)
			}
		}
	}()
	defer func() { obsStop(); <-obsDone }()
	runRequest := func(i int, item sizingRequest) {
		defer wg.Done()
		scheduled := start.Add(item.at)
		time.Sleep(time.Until(scheduled))
		deadline := scheduled.Add(time.Second)
		run, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		s := benchmark.Sample{ID: i, Class: item.phase, ScheduledMS: float64(item.at) / float64(time.Millisecond), DeadlineMS: 1000, StartLagMS: max(0, float64(time.Since(scheduled))/float64(time.Millisecond))}
		defer func() { s.TotalMS = float64(time.Since(scheduled)) / float64(time.Millisecond); result.Samples[i] = s }()
		q := time.Now()
		var c *pgx.Conn
		select {
		case c = <-clients:
		case <-run.Done():
			s.Outcome = "timeout"
			s.ApplicationQueueMS = float64(time.Since(q)) / float64(time.Millisecond)
			return
		}
		s.ApplicationQueueMS = float64(time.Since(q)) / float64(time.Millisecond)
		defer func() { clients <- c }()
		if c == nil || c.IsClosed() {
			var e error
			c, e = pgx.ConnectConfig(run, clientCfg)
			if e != nil {
				s.Outcome = "failed"
				if run.Err() != nil {
					s.Outcome = "timeout"
				}
				return
			}
		}
		var who string
		err := c.QueryRow(run, sql).Scan(&who)
		switch {
		case err == nil && who == "poolapp":
			s.Outcome = "success"
		case err == nil:
			s.Outcome = "failed"
			t.Error("wrong original database identity")
		case errors.Is(err, context.DeadlineExceeded) || run.Err() != nil:
			s.Outcome = "timeout"
		default:
			s.Outcome = "failed"
		}
		if err != nil {
			c.Close(context.Background())
			c = nil
		}
	}
	// Separate phases preserve an open offered-load schedule while allowing driver
	// session replacement in the no-query interval without disguising retries.
	for i, item := range trace {
		if item.at >= 9*time.Second {
			break
		}
		wg.Add(1)
		go runRequest(i, item)
	}
	wg.Wait()
	closeClients()
	time.Sleep(time.Until(start.Add(77 * time.Second)))
	reconnect := time.Now()
	openClients()
	result.ReconnectMS = float64(time.Since(reconnect)) / float64(time.Millisecond)
	for i, item := range trace {
		if item.at < 79*time.Second {
			continue
		}
		wg.Add(1)
		go runRequest(i, item)
	}
	wg.Wait()
	time.Sleep(time.Until(start.Add(92 * time.Second)))
	obsStop()
	<-obsDone
	result.ElapsedMS = float64(time.Since(start)) / float64(time.Millisecond)
	for _, phase := range []string{"steady", "burst", "rebound", "saturation"} {
		result.Summaries[phase] = benchmark.Summarize(result.Samples, phase)
	}
	for i, s := range result.Samples {
		if s.ID != i || s.Outcome == "" {
			t.Fatal("missing offered request")
		}
	}
	valid := 0
	for _, s := range result.Observations {
		if s.Valid {
			valid++
			if s.Connections > 20 || s.Maximum > 20 {
				result.ValidationErrors = append(result.ValidationErrors, fmt.Sprintf("hard pool budget exceeded at %.1fms: connections=%d maximum=%d connecting=%d cancellation_sockets=%d", s.AtMS, s.Connections, s.Maximum, s.Connecting, s.CancellationSockets))
			}
		}
	}
	if valid < 85 {
		result.ValidationErrors = append(result.ValidationErrors, fmt.Sprintf("insufficient observation coverage: %d", valid))
	}
	t.Logf("mode %s: phase summaries %+v; observations %d", mode, result.Summaries, len(result.Observations))
	return result
}

func TestSizingInventoryDuringRealCancellation(t *testing.T) {
	ctx, adminCfg, app, db, _ := testpool.Start(t)
	app.RuntimeParams["client_encoding"] = "UTF8"
	admin, err := pgx.ConnectConfig(ctx, adminCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	for _, q := range []string{"SET default_pool_size = 20", "SET min_pool_size = 20"} {
		if _, e := admin.Exec(ctx, q); e != nil {
			t.Fatal(e)
		}
	}
	monitor, err := pgx.ConnectConfig(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer monitor.Close(ctx)
	if _, e := monitor.Exec(ctx, "ALTER ROLE poolapp CONNECTION LIMIT 20"); e != nil {
		t.Fatal(e)
	}
	keeper, e := pgx.ConnectConfig(ctx, app)
	if e != nil {
		t.Fatal(e)
	}
	defer keeper.Close(ctx)
	if _, e := keeper.Exec(ctx, "SELECT 1"); e != nil {
		t.Fatal(e)
	}
	var warm []*pgx.Conn
	for i := 0; i < 20; i++ {
		c, e := pgx.ConnectConfig(ctx, app)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = c.Exec(ctx, "BEGIN; SELECT 1"); e != nil {
			t.Fatal(e)
		}
		warm = append(warm, c)
	}
	for _, c := range warm {
		if _, e := c.Exec(ctx, "COMMIT"); e != nil {
			t.Fatal(e)
		}
		c.Close(ctx)
	}
	rows, e := sizingRows(ctx, admin, "SHOW SERVERS")
	if e != nil || benchmark.CountPoolInventory(rows).Connections != 20 {
		t.Fatal("cancellation fixture not warm", len(rows), e)
	}
	run, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for run.Err() == nil {
				c, e := pgx.ConnectConfig(run, app)
				if e != nil {
					continue
				}
				q, cancel := context.WithTimeout(run, 15*time.Millisecond)
				c.Exec(q, "SELECT pg_sleep(0.05)")
				cancel()
				c.Close(context.Background())
			}
		}()
	}
	sawCancel := false
	maxRows, maxBackends, maxPG := 0, 0, 0
	for run.Err() == nil {
		rows, e := sizingRows(ctx, admin, "SHOW SERVERS")
		if e != nil {
			t.Fatal(e)
		}
		inv := benchmark.CountPoolInventory(rows)
		if inv.CancellationSockets > 0 {
			sawCancel = true
		}
		maxRows = max(maxRows, len(rows))
		maxBackends = max(maxBackends, inv.Connections)
		var actual int
		if e := monitor.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE usename='poolapp'").Scan(&actual); e != nil {
			t.Fatal(e)
		}
		maxPG = max(maxPG, actual)
		if !inv.Valid {
			t.Fatal("unrecognized inventory", rows)
		}
		time.Sleep(5 * time.Millisecond)
	}
	wg.Wait()
	if !sawCancel || maxRows <= 20 {
		t.Fatal("did not exercise cancellation inventory")
	}
	if maxBackends > 20 || maxPG > 20 {
		t.Fatalf("capacity exceeded: pool=%d postgres=%d rows=%d", maxBackends, maxPG, maxRows)
	}
	t.Logf("actual cancellation: raw SHOW SERVERS peak%d, established backend peak%d, pg_stat_activity peak%d", maxRows, maxBackends, maxPG)
}
