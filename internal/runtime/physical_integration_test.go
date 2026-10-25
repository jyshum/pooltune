//go:build integration

package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"pooltune/internal/control"
	"pooltune/internal/controller"
	"pooltune/internal/domain"
	"pooltune/internal/testpool"
	"testing"
	"time"
)

func TestPhysicalPoolRuntimeGrowthAndQuietReclamation(t *testing.T) {
	ctx, admin, app, monitor, cert := testpool.Start(t)
	_, _, cfg := recoveryFixtureFor(t, ctx, app, cert)
	// A separately approved initial pool target, not a measured readiness claim.
	a, err := pgx.ConnectConfig(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{"SET default_pool_size = 10", "SET min_pool_size = 10"} {
		if _, err = a.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	a.Close(ctx)
	save := func(name, raw string) string {
		file := filepath.Join(cfg.SecretRoot, name)
		if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		return file
	}
	cfg.Schema = "local-pg-pool-1"
	cfg.RecoveryDirectory = ""
	cfg.StartCapacity = 10
	cfg.MaxSessions = 32
	cfg.MonitorFile = save("monitor.dsn", monitor.ConnString())
	dsn := func(c *pgx.ConnConfig) string {
		u := url.URL{Scheme: "postgres", User: url.UserPassword(c.User, c.Password), Host: net.JoinHostPort(c.Host, fmt.Sprint(c.Port)), Path: "/" + c.Database, RawQuery: "sslmode=require"}
		return u.String()
	}
	cfg.Pool = &LocalPoolConfig{AdminFile: save("pool-admin.dsn", dsn(admin)), ApplicationFile: save("pool-app.dsn", dsn(app)), DatabaseUpstream: net.JoinHostPort(monitor.Host, fmt.Sprint(monitor.Port))}
	cfg.Registry.Workloads[0].Selector.Principal = "poolapp"
	now := time.Now()
	g := controller.Guardrails{Approved: true, Version: "g1", Floor: 2, Ceiling: 20, Start: 10, WarmRatio: .5, CPU: controller.Thresholds{Grow: .98, Backoff: .99, Stop: .999}, Memory: controller.Thresholds{Grow: .98, Backoff: .99, Stop: .999}, MaxAge: 2 * time.Second, ErrorLimit: .1, Baselines: map[string]controller.Baseline{"lookup": {Version: "correctness-only", Approved: true, Samples: 100, MeanSeconds: .01, From: now.Add(-time.Hour), To: now.Add(-time.Minute), Capacity: 10, MaxOccupancy: 90 * time.Second}}}
	cfg.CapacitySafety = &g
	traffic, err := NewPostgres(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, cancel := context.WithCancel(ctx)
	health, err := ListenLocal("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- traffic.Serve(serveCtx, health) }()
	defer func() { cancel(); <-done; traffic.Close() }()
	socket := filepath.Join(cfg.Directory, "control.sock")
	for deadline := time.Now().Add(3 * time.Second); ; {
		if _, err = os.Stat(socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("control socket unavailable")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cdir := filepath.Join(filepath.Dir(cfg.Directory), "control")
	os.Mkdir(cdir, 0700)
	calibration := save("capacity-evidence.txt", "Component correctness profile only. No production calibration claim.")
	hash := sha256.Sum256([]byte("Component correctness profile only. No production calibration claim."))
	cc := ControlConfig{Schema: "local-control-1", Directory: cdir, Socket: socket, CapacityCalibrationFile: calibration, Service: control.ServiceConfig{Target: cfg.Target, Path: domain.PathPostgres, Guardrails: cfg.Guardrails, Registry: cfg.Registry, ResourceSource: "local-host", Tick: time.Second, Window: 5 * time.Second, MaxEvidenceAge: 2 * time.Second, Capacity: &control.CapacityProfile{Guardrails: g, EvidenceSHA256: hex.EncodeToString(hash[:])}}}
	ctl, err := NewControl(ctx, cc)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err = ctl.Service.SetFrozen(ctx, true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if err = ctl.Service.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
	}
	if obs, err := traffic.pool.Observe(ctx); err != nil || obs.Target != 10 {
		t.Fatalf("frozen pool grew: %+v %v", obs, err)
	}
	if err = ctl.Service.SetFrozen(ctx, false); err != nil {
		t.Fatal(err)
	}
	var held pgx.Tx
	clientCfg := app.Copy()
	clientCfg.RuntimeParams["client_encoding"] = "UTF8"
	host, port, _ := net.SplitHostPort(cfg.Listen)
	clientCfg.Host = host
	fmt.Sscan(port, &clientCfg.Port)
	client, err := pgx.ConnectConfig(ctx, clientCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	held, err = client.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var user string
	if err = held.QueryRow(ctx, "SELECT current_user").Scan(&user); err != nil || user != "poolapp" {
		t.Fatal("identity not preserved", err)
	}
	defer held.Rollback(context.Background())
	deadline := time.Now().Add(95 * time.Second)
	grown, cut := false, false
	for time.Now().Before(deadline) {
		if err = ctl.Service.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		obs, e := traffic.pool.Observe(ctx)
		v := traffic.Engine.View()
		if e == nil && obs.Target == 15 && obs.Working == 15 && !obs.Pending {
			grown = true
		}
		if grown && v.Active.Limits[0].Effective == 9 {
			if obs.Target != 15 {
				t.Fatal("reclaimed pool with an active transaction")
			}
			if _, err = held.Exec(ctx, "SELECT 1"); err != nil {
				t.Fatal("quiet admission interrupted transaction", err)
			}
			if err = held.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			cut = true
			break
		}
		time.Sleep(time.Second)
	}
	if !grown || !cut {
		t.Fatalf("missing real physical growth/quiet admission: growth=%v cut=%v", grown, cut)
	}
	for time.Now().Before(deadline) {
		if err = ctl.Service.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		obs, e := traffic.pool.Observe(ctx)
		if e == nil && obs.Target == 14 && obs.Working == 14 && !obs.Pending {
			var one int
			if err = client.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
				t.Fatal("SQL after reclaim", err)
			}
			t.Log("real TLS/SCRAM poolapp transaction preserved; freeze held target 10; physical capacity 10 -> 15; 30-second quiet admission 10 -> 9; after idle drain physical capacity 15 -> 14; SQL still succeeds")
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal("quiet physical reclamation did not complete")
}
