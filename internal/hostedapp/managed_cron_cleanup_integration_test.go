//go:build integration

package hostedapp

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"pooltune/internal/hostedquery"
	"pooltune/internal/testpg"
)

func TestManagedCronDisconnectFailureKeepsStopStateAndCleanupCredential(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t)
	pc, err := pgxpool.ParseConfig(cfg.ConnString())
	if err != nil {
		t.Fatal(err)
	}
	pc.ConnConfig = cfg
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	app, err := New(ctx, pool, Config{Origin: "https://preview.example.com", Key: bytes.Repeat([]byte{18}, 32), Sampling: hostedquery.SamplingPolicy{Interval: time.Minute, Window: 5 * time.Minute}, QueueInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	user, _, err := app.accounts.Register(ctx, "cleanup@example.com", "a sufficiently long test password")
	if err != nil {
		t.Fatal(err)
	}
	id, err := app.projects.create(ctx, user.ID, "Cleanup project", "abcdefghijklmnopqrst")
	if err != nil {
		t.Fatal(err)
	}
	row, err := app.projects.find(ctx, user.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := app.vault.seal(row, projectSecrets{Automatic: true, RepairPassword: "retained-cleanup-only-password", Capabilities: []string{"cron"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = app.projects.activate(ctx, row, sealed); err != nil {
		t.Fatal(err)
	}
	row, err = app.projects.find(ctx, user.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	worker, stop := context.WithCancel(ctx)
	defer stop()
	app.workerStops = map[string]projectProcess{id: {epoch: row.Epoch, cancel: stop}}
	failed := errors.New("provider unavailable")
	err = app.disconnectAfterCleanup(ctx, row, func(_ context.Context, p projectRecord, s projectSecrets) error {
		if worker.Err() == nil || !s.RepairsPaused || !s.DisconnectPending || p.Epoch != row.Epoch+1 {
			t.Fatal("cleanup ran before worker stop and epoch fencing")
		}
		return failed
	})
	if !errors.Is(err, failed) {
		t.Fatal(err)
	}
	pending, err := app.projects.find(ctx, user.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := app.vault.open(pending, pending.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if !pending.Configured || !saved.RepairsPaused || !saved.DisconnectPending || saved.RepairPassword != "retained-cleanup-only-password" {
		t.Fatal("failed cleanup lost retry credential or resumed repairs")
	}
	identity, _ := pending.identity()
	var epoch int64
	if err := pool.QueryRow(ctx, `SELECT monitor_epoch FROM pooltune_worker.projects WHERE project_id=$1`, identity.ID()).Scan(&epoch); err != nil || epoch != pending.Epoch {
		t.Fatal("old worker epoch was not invalidated", err)
	}
	// Retrying cleanup must not re-enable the project or rotate the grant again.
	if err = app.disconnectAfterCleanup(ctx, pending, func(_ context.Context, p projectRecord, s projectSecrets) error {
		if p.Epoch != pending.Epoch || !s.RepairsPaused || !s.DisconnectPending {
			t.Fatal("retry changed stopped state")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	final, err := app.projects.find(ctx, user.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	if final.Configured || len(final.Secret) != 0 || final.Epoch >= 0 {
		t.Fatal("successful cleanup retained active credentials")
	}
}
