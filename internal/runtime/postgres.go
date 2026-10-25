// Package runtime assembles verified components. The local-pg-1 profile is a
// development path for an isolated PostgreSQL instance, not a managed-provider
// or restart-recovery compatibility claim.
package runtime

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"pooltune/internal/control"
	"pooltune/internal/controller"
	"pooltune/internal/domain"
	"pooltune/internal/durable"
	"pooltune/internal/hostmetrics"
	"pooltune/internal/measurement"
	"pooltune/internal/pgwire"
	"pooltune/internal/policy"
	"pooltune/internal/provider"
	"pooltune/internal/scheduler"
	"pooltune/internal/secrets"
	"pooltune/internal/store"
	"pooltune/internal/telemetry"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var ErrConfig = errors.New("invalid local PostgreSQL runtime configuration")

type LocalPoolConfig struct{ AdminFile, ApplicationFile, DatabaseUpstream string }

type Config struct {
	Pool                                                                *LocalPoolConfig
	RecoveryDirectory                                                   string
	StartCapacity                                                       int
	CapacitySafety                                                      *controller.Guardrails
	Schema, Target, Database, Upstream, Listen, Directory, SecretRoot   string
	CertificateFile, PrivateKeyFile, CAFile, CohortKeyFile, MonitorFile string
	Epoch                                                               uint64
	Capacity, MaxSessions                                               int
	Guardrails                                                          policy.Guardrails
	Registry                                                            policy.Registry
	Stable                                                              policy.Policy
	Scheduler                                                           scheduler.Config
}

func Load(r io.Reader) (Config, error) {
	var c Config
	data, err := io.ReadAll(io.LimitReader(r, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return c, ErrConfig
	}
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(&struct{}{}) != io.EOF || c.validate() != nil {
		return Config{}, ErrConfig
	}
	return c, nil
}
func localAddress(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	ip := net.ParseIP(host)
	return err == nil && ip != nil && ip.IsLoopback()
}
func ListenLocal(addr string) (net.Listener, error) {
	if !localAddress(addr) {
		return nil, ErrConfig
	}
	return net.Listen("tcp", addr)
}
func (c Config) validate() error {
	if (c.Schema != "local-pg-1" && c.Schema != "local-pg-pool-1") || c.Target == "" || c.Database == "" || c.Epoch == 0 || c.Capacity < 1 || c.MaxSessions < c.Capacity || c.MaxSessions > 10000 || !localAddress(c.Upstream) || !localAddress(c.Listen) || !filepath.IsAbs(c.Directory) || !filepath.IsAbs(c.SecretRoot) || c.Scheduler.MaxActive != c.Capacity || c.Stable.ExpectedPredecessor != "" || c.Guardrails.CurrentRevision != "" {
		return ErrConfig
	}
	if (c.Pool != nil) != (c.Schema == "local-pg-pool-1") {
		return ErrConfig
	}
	if p := c.Pool; p != nil {
		if c.CapacitySafety == nil || c.RecoveryDirectory != "" || !localAddress(p.DatabaseUpstream) || !filepath.IsAbs(p.AdminFile) || !filepath.IsAbs(p.ApplicationFile) {
			return ErrConfig
		}
	}
	if c.StartCapacity < 0 || c.StartCapacity > c.Capacity || (c.RecoveryDirectory != "" && !filepath.IsAbs(c.RecoveryDirectory)) {
		return ErrConfig
	}
	if c.CapacitySafety != nil {
		g := *c.CapacitySafety
		state := controller.State{Identity: telemetry.Identity{Target: c.Target, Path: domain.PathPostgres, Guardrail: c.Guardrails.Version, Policy: "initial", Capacity: "initial", Epoch: c.Epoch}, Commanded: c.Capacity, Effective: c.Capacity, LastSafe: c.Capacity, LastReady: c.Capacity, StartupVerified: true}
		if g.Ceiling > c.Capacity || localPressureDecision(time.Now(), state, domain.Resources{}, g).Err != "" {
			return ErrConfig
		}
	}
	for _, path := range []string{c.CertificateFile, c.PrivateKeyFile, c.CAFile, c.CohortKeyFile, c.MonitorFile} {
		if !filepath.IsAbs(path) {
			return ErrConfig
		}
	}
	for _, w := range c.Registry.Workloads {
		if w.Selector.Target != c.Target || w.Selector.Path != domain.PathPostgres {
			return ErrConfig
		}
	}
	return nil
}

type Traffic struct {
	pool            *provider.LocalPool
	poolJournal     *store.Store
	resume          *resumeTracker
	monitor         *pgx.ConnConfig
	capacityMetrics *atomic.Pointer[controller.MetricsSnapshot]
	databaseOwner   *durable.Files
	capacityState   controller.State
	resources       *atomic.Pointer[domain.Resources]
	measurements    *measurement.Collector
	Engine          *control.Engine
	Scheduler       *scheduler.Scheduler
	Server          *pgwire.Server
	config          Config
	mu              sync.Mutex
	totals          Counts
}
type Counts struct{ Arrivals, Admitted, Completed, Failed, Rejected, Uncertain uint64 }

func (t *Traffic) Counts() Counts { t.mu.Lock(); defer t.mu.Unlock(); return t.totals }
func (t *Traffic) record(o pgwire.Observation) {
	t.measurements.Record(o)
	t.mu.Lock()
	defer t.mu.Unlock()
	switch o.Kind {
	case pgwire.Arrived:
		t.totals.Arrivals++
	case pgwire.Admitted:
		t.totals.Admitted++
	case pgwire.Completed:
		t.totals.Completed++
		if o.Failed {
			t.totals.Failed++
		}
	case pgwire.AdmissionRejected:
		t.totals.Rejected++
	case pgwire.Indeterminate:
		t.totals.Uncertain++
	}
}

func NewPostgres(ctx context.Context, c Config) (*Traffic, error) {
	if c.validate() != nil {
		return nil, ErrConfig
	}
	secret, err := secrets.Open(c.SecretRoot)
	if err != nil {
		return nil, ErrConfig
	}
	defer secret.Close()
	certBytes, err := secret.Read(c.CertificateFile)
	if err != nil {
		return nil, ErrConfig
	}
	keyBytes, err := secret.Read(c.PrivateKeyFile)
	if err != nil {
		return nil, ErrConfig
	}
	cert, err := tls.X509KeyPair(certBytes, keyBytes)
	if err != nil {
		return nil, ErrConfig
	}
	ca, err := secret.Read(c.CAFile)
	if err != nil {
		return nil, ErrConfig
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, ErrConfig
	}
	encodedKey, err := secret.Read(c.CohortKeyFile)
	if err != nil {
		return nil, ErrConfig
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(encodedKey)))
	if err != nil || len(key) != 32 {
		return nil, ErrConfig
	}
	host, port, _ := net.SplitHostPort(c.Upstream)
	upstreamTLS := &tls.Config{RootCAs: roots, ServerName: host, MinVersion: tls.VersionTLS12}
	monitorBytes, err := secret.Read(c.MonitorFile)
	if err != nil {
		return nil, ErrConfig
	}
	monitor, err := pgx.ParseConfig(strings.TrimSpace(string(monitorBytes)))
	if err != nil {
		return nil, ErrConfig
	}
	// The secret supplies credentials, not permission to change the configured target.
	actual := net.JoinHostPort(monitor.Host, strconv.Itoa(int(monitor.Port)))
	databaseUpstream := net.JoinHostPort(host, port)
	if c.Pool != nil {
		databaseUpstream = c.Pool.DatabaseUpstream
	}
	if actual != databaseUpstream || monitor.Database != c.Database {
		return nil, ErrConfig
	}
	monitor.TLSConfig = upstreamTLS.Clone()
	monitor.Fallbacks = nil
	monitor.ConnectTimeout = 3 * time.Second
	monitor.DefaultQueryExecMode = pgx.QueryExecModeExec
	databaseOwner, err := acquireLocalDatabaseOwner(ctx, monitor)
	if err != nil {
		return nil, err
	}
	ownershipTransferred := false
	defer func() {
		if !ownershipTransferred {
			databaseOwner.Close()
		}
	}()
	var pool *provider.LocalPool
	var poolJournal *store.Store
	poolRole := ""
	if c.Pool != nil {
		read := func(path string) (*pgx.ConnConfig, error) {
			raw, e := secret.Read(path)
			if e != nil {
				return nil, ErrConfig
			}
			cfg, e := pgx.ParseConfig(strings.TrimSpace(string(raw)))
			if e != nil {
				return nil, ErrConfig
			}
			cfg.TLSConfig = upstreamTLS.Clone()
			cfg.Fallbacks = nil
			cfg.ConnectTimeout = time.Second
			cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
			return cfg, nil
		}
		admin, e := read(c.Pool.AdminFile)
		if e != nil {
			return nil, e
		}
		app, e := read(c.Pool.ApplicationFile)
		if e != nil {
			return nil, e
		}
		if net.JoinHostPort(app.Host, strconv.Itoa(int(app.Port))) != c.Upstream || app.Database != c.Database {
			return nil, ErrConfig
		}
		poolRole = app.User
		poolJournal, err = store.OpenStore(filepath.Join(c.Directory, "physical.db"))
		if err != nil {
			return nil, err
		}
		pool, err = provider.NewLocalPool(provider.LocalPoolConfig{Target: c.Target, Admin: admin, Application: app, Monitor: monitor, Allocation: c.MaxSessions, Journal: poolJournal})
		if err != nil {
			poolJournal.Close()
			return nil, err
		}
		defer func() {
			if !ownershipTransferred {
				pool.Close()
				poolJournal.Close()
			}
		}()
	}
	compiled, err := policy.Compile(c.Stable, c.Guardrails, c.Registry)
	if err != nil {
		return nil, ErrConfig
	}
	initial := domain.LimitSnapshot{Target: c.Target, Path: domain.PathPostgres, Revision: "bootstrap", CapacityRevision: "initial-capacity", GuardrailVersion: c.Guardrails.Version, Epoch: c.Epoch, CohortBudgets: map[string]int{"stable": 0}}
	sched, err := scheduler.New(c.Scheduler, scheduler.Snapshot{Limits: initial, Policies: map[string]*policy.CompiledPolicy{"stable": compiled}})
	if err != nil {
		return nil, ErrConfig
	}
	reconcile := func(ctx context.Context, r control.RecoveryRequest) (control.RecoveryEvidence, error) {
		// Restart requires the opt-in independent anchor and control confirmation.
		// Every attempt also checks actual cluster clients and prepared work.
		if r.PreviousEpoch != 0 && c.RecoveryDirectory == "" {
			return control.RecoveryEvidence{}, control.ErrRecovery
		}
		if pool != nil {
			// This new local profile does not infer independent pool recovery.
			if r.PreviousEpoch != 0 {
				return control.RecoveryEvidence{}, control.ErrRecovery
			}
			start := c.CapacitySafety.Start
			if c.StartCapacity > 0 {
				start = min(start, c.StartCapacity)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				obs, err := pool.Observe(ctx)
				if err == nil && !obs.Pending && obs.Working >= start && obs.Idle == obs.Working {
					return control.RecoveryEvidence{CapacityLimit: &start, StableRevision: r.StableRevision, ObservedAt: time.Now(), NoOrphanWork: true, CapacityVerified: true}, nil
				}
				if time.Now().After(deadline) || ctx.Err() != nil {
					return control.RecoveryEvidence{}, control.ErrRecovery
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		conn, err := pgx.ConnectConfig(probeCtx, monitor)
		if err != nil {
			return control.RecoveryEvidence{}, control.ErrRecovery
		}
		defer conn.Close(context.Background())
		var visible bool
		err = conn.QueryRow(probeCtx, "SELECT rolsuper OR pg_has_role(current_user, 'pg_read_all_stats', 'MEMBER') FROM pg_roles WHERE rolname=current_user").Scan(&visible)
		if err != nil || !visible {
			return control.RecoveryEvidence{}, control.ErrRecovery
		}
		var others, slots, prepared int
		err = conn.QueryRow(probeCtx, "SELECT count(*) FROM pg_stat_activity WHERE backend_type='client backend' AND pid<>pg_backend_pid()").Scan(&others)
		if err != nil || others != 0 {
			return control.RecoveryEvidence{}, control.ErrRecovery
		}
		err = conn.QueryRow(probeCtx, "SELECT count(*) FROM pg_prepared_xacts").Scan(&prepared)
		if err != nil || prepared != 0 {
			return control.RecoveryEvidence{}, control.ErrRecovery
		}
		err = conn.QueryRow(probeCtx, "SELECT current_setting('max_connections')::int - current_setting('superuser_reserved_connections')::int - COALESCE(current_setting('reserved_connections', true), '0')::int").Scan(&slots)
		if err != nil || slots < c.MaxSessions+1 {
			return control.RecoveryEvidence{}, control.ErrRecovery
		}
		start := c.Capacity
		if c.StartCapacity > 0 {
			start = c.StartCapacity
		}
		if c.CapacitySafety != nil {
			start = min(start, c.CapacitySafety.Start)
		}
		return control.RecoveryEvidence{CapacityLimit: &start, PreviousEpoch: r.PreviousEpoch, StableRevision: r.StableRevision, ObservedAt: time.Now(), NoOrphanWork: true, CapacityVerified: true}, nil
	}
	names := make([]string, 0, len(c.Registry.Workloads)+1)
	for _, w := range c.Registry.Workloads {
		names = append(names, w.ID)
	}
	if len(names) == 0 {
		names = append(names, "overflow")
	}
	measurements, err := measurement.New(names, c.Scheduler.MaxActive+c.Scheduler.MaxQueued)
	if err != nil {
		return nil, ErrConfig
	}
	if err = sched.EnableCapacity(names); err != nil {
		return nil, ErrConfig
	}
	resources := &atomic.Pointer[domain.Resources]{}
	resume := &resumeTracker{}
	capacityMetrics := &atomic.Pointer[controller.MetricsSnapshot]{}
	engine, err := control.New(control.Config{PhysicalBlocked: func() bool { return pool != nil && pool.Blocked() }, PhysicalApply: func(ctx context.Context, r control.PhysicalChange) error {
		if pool == nil {
			return control.ErrInvalid
		}
		reading := resources.Load()
		now := time.Now()
		g := c.CapacitySafety
		if reading == nil || reading.Source != "local-host" || reading.At.After(now) || now.Sub(reading.At) > 2*time.Second || reading.CPU >= g.CPU.Grow || reading.Memory >= g.Memory.Grow {
			return control.ErrRejected
		}
		err := pool.Apply(ctx, r.ID, r.Version, r.Target)
		// Invalidate cached readiness immediately, including an ambiguous write.
		capacityMetrics.Store(&controller.MetricsSnapshot{})
		return err
	}, ResumeEvidence: resume.snapshot, CapacityMetrics: func() controller.MetricsSnapshot {
		if m := capacityMetrics.Load(); m != nil {
			return *m
		}
		return controller.MetricsSnapshot{}
	}, RecoveryDirectory: c.RecoveryDirectory, Resources: func() domain.Resources {
		if r := resources.Load(); r != nil {
			return *r
		}
		return domain.Resources{}
	}, Measurements: measurements, Target: c.Target, Path: domain.PathPostgres, Epoch: c.Epoch, Directory: c.Directory, Guardrails: c.Guardrails, Registry: c.Registry, MaxCapacity: c.Capacity, MaxLease: time.Hour, CohortKey: key, Reconcile: reconcile}, sched)
	if err != nil {
		return nil, err
	}
	owned := false
	defer func() {
		if !owned {
			engine.Close()
		}
	}()
	traffic := &Traffic{pool: pool, poolJournal: poolJournal, resume: resume, monitor: monitor, capacityMetrics: capacityMetrics, databaseOwner: databaseOwner, resources: resources, measurements: measurements, Engine: engine, Scheduler: sched, config: c}
	server, err := pgwire.NewServer(pgwire.TargetConfig{AllowedUser: poolRole, Ready: engine.Ready, Target: c.Target, Database: c.Database, Upstream: c.Upstream, ServerTLS: &tls.Config{Certificates: []tls.Certificate{cert}}, UpstreamTLS: upstreamTLS, MaxSessions: c.MaxSessions, MaxHandshakes: min(c.MaxSessions, 16), MaxMessageBytes: 1 << 20, HandshakeTimeout: 3 * time.Second, IOTimeout: time.Minute,
		ClassifySession: func(user, database, id string) domain.Classification {
			return engine.Route(domain.RequestContext{Target: c.Target, Path: domain.PathPostgres, Principal: user}, id)
		}, Observe: traffic.record}, sched)
	if err != nil {
		return nil, err
	}
	traffic.Server = server
	if engine.View().Active.Revision == "" {
		raw, _ := json.Marshal(map[string]policy.Policy{"stable": c.Stable})
		initial.Revision = "initial"
		start := c.Capacity
		if c.StartCapacity > 0 {
			start = c.StartCapacity
		}
		initial.Effective = start
		initial.CohortBudgets = map[string]int{"stable": start}
		initial.CriticalReserve = min(int(c.Stable.ReserveFraction*float64(start)), start-1)
		env := domain.ActivationEnvelope{Revision: "initial", Epoch: c.Epoch, SchemaVersion: "1", GuardrailVersion: c.Guardrails.Version, PolicyJSON: raw, Limits: []domain.LimitSnapshot{initial}}
		env.Checksum = control.Checksum(env)
		if _, err = engine.Activate(ctx, env); err != nil {
			return nil, err
		}
	}
	// Failed proof leaves a persisted, quarantined configuration. No blind retry,
	// automatic state erasure or implicit provider mutation.
	if engine.View().Active.Epoch == c.Epoch {
		if err = engine.Recover(ctx); err != nil {
			return nil, err
		}
	} else if c.RecoveryDirectory == "" {
		return nil, control.ErrRecovery
	}
	owned = true
	ownershipTransferred = true
	return traffic, nil
}
func (t *Traffic) Close() error {
	e := t.Engine.Close()
	if t.pool != nil {
		t.pool.Close()
		t.poolJournal.Close()
	}
	if t.databaseOwner != nil {
		if next := t.databaseOwner.Close(); e == nil {
			e = next
		}
	}
	return e
}
func (t *Traffic) Health() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "alive\n") })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !t.Engine.Ready() || t.Scheduler.Stats().Effective == 0 {
			http.Error(w, "traffic unavailable", 503)
			return
		}
		io.WriteString(w, "ready\n")
	})
	return mux
}

// Serve owns three distinct surfaces: local TLS PostgreSQL, private Unix control,
// and loopback-only health. The UI and gateway are separate services.
func (t *Traffic) Serve(ctx context.Context, health net.Listener) error {
	if health == nil || !localAddress(health.Addr().String()) {
		return ErrConfig
	}
	ln, err := ListenLocal(t.config.Listen)
	if err != nil {
		return err
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	hs := &http.Server{Handler: t.Health(), ReadHeaderTimeout: time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	resourcesDone := make(chan struct{})
	go func() {
		defer close(resourcesDone)
		sampler := &hostmetrics.Sampler{}
		sample := func() {
			reading, err := sampler.Sample(ctx)
			t.recordHostSample(ctx, reading, err)
		}
		sample()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sample()
			}
		}
	}()
	defer func() { cancel(); <-resourcesDone }()
	done := make(chan error, 3)
	go func() { done <- t.Server.Serve(ctx, ln) }()
	go func() { done <- t.Engine.Serve(ctx) }()
	go func() { done <- hs.Serve(health) }()
	var first error
	received := 0
	select {
	case <-ctx.Done():
	case first = <-done:
		received++
	}
	cancel()
	hs.Close()
	for ; received < 3; received++ {
		if next := <-done; first == nil && next != nil && !errors.Is(next, http.ErrServerClosed) {
			first = next
		}
	}
	if errors.Is(first, http.ErrServerClosed) {
		first = nil
	}
	if t.Scheduler.Stats().Active > 0 {
		return errors.New("traffic stopped with unresolved PostgreSQL sessions; recovery required")
	}
	return first
}
func RunFile(ctx context.Context, path string, health net.Listener) error {
	f, err := os.Open(path)
	if err != nil {
		return ErrConfig
	}
	defer f.Close()
	c, err := Load(f)
	if err != nil {
		return err
	}
	traffic, err := NewPostgres(ctx, c)
	if err != nil {
		return err
	}
	defer traffic.Close()
	return traffic.Serve(ctx, health)
}
