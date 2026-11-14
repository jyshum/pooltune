//go:build integration

package burst

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"github.com/jackc/pgx/v5"
	"os"
	"path/filepath"
	"pooltune/internal/provider"
	"pooltune/internal/testpg"
	"testing"
	"time"
)

func TestOwnedPoolPreservesTransactionWhileReserveOpens(t *testing.T) {
	ctx, backend, cert := testpg.Start(t)
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	certFile := filepath.Join(dir, "client.crt")
	keyFile := filepath.Join(dir, "client.key")
	os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0600)
	os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(cert.PrivateKey.(*rsa.PrivateKey))}), 0600)
	state := filepath.Join(dir, "state")
	os.Mkdir(state, 0700)
	settings := Settings{Control: Config{Target: "fixture", Base: 1, Ceiling: 2, Queue: 8, QueueWait: time.Second, PressureSource: "fixture", PressureMaxAge: 30 * time.Second}, Backend: backend, Monitor: backend, SQLRole: backend.User, OtherReserve: 8, ProviderLimit: 2, Exclusive: true, Directory: state, Listen: "127.0.0.1:0", CertificateFile: certFile, KeyFile: keyFile, UpstreamCA: certFile, Pressure: func(context.Context) (provider.Pressure, error) {
		return provider.Pressure{At: time.Now(), CPU: .1, Memory: .1, Source: "fixture"}, nil
	}}
	r, e := Start(ctx, settings)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	admin, e := pgx.ConnectConfig(ctx, backend)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = admin.Exec(ctx, "CREATE DATABASE other_budget"); e != nil {
		t.Fatal(e)
	}
	admin.Close(ctx)
	second := settings
	second.Directory = filepath.Join(dir, "second")
	os.Mkdir(second.Directory, 0700)
	second.Backend = backend.Copy()
	second.Monitor = backend.Copy()
	second.Backend.Database = "other_budget"
	second.Monitor.Database = "other_budget"
	other, e := Start(ctx, second)
	if e == nil {
		other.Close()
		t.Fatal("different databases acquired the same server budget")
	}
	if e = r.Controller.Resume(time.Now()); e != nil {
		t.Fatal(e)
	}
	initialView, err := (DashboardBackend{Controller: r.Controller}).Read(ctx, "state")
	if err != nil || initialView.(DashboardState).Transactions == nil {
		t.Fatal("owned pool counter not wired")
	}
	initialCounter := initialView.(DashboardState).Transactions
	cfg := r.ClientConfig()
	a, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close(ctx)
	b, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close(ctx)
	if _, e = a.Exec(ctx, "BEGIN"); e != nil {
		t.Fatal(e)
	}
	var firstPID int
	if e = a.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&firstPID); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := b.Exec(ctx, "SELECT 1"); done <- e }()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(750 * time.Millisecond):
		t.Fatal("queued transaction did not obtain warm reserve")
	}
	var afterPID int
	if e = a.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&afterPID); e != nil || afterPID != firstPID {
		t.Fatal("active transaction moved or interrupted")
	}
	if _, e = a.Exec(ctx, "COMMIT"); e != nil {
		t.Fatal(e)
	}
	if r.Controller.State().Effective != 2 {
		t.Fatal("actual gateway did not expand admission")
	}
	deadlineCount := time.Now().Add(2 * time.Second)
	for {
		v, _ := (DashboardBackend{Controller: r.Controller}).Read(ctx, "state")
		counter := v.(DashboardState).Transactions
		if counter != nil && counter.Total >= initialCounter.Total+2 {
			break
		}
		if time.Now().After(deadlineCount) {
			t.Fatal("gateway transactions did not reach dashboard counter")
		}
		time.Sleep(20 * time.Millisecond)
	}
	a.Close(ctx)
	b.Close(ctx)
	if e = r.Close(); e != nil {
		t.Fatal(e)
	}
	restarted, e := Start(ctx, settings)
	if e != nil {
		t.Fatalf("clean restart failed: %v", e)
	}
	defer restarted.Close()
	restartedView, _ := (DashboardBackend{Controller: restarted.Controller}).Read(ctx, "state")
	if counter := restartedView.(DashboardState).Transactions; counter == nil || counter.Epoch == initialCounter.Epoch {
		t.Fatal("restart did not reset telemetry epoch")
	}
	if e = restarted.Controller.Resume(time.Now()); e != nil {
		t.Fatal(e)
	}
	probe, e := pgx.ConnectConfig(ctx, restarted.admin.Config().Copy())
	if e != nil {
		t.Fatal(e)
	}
	defer probe.Close(ctx)
	if _, e = probe.Exec(ctx, "SET server_check_delay = 0"); e != nil {
		t.Fatal(e)
	}
	// Reproduce the live Google state without cloud setup: established backends
	// wait for PgBouncer's normal liveness check after they become idle.
	deadline := time.Now().Add(3 * time.Second)
	used := false
	for time.Now().Before(deadline) {
		rows, err := probe.Query(ctx, "SHOW POOLS")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values, err := rows.Values()
			if err != nil {
				t.Fatal(err)
			}
			for i, f := range rows.FieldDescriptions() {
				if f.Name == "sv_used" && fmt.Sprint(values[i]) == "2" {
					used = true
				}
			}
		}
		rows.Close()
		if used {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !used {
		t.Fatal("fixture did not enter the observed sv_used state")
	}
	time.Sleep(1100 * time.Millisecond) // Allow two real health observations.
	if !restarted.Controller.Ready() {
		t.Fatal("idle established backends caused a false health stop")
	}
	client, e := pgx.ConnectConfig(ctx, restarted.ClientConfig())
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close(ctx)
	if _, e = client.Exec(ctx, "SELECT 1"); e != nil {
		t.Fatal(e)
	}
}
