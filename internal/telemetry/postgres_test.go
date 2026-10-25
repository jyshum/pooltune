package telemetry

import (
	"context"
	"os"
	"path/filepath"
	"pooltune/internal/admin"
	"strconv"
	"testing"
	"time"
)

type testSource struct{}

type stalledClose struct{ bounded bool }

func (s *stalledClose) Close(ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	s.bounded = ok && time.Until(deadline) <= time.Second
	if !ok {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestHistoryDisconnectHasDeadline(t *testing.T) {
	c := &stalledClose{}
	start := time.Now()
	closeHistoryConnection(c)
	if !c.bounded || time.Since(start) > 2*time.Second {
		t.Fatal("history disconnect must be bounded even when the server stalls")
	}
}

func (testSource) Read(context.Context, string) (any, error) { return "controller unaffected", nil }
func (testSource) Execute(context.Context, string, admin.Command) (any, error) {
	return "controller unaffected", nil
}
func TestDisabledAndInvalidHistoryPreservesBackend(t *testing.T) {
	t.Setenv("POOLTUNE_HISTORY_URL_FILE", "")
	b, close := WrapEnv(context.Background(), testSource{}, "test")
	defer close()
	v, e := b.Read(context.Background(), "state")
	if e != nil || v != "controller unaffected" {
		t.Fatal("state changed")
	}
	h, e := b.Read(context.Background(), "telemetry")
	if e != nil || h.(View).Enabled {
		t.Fatal("disabled history")
	}
	t.Setenv("POOLTUNE_HISTORY_URL_FILE", "/nonexistent")
	t.Setenv("POOLTUNE_HISTORY_STREAM", "test")
	bad, stop := WrapEnv(context.Background(), testSource{}, "test")
	defer stop()
	v, e = bad.Execute(context.Background(), "freeze", admin.Command{})
	if e != nil || v != "controller unaffected" {
		t.Fatal("commands changed")
	}
	h, e = bad.Read(context.Background(), "telemetry")
	if e != nil || h.(View).Status != "configuration unavailable" {
		t.Fatal("redacted error")
	}
}
func TestTLSAndPrivateFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "url")
	os.WriteFile(p, []byte("postgresql://user:secret@db.example/test?sslmode=require"), 0600)
	d, e := FromFile(p)
	if e != nil || d.config.TLSConfig.InsecureSkipVerify || len(d.config.Fallbacks) != 0 {
		t.Fatal("TLS verification")
	}
	os.Chmod(p, 0644)
	if _, e = FromFile(p); e == nil {
		t.Fatal("public file")
	}
	os.Chmod(p, 0600)
	os.WriteFile(p, []byte("postgresql://user:secret@db.example/test?sslmode=disable"), 0600)
	if _, e = FromFile(p); e == nil {
		t.Fatal("plaintext")
	}
}
func TestLiveTigerHistory(t *testing.T) {
	path := os.Getenv("POOLTUNE_TEST_HISTORY_URL_FILE")
	if path == "" {
		t.Skip("explicit Tiger Data credential required")
	}
	d, e := FromFile(path)
	if e != nil {
		t.Fatal("private config rejected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, e := d.connect(ctx)
	if e != nil {

		t.Fatal("TLS connection failed")
	}
	defer c.Close(context.Background())
	if d.initialize(ctx, c) != nil {
		t.Fatal("hypertable initialization failed")
	}
	stream := "verification-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	// Only rows created by this disposable test are removed.
	defer c.Exec(context.Background(), "DELETE FROM pooltune_history.samples WHERE stream=$1", stream)
	now := time.Now().UTC().Truncate(time.Minute).Add(-time.Minute)
	for _, provider := range []string{"azure", "google", "supabase", "local"} {
		a := Sample{At: now, Provider: provider, Total: "9007199254741000", Epoch: "controlled-test", Demo: true, Limit: 4, Warm: 8, Active: 2, Queued: 1}
		b := a
		b.At = now.Add(time.Second)
		b.Total = "9007199254741010"
		a = Measure(nil, a)
		b = Measure(&a, b)
		if d.write(ctx, c, stream, a) != nil || d.write(ctx, c, stream, b) != nil || d.write(ctx, c, stream, b) != nil {
			t.Fatal("write failed")
		}
	}
	// query uses a new TLS connection: persistence does not depend on writer memory.
	rows, e := d.query(ctx, stream)
	if e != nil || len(rows) != 4 {
		t.Fatalf("readback failed: %d groups", len(rows))
	}
	for _, r := range rows {
		if r.Readings != 2 || r.Rate == nil || *r.Rate != 10 || r.Peak == nil || *r.Peak != 10 || !r.Demo {
			t.Fatalf("aggregate mismatch: %#v", r)
		}
	}
	other, e := d.query(ctx, stream+"-other")
	if e != nil || len(other) != 0 {
		t.Fatal("stream isolation failed")
	}
	var hypertable bool
	if c.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM timescaledb_information.hypertables WHERE hypertable_schema='pooltune_history' AND hypertable_name='samples')").Scan(&hypertable) != nil || !hypertable {
		t.Fatal("not a Timescale hypertable")
	}
	t.Log("Tiger Data verified: certificate-verified TLS, hypertable, four labeled provider fixtures, exact counters, duplicate suppression, time_bucket rates, new-connection persistence, stream isolation")
}
