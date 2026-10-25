//go:build integration

package pgwire

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net"
	"os"
	"pooltune/internal/control"
	"pooltune/internal/domain"
	"pooltune/internal/policy"
	"pooltune/internal/scheduler"
	"sync"
	"testing"
	"time"
)

func TestRealTrafficCanaryRetainsTransactionAndRollsBack(t *testing.T) {
	ctx, config, cert := realPostgres(t)
	bounds := policy.Guardrails{Version: "g1", MaxChangedEntries: 5, MaxReserveFraction: .5, MinClassWeight: 1, MaxClassWeight: 10}
	reg := policy.Registry{Fallback: policy.Fallback{QueueWait: time.Second, QueueLimit: 100, ConcurrencyCap: 20}}
	p1 := policy.Policy{SchemaVersion: "1", Revision: "p1", GuardrailVersion: "g1", ClassWeights: policy.ClassWeights{Critical: 1, Normal: 1, Background: 1}}
	cp, err := policy.Compile(p1, bounds, reg)
	if err != nil {
		t.Fatal(err)
	}
	s, err := scheduler.New(scheduler.Config{MaxActive: 20, MaxQueued: 100, MaxTenants: 100, MaxWorkloads: 10, MaxTenantQueued: 100, MaxTenantActive: 20, MaxQueuedBytes: 1 << 20, RatePerSecond: 100000, RateBurst: 10000}, scheduler.Snapshot{Limits: domain.LimitSnapshot{Target: "db", Path: domain.PathPostgres, Revision: "boot", GuardrailVersion: "g1", Epoch: 1, CohortBudgets: map[string]int{"stable": 0}}, Policies: map[string]*policy.CompiledPolicy{"stable": cp}})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	key := make([]byte, 32)
	rand.Read(key)
	// Fresh isolated database created by this test. This proof is fixture-only;
	// customer restart reconciliation must inspect independent backend evidence.
	engine, err := control.New(control.Config{Target: "db", Path: domain.PathPostgres, Epoch: 1, Directory: dir, Guardrails: bounds, Registry: reg, MaxCapacity: 20, MaxLease: time.Minute, CohortKey: key, Reconcile: func(_ context.Context, r control.RecoveryRequest) (control.RecoveryEvidence, error) {
		return control.RecoveryEvidence{PreviousEpoch: r.PreviousEpoch, StableRevision: r.StableRevision, ObservedAt: time.Now(), NoOrphanWork: true, CapacityVerified: true}, nil
	}}, s)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	raw, _ := json.Marshal(map[string]policy.Policy{"stable": p1})
	env := domain.ActivationEnvelope{Revision: "init", Epoch: 1, SchemaVersion: "1", GuardrailVersion: "g1", PolicyJSON: raw, Limits: []domain.LimitSnapshot{{Target: "db", Path: domain.PathPostgres, Revision: "init", CapacityRevision: "capacity1", Effective: 20, CohortBudgets: map[string]int{"stable": 20}, GuardrailVersion: "g1", Epoch: 1}}}
	env.Checksum = control.Checksum(env)
	if _, err = engine.Activate(ctx, env); err != nil {
		t.Fatal(err)
	}
	if err = engine.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	observations := []Observation{}
	cfg := TargetConfig{Target: "db", Database: "postgres", Upstream: net.JoinHostPort(config.Host, fmt.Sprint(config.Port)), ServerTLS: &tls.Config{Certificates: []tls.Certificate{cert}}, UpstreamTLS: config.TLSConfig, MaxSessions: 16, MaxHandshakes: 4, MaxMessageBytes: 1 << 20, HandshakeTimeout: 3 * time.Second, IOTimeout: 10 * time.Second,
		ClassifySession: func(user, database, id string) domain.Classification {
			return engine.Route(domain.RequestContext{Target: "db", Path: domain.PathPostgres, Principal: user}, id)
		},
		Observe: func(o Observation) { mu.Lock(); observations = append(observations, o); mu.Unlock() }}
	server, err := NewServer(cfg, s)
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serving, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- server.Serve(serving, l) }()
	defer func() { stop(); <-done }()
	clientConfig := config.Copy()
	clientConfig.User = "fixture_app"
	clientConfig.ChannelBinding = "disable"
	clientConfig.Port = uint16(l.Addr().(*net.TCPAddr).Port)
	held, err := pgx.ConnectConfig(ctx, clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close(context.Background())
	if _, err = held.Exec(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	p2 := p1
	p2.Revision = "p2"
	p2.ExpectedPredecessor = "p1"
	p2.ClassWeights.Normal = 2
	env.ExpectedPredecessor = env.Revision
	env.Revision = "canary10"
	env.PolicyJSON, _ = json.Marshal(map[string]policy.Policy{"stable": p1, "candidate": p2})
	env.CandidateFraction = .1
	env.LeaseExpiresAt = time.Now().Add(30 * time.Second)
	env.Limits[0].Revision = env.Revision
	env.Limits[0].CohortBudgets = map[string]int{"stable": 18, "candidate": 2}
	env.Checksum = control.Checksum(env)
	if _, err = engine.Activate(ctx, env); err != nil {
		t.Fatal(err)
	}
	// One held transaction continues on its original binding through activation.
	if _, err = held.Exec(ctx, "SELECT 1"); err != nil {
		t.Fatal(err)
	}
	if _, err = held.Exec(ctx, "COMMIT"); err != nil {
		t.Fatal(err)
	}
	held.Close(ctx)
	// Distinct authenticated server sessions sample the keyed assignment.
	for i := 0; i < 100; i++ {
		c, err := pgx.ConnectConfig(ctx, clientConfig)
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err = c.QueryRow(ctx, "SELECT 42").Scan(&n); err != nil || n != 42 {
			t.Fatalf("real query %d: %v", i, err)
		}
		c.Close(ctx)
	}
	mu.Lock()
	candidate, stable, old := 0, 0, 0
	for _, o := range observations {
		if o.Kind != Completed {
			continue
		}
		if o.LimitRevision == "init" {
			old++
			continue
		}
		if o.Classification.Cohort == "candidate" {
			candidate++
			if o.Classification.Revision != "p2" {
				t.Error("wrong candidate policy")
			}
		} else {
			stable++
		}
		if o.Failed || o.FinishedAt.Before(o.AcquiredAt) || o.AcquiredAt.Before(o.ArrivedAt) {
			t.Error("invalid outcome/timing")
		}
	}
	mu.Unlock()
	if old != 1 || candidate == 0 || stable == 0 || candidate+stable != 100 {
		t.Fatalf("coverage old=%d candidate=%d stable=%d", old, candidate, stable)
	}
	if err = engine.Rollback(); err != nil {
		t.Fatal(err)
	}
	c, err := pgx.ConnectConfig(ctx, clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	if _, err = c.Exec(ctx, "SELECT 7"); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return s.Stats().Active == 0 })
	mu.Lock()
	last := observations[len(observations)-1]
	mu.Unlock()
	if last.Classification.Cohort != "stable" || last.Classification.Revision != "p1" || last.LimitRevision == "init" || last.LimitRevision == "canary10" {
		t.Fatal("rollback did not restore stable under new revision")
	}
	t.Logf("REAL PostgreSQL TLS/SCRAM traffic: 100 sessions; candidate=%d stable=%d; nominal assignment=10%%, isolated budgets=2/18; held transaction retained; rollback new revision=%s; no model/provider call or benefit claim", candidate, stable, last.LimitRevision)
}
