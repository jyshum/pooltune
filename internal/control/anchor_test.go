package control

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestIndependentAnchorRejectsRestoredTrafficAndReusedEpoch(t *testing.T) {
	dir, anchor := t.TempDir(), t.TempDir()
	os.Chmod(anchor, 0700)
	e, s := fixture(t, dir, 1, approved)
	cfg := e.cfg
	e.Close()
	cfg.RecoveryDirectory = anchor
	e, err := New(cfg, s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Activate(context.Background(), envelope("p1", "", 1)); err != nil {
		t.Fatal(err)
	}
	if err = e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	old, _ := os.ReadFile(filepath.Join(dir, "activation.json"))
	if _, err = e.Activate(context.Background(), envelope("p2", "p1", 1)); err != nil {
		t.Fatal(err)
	}
	e.Close()
	if err = os.WriteFile(filepath.Join(dir, "activation.json"), old, 0600); err != nil {
		t.Fatal(err)
	}
	// Both a restored traffic snapshot and a restored control ledger describing p1
	// must still disagree with the independently retained anchor for p2.
	bootstrap, fresh := fixture(t, t.TempDir(), 2, approved)
	bootstrap.Close()
	cfg.Epoch = 2
	if restored, err := New(cfg, fresh); err == nil {
		restored.Close()
		t.Fatal("restored state accepted")
	}
}

func TestAnchoredRecoveryNeedsMatchingControlConfirmation(t *testing.T) {
	dir, anchor := t.TempDir(), t.TempDir()
	os.Chmod(anchor, 0700)
	e, s := fixture(t, dir, 1, approved)
	cfg := e.cfg
	e.Close()
	cfg.RecoveryDirectory = anchor
	e, err := New(cfg, s)
	if err != nil {
		t.Fatal(err)
	}
	first := envelope("p1", "", 1)
	e.Activate(context.Background(), first)
	if err = e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.Close()
	bootstrap, fresh := fixture(t, t.TempDir(), 2, approved)
	bootstrap.Close()
	cfg.Epoch = 2
	e, err = New(cfg, fresh)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err = e.Recover(context.Background()); err == nil {
		t.Fatal("restart accepted without independent control confirmation")
	}
	wrong := first
	wrong.Checksum = "wrong"
	if err = e.RecoverConfirmed(context.Background(), wrong); err == nil {
		t.Fatal("wrong confirmation accepted")
	}
	if err = e.RecoverConfirmed(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if !e.Ready() || e.View().Active.Epoch != 2 {
		t.Fatal("recovery failed")
	}
}

func TestAnchoredRecoveryAttemptConsumesEpoch(t *testing.T) {
	dir, anchor := t.TempDir(), t.TempDir()
	os.Chmod(anchor, 0700)
	e, s := fixture(t, dir, 1, approved)
	cfg := e.cfg
	e.Close()
	cfg.RecoveryDirectory = anchor
	e, err := New(cfg, s)
	if err != nil {
		t.Fatal(err)
	}
	e.Activate(context.Background(), envelope("p1", "", 1))
	if err = e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.Close()
	bootstrap, fresh := fixture(t, t.TempDir(), 2, approved)
	bootstrap.Close()
	cfg.Epoch = 2
	e, err = New(cfg, fresh)
	if err != nil {
		t.Fatal(err)
	}
	e.Close()
	bootstrap, fresh = fixture(t, t.TempDir(), 2, approved)
	bootstrap.Close()
	if duplicate, err := New(cfg, fresh); err == nil {
		duplicate.Close()
		t.Fatal("interrupted attempt reused epoch")
	}
}

func TestRecoveryStartsAtVerifiedLimit(t *testing.T) {
	dir := t.TempDir()
	e, _ := fixture(t, dir, 1, approved)
	e.Activate(context.Background(), envelope("p1", "", 1))
	e.Recover(context.Background())
	e.Close()
	restarted, s := fixture(t, dir, 2, func(ctx context.Context, r RecoveryRequest) (RecoveryEvidence, error) {
		p, _ := approved(ctx, r)
		limit := 3
		p.CapacityLimit = &limit
		return p, nil
	})
	defer restarted.Close()
	if err := restarted.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.Stats().Effective != 3 {
		t.Fatal("restart restored high capacity")
	}
}
