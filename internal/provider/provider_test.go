package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type memoryJournal struct {
	op   *Operation
	fail bool
}

func (j *memoryJournal) Load(string) (*Operation, error) {
	if j.op == nil {
		return nil, nil
	}
	copy := *j.op
	return &copy, nil
}
func (j *memoryJournal) Save(op Operation) error {
	if j.fail {
		return ErrStorage
	}
	j.op = &op
	return nil
}
func configFor(kind Kind, endpoint string, j *memoryJournal) Config {
	return Config{Kind: kind, Target: Target{ID: "db"}, Project: "project", Instance: "instance", Subscription: "subscription", ResourceGroup: "group", Endpoint: endpoint, LocalTest: true, Token: func(context.Context) (string, error) { return "fixture-token", nil }, Timeout: time.Second, Freshness: time.Second, Scopes: []Scope{{ID: "db-role-primary", Database: "app", Role: "app", Replica: "primary"}}, InventoryVerified: true, PhysicalAllocation: 20, SingleWriterVerified: true, ChangesEnabled: true, Journal: j}
}
func TestGooglePreservesFlagsAndRequiresUsefulReadiness(t *testing.T) {
	var changed atomic.Bool
	var patchCount atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("missing scoped auth")
		}
		if r.Method == "PATCH" {
			patchCount.Add(1)
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			settings := body["settings"].(map[string]any)
			if settings["settingsVersion"] != "41" {
				t.Error("missing settings version")
			}
			flags := settings["connectionPoolConfig"].(map[string]any)["flags"].([]any)
			if len(flags) != 3 {
				t.Error("unrelated flag lost")
			}
			changed.Store(true)
			json.NewEncoder(w).Encode(map[string]any{"name": "operation-1", "status": "PENDING"})
			return
		}
		if r.URL.Path == "/v1/projects/project/operations/operation-1" {
			json.NewEncoder(w).Encode(map[string]any{"status": "DONE"})
			return
		}
		size, version := "10", "41"
		if changed.Load() {
			size = "15"
			version = "42"
		}
		json.NewEncoder(w).Encode(map[string]any{"settings": map[string]any{"edition": "ENTERPRISE_PLUS", "settingsVersion": version, "connectionPoolConfig": map[string]any{"connectionPoolingEnabled": true, "flags": []any{map[string]string{"name": "max_pool_size", "value": size}, map[string]string{"name": "min_pool_size", "value": "0"}, map[string]string{"name": "ignore_startup_parameters", "value": "extra_float_digits"}}}}})
	}))
	defer api.Close()
	cfg := configFor(Google, api.URL, &memoryJournal{})
	var ready atomic.Bool
	cfg.Readiness = func(_ context.Context, _ Target, version string) ([]Evidence, error) {
		if !ready.Load() {
			return nil, nil
		}
		return []Evidence{{ScopeID: "db-role-primary", Version: version, ObservedAt: time.Now(), Working: 15, Idle: 5, Complete: true}}, nil
	}
	p, e := New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	before, e := p.Observe(ctx, cfg.Target)
	if e != nil {
		t.Fatal(e)
	}
	op, e := p.Apply(ctx, TargetChange{Target: cfg.Target, Revision: "c2", ExpectedVersion: before.Version, Parameter: "max_pool_size", Value: 15})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = p.Apply(ctx, TargetChange{Target: cfg.Target, Revision: "c3", ExpectedVersion: before.Version, Parameter: "max_pool_size", Value: 16}); e != ErrPending {
		t.Fatalf("compounding write: %v", e)
	}
	after, e := p.Reconcile(ctx, op)
	if e != nil {
		t.Fatal(e)
	}
	if after.Ready || after.Working != 0 {
		t.Fatal("API completion treated as capacity")
	}
	ready.Store(true)
	after, e = p.Reconcile(ctx, op)
	if e != nil || !after.Ready || after.Working != 15 {
		t.Fatalf("readiness: %+v %v", after, e)
	}
	if patchCount.Load() != 1 {
		t.Fatal("repeated write")
	}
}
func TestMissingAcknowledgementPersistsAndBlocksAfterRestart(t *testing.T) {
	var patches atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" {
			patches.Add(1)
			w.WriteHeader(202)
			w.Write([]byte(`{}`))
			return
		}
		w.Write([]byte(`{"properties":{"value":"10"}}`))
	}))
	defer api.Close()
	j := &memoryJournal{}
	cfg := configFor(Azure, api.URL, j)
	p, e := New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	before, e := p.Observe(context.Background(), cfg.Target)
	if e != nil {
		t.Fatal(e)
	}
	_, e = p.Apply(context.Background(), TargetChange{Target: cfg.Target, Revision: "c2", ExpectedVersion: before.Version, Parameter: "pgbouncer.default_pool_size", Value: 15})
	if e != ErrUncertain {
		t.Fatalf("missing acknowledgement: %v", e)
	}
	restarted, e := New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = restarted.Apply(context.Background(), TargetChange{Target: cfg.Target, Revision: "c3", ExpectedVersion: before.Version, Parameter: "pgbouncer.default_pool_size", Value: 16}); e != ErrPending || patches.Load() != 1 {
		t.Fatal("uncertain operation replayed")
	}
}
func TestSupabaseWarmOnlyAndSeparateScopes(t *testing.T) {
	var writes atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			writes.Add(1)
		}
		w.Write([]byte(`[{"default_pool_size":16,"pool_mode":"transaction"}]`))
	}))
	defer api.Close()
	cfg := configFor(Supabase, api.URL, &memoryJournal{})
	p, e := New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	observation, e := p.Observe(context.Background(), cfg.Target)
	if e != nil || observation.Ready {
		t.Fatal("configured pool mistaken for warm reserve")
	}
	if _, e = p.Apply(context.Background(), TargetChange{Target: cfg.Target, Revision: "c2", ExpectedVersion: observation.Version, Parameter: "default_pool_size", Value: 20}); e != ErrUnsupported || writes.Load() != 0 {
		t.Fatal("cold Supabase resize enabled")
	}
}

func TestReadinessCannotBorrowAcrossPoolScopes(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/projects/project/operations/op" {
			w.Write([]byte(`{"status":"DONE"}`))
			return
		}
		w.Write([]byte(`{"settings":{"edition":"ENTERPRISE_PLUS","settingsVersion":"42","connectionPoolConfig":{"connectionPoolingEnabled":true,"flags":[{"name":"max_pool_size","value":"15"},{"name":"min_pool_size","value":"0"}]}}}`))
	}))
	defer api.Close()
	j := &memoryJournal{op: &Operation{Target: Target{ID: "db"}, Kind: Google, Revision: "c2", Parameter: "max_pool_size", Value: 15, ProviderID: "op", State: "accepted", StartedAt: time.Now()}}
	cfg := configFor(Google, api.URL, j)
	cfg.PhysicalAllocation = 40
	cfg.Scopes = append(cfg.Scopes, Scope{ID: "other-role", Database: "app", Role: "other", Replica: "primary"})
	cfg.Readiness = func(_ context.Context, _ Target, v string) ([]Evidence, error) {
		return []Evidence{{ScopeID: "db-role-primary", Version: v, ObservedAt: time.Now(), Working: 20, Complete: true}, {ScopeID: "other-role", Version: v, ObservedAt: time.Now(), Working: 10, Complete: true}}, nil
	}
	p, e := New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	obs, e := p.Reconcile(context.Background(), *j.op)
	if e != nil {
		t.Fatal(e)
	}
	if obs.Ready || j.op.State == "complete" {
		t.Fatal("one pool substituted for another")
	}
}
func TestStorageFailurePreventsCloudWrite(t *testing.T) {
	var writes atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			writes.Add(1)
		}
		w.Write([]byte(`{"properties":{"value":"10"}}`))
	}))
	defer api.Close()
	cfg := configFor(Azure, api.URL, &memoryJournal{fail: true})
	p, e := New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	obs, e := p.Observe(context.Background(), cfg.Target)
	if e != nil {
		t.Fatal(e)
	}
	_, e = p.Apply(context.Background(), TargetChange{Target: cfg.Target, Revision: "c2", ExpectedVersion: obs.Version, Parameter: "pgbouncer.default_pool_size", Value: 15})
	if e != ErrStorage || writes.Load() != 0 {
		t.Fatal("unpersisted provider write")
	}
}
func TestProviderErrorsDoNotExposeResponseOrToken(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte("private credential details"))
	}))
	defer api.Close()
	cfg := configFor(Supabase, api.URL, &memoryJournal{})
	p, e := New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	_, e = p.Observe(context.Background(), cfg.Target)
	if e != ErrAPI {
		t.Fatalf("unexpected error: %v", e)
	}
}
