package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDiscoveryReadsSettingsWithoutInventingInventoryOrWriteAuthority(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" {
			t.Error("discovery attempted mutation")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"properties":{"value":"4"}}`))
	}))
	defer server.Close()
	cfg := Config{Kind: Azure, Target: Target{ID: "discovery"}, Subscription: "sub", ResourceGroup: "group", Instance: "instance", Endpoint: server.URL, LocalTest: true, Token: func(context.Context) (string, error) { return "fixture", nil }, Timeout: time.Second, Freshness: time.Minute}
	a, e := NewDiscovery(cfg)
	if e != nil {
		t.Fatal(e)
	}
	obs, e := a.Observe(context.Background(), cfg.Target)
	if e != nil {
		t.Fatal(e)
	}
	if obs.Ready || len(obs.Scopes) != 0 || obs.Working != 0 || obs.Parameters["pgbouncer.default_pool_size"] != 4 {
		t.Fatalf("fabricated capacity: %+v", obs)
	}
	before := calls
	if _, e = a.Apply(context.Background(), TargetChange{Target: cfg.Target, Revision: "forbidden", ExpectedVersion: obs.Version, Parameter: "pgbouncer.default_pool_size", Value: 5}); e == nil || calls != before {
		t.Fatal("discovery acquired mutation authority")
	}
	cfg.ChangesEnabled = true
	if _, e = NewDiscovery(cfg); e == nil {
		t.Fatal("writer profile accepted as discovery")
	}
}

// The real stopped Cloud SQL test instance returns managed pooling enabled with
// no explicit flags. Discovery must preserve that gap without inventing defaults.
func TestGoogleDiscoveryReportsMissingFlagsWithoutClaimingReady(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"settings":{"edition":"ENTERPRISE_PLUS","settingsVersion":"11","connectionPoolConfig":{"connectionPoolingEnabled":true,"poolerCount":1}}}`))
	}))
	defer server.Close()
	cfg := Config{Kind: Google, Target: Target{ID: "discovery"}, Project: "project", Instance: "instance", Endpoint: server.URL, LocalTest: true, Token: func(context.Context) (string, error) { return "fixture", nil }, Timeout: time.Second, Freshness: time.Minute}
	a, e := NewDiscovery(cfg)
	if e != nil {
		t.Fatal(e)
	}
	obs, e := a.Observe(context.Background(), cfg.Target)
	if e != nil {
		t.Fatalf("readable configuration discarded: %v", e)
	}
	if obs.Ready || obs.Status != "discovery_incomplete" || len(obs.Parameters) != 0 || len(obs.Gaps) != 2 {
		t.Fatalf("missing flags hidden or invented: %+v", obs)
	}
}
