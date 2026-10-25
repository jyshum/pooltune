//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"pooltune/internal/admin"
	"pooltune/internal/testpool"
	"pooltune/internal/tuning"
	"testing"
	"time"
)

func TestTunerProcessHelper(t *testing.T) {
	if os.Getenv("POOLTUNE_TUNER_HELPER") != "1" {
		return
	}
	if run(context.Background(), os.Getenv("POOLTUNE_TUNER_PROFILE")) != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

// A genuine independent tuner process dies while a direct pooled transaction
// remains open. Success requires the same transaction and new connections work.
func TestBackgroundTunerCrashLeavesDirectRoute(t *testing.T) {
	ctx, adminCfg, appCfg, monitor, cert := testpool.Start(t)
	appCfg.RuntimeParams["client_encoding"] = "UTF8"
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	root := filepath.Join(dir, "ca.pem")
	os.WriteFile(root, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0600)
	for name, c := range map[string]*pgx.ConnConfig{"admin": adminCfg, "app": appCfg, "monitor": monitor} {
		dsn := fmt.Sprintf("host=127.0.0.1 port=%d user=%s password=%s dbname=%s sslmode=verify-full sslrootcert=%s", c.Port, c.User, c.Password, c.Database, root)
		if e := os.WriteFile(filepath.Join(dir, name), []byte(dsn), 0600); e != nil {
			t.Fatal(e)
		}
	}
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	listen := l.Addr().String()
	l.Close()
	cfg := tuning.Config{Schema: "local-tuner-1", Target: "db", Directory: dir, SecretRoot: dir, AdminDSNFile: filepath.Join(dir, "admin"), ApplicationDSNFile: filepath.Join(dir, "app"), MonitorDSNFile: filepath.Join(dir, "monitor"), Listen: listen, Assets: t.TempDir(), ExclusiveLocalPool: true, Bounds: tuning.Bounds{Minimum: 2, Maximum: 4, CPUGrow: .97, CPUBackoff: .99, MemoryGrow: .97, MemoryBackoff: .99}}
	if e := os.Symlink(filepath.Join(dir, "monitor"), filepath.Join(cfg.Assets, "leaked-dsn")); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(cfg)
	profile := filepath.Join(dir, "profile.json")
	os.WriteFile(profile, raw, 0600)
	start := func() *exec.Cmd {
		t.Helper()
		c := exec.Command(os.Args[0], "-test.run=^TestTunerProcessHelper$")
		c.Env = append(os.Environ(), "POOLTUNE_TUNER_HELPER=1", "POOLTUNE_TUNER_PROFILE="+profile)
		if e := c.Start(); e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() {
			if c.ProcessState == nil {
				c.Process.Kill()
				c.Wait()
			}
		})
		return c
	}
	process := start()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 4 * time.Second}
	origin := "http://" + listen
	request := func(path string, body any, csrf string, out any) int {
		t.Helper()
		method := "GET"
		var data []byte
		if body != nil {
			method = "POST"
			data, _ = json.Marshal(body)
		}
		req, _ := http.NewRequestWithContext(ctx, method, origin+"/api/"+path, bytes.NewReader(data))
		if body != nil {
			req.Header.Set("Origin", origin)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-CSRF-Token", csrf)
		}
		resp, e := client.Do(req)
		if e != nil {
			return 0
		}
		defer resp.Body.Close()
		if out != nil && resp.StatusCode == 200 {
			if json.NewDecoder(resp.Body).Decode(out) != nil {
				t.Fatal("invalid private response")
			}
		}
		return resp.StatusCode
	}
	deadline := time.Now().Add(6 * time.Second)
	for request("setup/status", nil, "", nil) != 200 {
		if time.Now().After(deadline) {
			t.Fatal("private server unavailable")
		}
		time.Sleep(50 * time.Millisecond)
	}
	leak, e := client.Get(origin + "/leaked-dsn")
	if e != nil {
		t.Fatal(e)
	}
	leak.Body.Close()
	if leak.StatusCode == 200 {
		t.Fatal("unauthenticated static symlink exposed a private DSN")
	}
	claim, e := os.ReadFile(filepath.Join(dir, "claim.token"))
	if e != nil {
		t.Fatal(e)
	}
	var session struct {
		CSRF string `json:"csrf"`
	}
	if request("setup/claim", map[string]string{"claim_token": string(claim), "password": "local-test-password-123"}, "", &session) != 200 {
		t.Fatal("claim failed")
	}
	type frame struct {
		At          time.Time      `json:"at"`
		Event       string         `json:"event,omitempty"`
		Observation *tuning.Sample `json:"observation,omitempty"`
		Frozen      bool           `json:"frozen"`
		Ready       bool           `json:"ready"`
		Status      string         `json:"status"`
	}
	var frames []frame
	var view tuning.View
	record := func(event string) {
		var sample *tuning.Sample
		if view.Observation != nil {
			copy := *view.Observation
			sample = &copy
		}
		frames = append(frames, frame{At: time.Now().UTC(), Event: event, Observation: sample, Frozen: view.Frozen, Ready: view.Ready, Status: view.Status})
	}
	lastObservation := time.Time{}
	waitView := func(predicate func(tuning.View) bool, timeout time.Duration) {
		t.Helper()
		deadline := time.Now().Add(timeout)
		for {
			if request("state", nil, "", &view) == 200 {
				if view.Observation != nil && !view.Observation.At.Equal(lastObservation) {
					record("")
					lastObservation = view.Observation.At
				}
				if predicate(view) {
					return
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("state not reached: status=%s pending=%v", view.Status, view.Pending != nil)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	waitView(func(v tuning.View) bool { return v.Ready && v.Frozen }, 6*time.Second)
	if request("resume", admin.Command{ExpectedRevision: view.Revision, IdempotencyKey: "real-resume"}, session.CSRF, nil) != 200 {
		t.Fatal("resume refused")
	}
	record("Automation resumed; four direct transactions submitted")
	// Four real transactions compete for the initial two connections.
	txs := make(chan pgx.Tx, 4)
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		c, e := pgx.ConnectConfig(ctx, appCfg)
		if e != nil {
			t.Fatal(e)
		}
		defer c.Close(context.Background())
		go func(c *pgx.Conn) {
			tx, e := c.Begin(ctx)
			if e != nil {
				errs <- e
				return
			}
			txs <- tx
		}(c)
	}
	waitView(func(v tuning.View) bool {
		return v.Ready && v.Observation != nil && v.Observation.Target == 4 && v.Observation.Working == 4
	}, 25*time.Second)
	var held []pgx.Tx
	for i := 0; i < 4; i++ {
		select {
		case tx := <-txs:
			held = append(held, tx)
		case <-errs:
			t.Fatal("direct transaction failed")
		case <-ctx.Done():
			t.Fatal("transaction timed out")
		}
	}
	if e = process.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	process.Wait()
	record("Tuner process killed")
	for _, tx := range held {
		var n int
		if e = tx.QueryRow(ctx, "SELECT 42").Scan(&n); e != nil || n != 42 {
			t.Fatalf("tuner death transaction check: %v (value %d)", e, n)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
	}
	fresh, e := pgx.ConnectConfig(ctx, appCfg)
	if e != nil {
		t.Fatal(e)
	}
	defer fresh.Close(ctx)
	var n int
	if e = fresh.QueryRow(ctx, "SELECT 42").Scan(&n); e != nil || n != 42 {
		t.Fatal("normal route lost after tuner death")
	}
	record("All four transactions committed; fresh direct connection succeeded")
	process = start()
	waitView(func(v tuning.View) bool { return v.Ready && v.Frozen && v.Observation.Target == 4 }, 8*time.Second)
	record("Independent tuner restart remained frozen")
	if request("resume", admin.Command{ExpectedRevision: view.Revision, IdempotencyKey: "quiet-resume"}, session.CSRF, nil) != 200 {
		t.Fatal("quiet resume refused")
	}
	record("Automation resumed for quiet reclamation")
	waitView(func(v tuning.View) bool {
		return v.Ready && v.Observation != nil && v.Observation.Target == 3 && v.Observation.Working == 3
	}, 45*time.Second)
	record("Quiet window reclaimed one established connection")
	if len(frames) == 0 || frames[0].Observation == nil || frames[0].Observation.Target != 2 {
		t.Fatal("recording rewrote early observations")
	}
	for _, f := range frames {
		if f.Event == "Independent tuner restart remained frozen" && (f.Observation.Target != 4 || !f.Frozen) {
			t.Fatal("recording rewrote restart evidence")
		}
	}
	if output := os.Getenv("POOLTUNE_TUNER_EVIDENCE"); output != "" {
		if !filepath.IsAbs(output) {
			t.Fatal("evidence path must be absolute")
		}
		evidence := struct {
			Scope  string  `json:"scope"`
			Frames []frame `json:"frames"`
		}{"Real disposable local PostgreSQL and PgBouncer; functional correctness only. No hosted, performance, cost or HA claim.", frames}
		data, e := json.MarshalIndent(evidence, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(output, data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	t.Log("Real direct TLS pool route: automatic physical growth 2 -> 3 -> 4; tuner SIGKILL preserved four transactions and a fresh connection; independent restart stayed frozen at verified setting 4; a real quiet window reclaimed capacity to 3. No performance claim.")
}
