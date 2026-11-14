package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSupabasePressureAcceptsSourceTimestampGeneratedDuringRequest(t *testing.T) {
	var calls atomic.Int32
	var last time.Time
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		n := float64(calls.Add(1))
		last = time.Now().UTC()
		raw := string(metricsFixture(100, 100+n*8, 100+n*2))
		raw = strings.Replace(raw, "} 100\n", fmt.Sprintf("} %.9f\n", float64(last.UnixNano())/1e9), 1)
		w.Write([]byte(raw))
	}))
	defer server.Close()
	a, e := NewDiscovery(Config{Kind: Supabase, Target: Target{ID: "test"}, Project: "project", Endpoint: server.URL, LocalTest: true, Token: func(context.Context) (string, error) { return "fixture", nil }, Timeout: time.Second, Freshness: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.Pressure(context.Background()); e != ErrPending {
		t.Fatalf("first valid counter sample should await a second sample, got %v", e)
	}
	p, e := a.Pressure(context.Background())
	if e != nil || p.CPU < .199 || p.CPU > .201 || p.At.Sub(last) > time.Microsecond || last.Sub(p.At) > time.Microsecond {
		t.Fatalf("fresh source discarded or retimestamped: %+v %v", p, e)
	}
}
