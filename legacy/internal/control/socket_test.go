package control

import (
	"context"
	"os"
	"path/filepath"
	"pooltune/internal/domain"
	"pooltune/internal/store"
	"testing"
	"time"
)

type lostACK struct {
	engine *Engine
	calls  int
}

func (c *lostACK) Activate(ctx context.Context, env domain.ActivationEnvelope) (ActivationAck, error) {
	c.calls++
	ack, err := c.engine.Activate(ctx, env)
	if err != nil {
		return ack, err
	}
	if ack.State == "prepared" {
		if err = c.engine.Recover(ctx); err != nil {
			return ack, err
		}
	}
	return ActivationAck{}, ErrRecovery
}
func (c *lostACK) Status(_ context.Context, r domain.RevisionID) (ActivationAck, error) {
	return c.engine.Status(r), nil
}
func TestLostAcknowledgementReconcilesWithoutResending(t *testing.T) {
	engine, _ := fixture(t, t.TempDir(), 1, approved)
	defer engine.Close()
	ledger, err := store.OpenStore(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	client := &lostACK{engine: engine}
	c, err := NewCoordinator("db", domain.PathPostgres, dir, ledger, client)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.Apply(context.Background(), envelope("p1", "", 1)); err == nil {
		t.Fatal("expected lost acknowledgement")
	}
	pending, err := ledger.Pending(c.LedgerTarget())
	if err != nil || len(pending) != 1 {
		t.Fatal("intent not retained")
	}
	ack, err := c.Reconcile(context.Background(), "p1")
	if err != nil || ack.State != "active" || client.calls != 1 {
		t.Fatalf("reconciliation replay: %+v %v", ack, err)
	}
	revision, err := ledger.ActiveRevision(c.LedgerTarget())
	if err != nil || revision.ID != "p1" {
		t.Fatal("confirmation missing")
	}
}
func TestPrivateUnixControlRoundTrip(t *testing.T) {
	// macOS has a short Unix socket path limit; use a short private fixture directory.
	dir, err := os.MkdirTemp("/tmp", "pt-control-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	e, _ := fixture(t, dir, 1, approved)
	defer e.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Serve(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err = os.Stat(filepath.Join(dir, "control.sock")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket unavailable")
		}
		time.Sleep(time.Millisecond)
	}
	client, err := NewClient(filepath.Join(dir, "control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ack, err := client.Activate(ctx, envelope("p1", "", 1))
	if err != nil || ack.State != "prepared" {
		t.Fatalf("activation: %+v %v", ack, err)
	}
	if err = client.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	ack, err = client.Status(ctx, "p1")
	if err != nil || ack.State != "active" {
		t.Fatal("status mismatch")
	}
	info, err := os.Stat(filepath.Join(dir, "control.sock"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("socket not private")
	}
}

func TestCoordinatorRejectsWrongPathBeforeIntent(t *testing.T) {
	engine, _ := fixture(t, t.TempDir(), 1, approved)
	defer engine.Close()
	ledger, err := store.OpenStore(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	c, err := NewCoordinator("db", domain.PathPostgres, dir, ledger, &lostACK{engine: engine})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	env := envelope("p1", "", 1)
	env.Limits[0].Path = domain.PathREST
	env.Checksum = Checksum(env)
	if _, err = c.Apply(context.Background(), env); err != ErrInvalid {
		t.Fatal("wrong path reached traffic")
	}
	pending, err := ledger.Pending(c.LedgerTarget())
	if err != nil || len(pending) != 0 {
		t.Fatal("wrong path wrote intent")
	}
}

func (c *lostACK) View(context.Context) (StateView, error) { return c.engine.View(), nil }
func (c *lostACK) AcknowledgeFallback(_ context.Context, r domain.RevisionID, checksum string) error {
	return c.engine.AcknowledgeFallback(r, checksum)
}
func TestRecoveryIsAppendedWithoutConfirmingUnknownOldRevision(t *testing.T) {
	dir := t.TempDir()
	engine, _ := fixture(t, dir, 1, approved)
	ledger, err := store.OpenStore(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	payloadDir := t.TempDir()
	os.Chmod(payloadDir, 0700)
	client := &lostACK{engine: engine}
	c, err := NewCoordinator("db", domain.PathPostgres, payloadDir, ledger, client)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Apply(context.Background(), envelope("p1", "", 1))
	engine.Close()
	restarted, _ := fixture(t, dir, 2, approved)
	defer restarted.Close()
	client.engine = restarted
	if err = restarted.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	ack, err := c.SyncFallback(context.Background())
	if err != nil || ack.Revision != "recovery-2" {
		t.Fatalf("sync: %+v %v", ack, err)
	}
	active, err := ledger.ActiveRevision(c.LedgerTarget())
	if err != nil || active.ID != "recovery-2" {
		t.Fatal("fallback history missing")
	}
	pending, err := ledger.Pending(c.LedgerTarget())
	if err != nil || len(pending) != 1 || pending[0].ID != "p1" {
		t.Fatal("unknown old revision fabricated as confirmed")
	}
}
