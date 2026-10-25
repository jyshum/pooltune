package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"pooltune/internal/control"
	"pooltune/internal/controller"
	"pooltune/internal/domain"
	"pooltune/internal/durable"
	"pooltune/internal/hostmetrics"
	"pooltune/internal/httpingress"
	"pooltune/internal/measurement"
	"pooltune/internal/policy"
	"pooltune/internal/scheduler"
	"pooltune/internal/secrets"
	"pooltune/internal/telemetry"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// RESTConfig is an isolated local profile. HTTP admission concurrency and the
// PostgreSQL role's physical connection allocation are deliberately separate.
type RESTConfig struct {
	RecoveryDirectory                                                         string
	CapacitySafety                                                            *controller.Guardrails
	Schema, Target, Database, Upstream, Listen, Directory, SecretRoot         string
	CertificateFile, PrivateKeyFile, CAFile, CohortKeyFile, MonitorFile       string
	PostgRESTExecutable, PostgRESTDatabaseFile, DatabaseSchema, AnonymousRole string
	Issuer, Audience, JWKSFile                                                string
	KeysValidUntil                                                            time.Time
	Epoch                                                                     uint64
	Capacity, PhysicalAllocation, MaxRequests, MaxHeaderBytes                 int
	MaxBodyBytes                                                              int64
	Timeout                                                                   time.Duration
	AllowedOrigins                                                            []string
	Guardrails                                                                policy.Guardrails
	Registry                                                                  policy.Registry
	Stable                                                                    policy.Policy
	Scheduler                                                                 scheduler.Config
}

func LoadREST(r io.Reader) (RESTConfig, error) {
	var c RESTConfig
	raw, e := io.ReadAll(io.LimitReader(r, (1<<20)+1))
	if e != nil || len(raw) > 1<<20 {
		return c, ErrConfig
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(&struct{}{}) != io.EOF || c.validate() != nil {
		return RESTConfig{}, ErrConfig
	}
	return c, nil
}
func (c RESTConfig) validate() error {
	if c.Schema != "local-rest-1" || c.Target == "" || c.Database == "" || c.DatabaseSchema == "" || c.AnonymousRole == "" || c.Epoch == 0 || c.Capacity < 1 || c.PhysicalAllocation < 1 || c.PhysicalAllocation > 1000 || c.Scheduler.MaxActive != c.Capacity || c.MaxRequests < c.Capacity || c.MaxRequests > 4096 || c.MaxHeaderBytes < 1024 || c.MaxHeaderBytes > 64<<10 || c.MaxBodyBytes < 1 || c.MaxBodyBytes > 16<<20 || c.Timeout <= 0 || c.Timeout > time.Minute || !localAddress(c.Upstream) || !localAddress(c.Listen) || c.Stable.ExpectedPredecessor != "" || c.Guardrails.CurrentRevision != "" {
		return ErrConfig
	}
	if c.RecoveryDirectory != "" && !filepath.IsAbs(c.RecoveryDirectory) {
		return ErrConfig
	}
	if c.CapacitySafety != nil {
		g := *c.CapacitySafety
		state := controller.State{Identity: telemetry.Identity{Target: c.Target, Path: domain.PathREST, Guardrail: c.Guardrails.Version, Policy: "initial", Capacity: "initial", Epoch: c.Epoch}, Commanded: c.Capacity, Effective: c.Capacity, LastSafe: c.Capacity, LastReady: c.Capacity, StartupVerified: true}
		if g.Ceiling != c.Capacity || g.HTTPEnvelope != c.Capacity || localPressureDecision(time.Now(), state, domain.Resources{}, g).Err != "" {
			return ErrConfig
		}
	}
	for _, p := range []string{c.Directory, c.SecretRoot, c.CertificateFile, c.PrivateKeyFile, c.CAFile, c.CohortKeyFile, c.MonitorFile, c.PostgRESTDatabaseFile, c.PostgRESTExecutable, c.JWKSFile} {
		if !filepath.IsAbs(p) {
			return ErrConfig
		}
	}
	for _, w := range c.Registry.Workloads {
		if w.Selector.Target != c.Target || w.Selector.Path != domain.PathREST {
			return ErrConfig
		}
	}
	return nil
}

type RESTTraffic struct {
	capacityMetrics          atomic.Pointer[controller.MetricsSnapshot]
	resume                   resumeTracker
	monitor                  *pgx.ConnConfig
	appRole, applicationName string
	databaseOwner            *durable.Files
	capacityState            controller.State
	pressureMu               sync.Mutex
	Engine                   *control.Engine
	Scheduler                *scheduler.Scheduler
	Handler                  *httpingress.Handler
	cfg                      RESTConfig
	cert                     tls.Certificate
	resources                atomic.Pointer[domain.Resources]
	measurements             *measurement.Collector
	child                    *exec.Cmd
	childDone                chan error
	close                    sync.Once
}

func NewREST(ctx context.Context, c RESTConfig) (*RESTTraffic, error) {
	if c.validate() != nil {
		return nil, ErrConfig
	}
	files, e := secrets.Open(c.SecretRoot)
	if e != nil {
		return nil, ErrConfig
	}
	defer files.Close()
	read := func(p string) ([]byte, error) {
		b, e := files.Read(p)
		if e != nil {
			return nil, ErrConfig
		}
		return b, nil
	}
	certBytes, e := read(c.CertificateFile)
	if e != nil {
		return nil, e
	}
	keyBytes, e := read(c.PrivateKeyFile)
	if e != nil {
		return nil, e
	}
	cert, e := tls.X509KeyPair(certBytes, keyBytes)
	if e != nil {
		return nil, ErrConfig
	}
	ca, e := read(c.CAFile)
	if e != nil {
		return nil, e
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, ErrConfig
	}
	encoded, e := read(c.CohortKeyFile)
	if e != nil {
		return nil, e
	}
	key, e := hex.DecodeString(strings.TrimSpace(string(encoded)))
	if e != nil || len(key) != 32 {
		return nil, ErrConfig
	}
	jwks, e := read(c.JWKSFile)
	if e != nil {
		return nil, e
	}
	verifier, e := httpingress.LoadVerifier(c.Issuer, c.Audience, c.KeysValidUntil, jwks)
	if e != nil {
		return nil, ErrConfig
	}
	connection := func(file string) (*pgx.ConnConfig, error) {
		raw, e := read(file)
		if e != nil {
			return nil, e
		}
		db, e := pgx.ParseConfig(strings.TrimSpace(string(raw)))
		if e != nil {
			return nil, ErrConfig
		}
		if net.JoinHostPort(db.Host, strconv.Itoa(int(db.Port))) != c.Upstream || db.Database != c.Database {
			return nil, ErrConfig
		}
		db.TLSConfig = &tls.Config{RootCAs: roots, ServerName: db.Host, MinVersion: tls.VersionTLS12}
		db.Fallbacks = nil
		db.ConnectTimeout = 3 * time.Second
		db.DefaultQueryExecMode = pgx.QueryExecModeExec
		return db, nil
	}
	monitor, e := connection(c.MonitorFile)
	if e != nil {
		return nil, e
	}
	app, e := connection(c.PostgRESTDatabaseFile)
	if e != nil {
		return nil, e
	}
	databaseOwner, e := acquireLocalDatabaseOwner(ctx, monitor)
	if e != nil {
		return nil, e
	}
	ownershipTransferred := false
	defer func() {
		if !ownershipTransferred {
			databaseOwner.Close()
		}
	}()
	// Check before claiming durable state or starting a child. Failures here have no activation side effects.
	if e = verifyRESTDatabase(ctx, monitor, app.User, c.PhysicalAllocation, ""); e != nil {
		return nil, e
	}
	compiled, e := policy.Compile(c.Stable, c.Guardrails, c.Registry)
	if e != nil {
		return nil, ErrConfig
	}
	initial := domain.LimitSnapshot{Target: c.Target, Path: domain.PathREST, Revision: "bootstrap", CapacityRevision: "initial-capacity", GuardrailVersion: c.Guardrails.Version, Epoch: c.Epoch, CohortBudgets: map[string]int{"stable": 0}}
	sched, e := scheduler.New(c.Scheduler, scheduler.Snapshot{Limits: initial, Policies: map[string]*policy.CompiledPolicy{"stable": compiled}})
	if e != nil {
		return nil, ErrConfig
	}
	names := []string{}
	for _, w := range c.Registry.Workloads {
		names = append(names, w.ID)
	}
	if err := sched.EnableCapacity(names); err != nil {
		return nil, ErrConfig
	}
	if len(names) == 0 {
		names = append(names, "overflow")
	}
	collector, e := measurement.New(names, c.Scheduler.MaxActive+c.Scheduler.MaxQueued)
	if e != nil {
		return nil, ErrConfig
	}
	nonce := make([]byte, 16)
	if _, e = rand.Read(nonce); e != nil {
		return nil, e
	}
	out := &RESTTraffic{monitor: monitor, appRole: app.User, applicationName: "pooltune-rest-" + hex.EncodeToString(nonce), databaseOwner: databaseOwner, cfg: c, cert: cert, Scheduler: sched, measurements: collector}
	reconcile := func(ctx context.Context, r control.RecoveryRequest) (control.RecoveryEvidence, error) {
		if r.PreviousEpoch != 0 && c.RecoveryDirectory == "" {
			return control.RecoveryEvidence{}, control.ErrRecovery
		}
		if e := verifyRESTDatabase(ctx, monitor, app.User, c.PhysicalAllocation, out.applicationName); e != nil {
			return control.RecoveryEvidence{}, e
		}
		if !out.postgRESTReady(ctx) {
			return control.RecoveryEvidence{}, control.ErrRecovery
		}
		start := c.Capacity
		if c.CapacitySafety != nil {
			start = min(start, c.CapacitySafety.Start)
		}
		return control.RecoveryEvidence{CapacityLimit: &start, PreviousEpoch: r.PreviousEpoch, StableRevision: r.StableRevision, ObservedAt: time.Now(), NoOrphanWork: true, CapacityVerified: true}, nil
	}
	out.Engine, e = control.New(control.Config{CapacityMetrics: func() controller.MetricsSnapshot {
		if m := out.capacityMetrics.Load(); m != nil {
			return *m
		}
		return controller.MetricsSnapshot{}
	}, ResumeEvidence: out.resume.snapshot, RecoveryDirectory: c.RecoveryDirectory, Target: c.Target, Path: domain.PathREST, Epoch: c.Epoch, Directory: c.Directory, Guardrails: c.Guardrails, Registry: c.Registry, MaxCapacity: c.Capacity, MaxLease: time.Hour, CohortKey: key, Measurements: collector, Resources: func() domain.Resources {
		if r := out.resources.Load(); r != nil {
			return *r
		}
		return domain.Resources{}
	}, Reconcile: reconcile}, sched)
	if e != nil {
		return nil, e
	}
	owned := false
	defer func() {
		if !owned {
			out.Close()
		}
	}()
	// Legacy profiles remain quarantined. Anchored restart can launch only after
	// exclusive cluster ownership and absence of all old backend work are proven.
	if out.Engine.View().Active.Revision != "" && c.RecoveryDirectory == "" {
		return nil, control.ErrRecovery
	}
	// Runtime owns the process and its exact DB URI, pool, and private socket. A
	// separately configured socket could point at a different, unverified database.
	if e = out.startPostgREST(ctx, app, jwks); e != nil {
		return nil, e
	}
	out.Handler, e = httpingress.NewRESTHandler(httpingress.TargetConfig{Target: c.Target, LocalPostgRESTSocket: filepath.Join(c.Directory, "postgrest.sock"), MaxRequests: c.MaxRequests, MaxHeaderBytes: c.MaxHeaderBytes, MaxBodyBytes: c.MaxBodyBytes, Timeout: c.Timeout, AllowedOrigins: c.AllowedOrigins, Verify: verifier.Verify, Observe: collector.Record, ClassifyIdentity: func(request domain.RequestContext, id httpingress.Identity) domain.Classification {
		if !id.Verified {
			return out.Engine.RouteFallback(request)
		}
		return out.Engine.Route(request, id.Subject)
	}}, sched)
	if e != nil {
		return nil, e
	}
	if out.Engine.View().Active.Revision == "" {
		raw, _ := json.Marshal(map[string]policy.Policy{"stable": c.Stable})
		initial.Revision = "initial"
		initial.Effective = c.Capacity
		initial.CohortBudgets = map[string]int{"stable": c.Capacity}
		initial.CriticalReserve = min(int(c.Stable.ReserveFraction*float64(c.Capacity)), c.Capacity-1)
		env := domain.ActivationEnvelope{Revision: "initial", Epoch: c.Epoch, SchemaVersion: "1", GuardrailVersion: c.Guardrails.Version, PolicyJSON: raw, Limits: []domain.LimitSnapshot{initial}}
		env.Checksum = control.Checksum(env)
		if _, e = out.Engine.Activate(ctx, env); e != nil {
			return nil, e
		}
	}
	if out.Engine.View().Active.Epoch == c.Epoch {
		if e = out.Engine.Recover(ctx); e != nil {
			return nil, e
		}
	}
	owned = true
	ownershipTransferred = true
	return out, nil
}

func verifyRESTDatabase(ctx context.Context, monitor *pgx.ConnConfig, role string, allocation int, application string) error {
	_, err := inspectRESTDatabase(ctx, monitor, role, allocation, application, false)
	return err
}

type restInventory struct{ Slots, Owned, Idle int }

func inspectRESTDatabase(ctx context.Context, monitor *pgx.ConnConfig, role string, allocation int, application string, allowActive bool) (restInventory, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, e := pgx.ConnectConfig(ctx, monitor)
	if e != nil {
		return restInventory{}, control.ErrRecovery
	}
	defer conn.Close(context.Background())
	var visible, safe bool
	var limit, others, slots, prepared, owned, idle int
	e = conn.QueryRow(ctx, "SELECT rolsuper OR pg_has_role(current_user, 'pg_read_all_stats', 'MEMBER') FROM pg_roles WHERE rolname=current_user").Scan(&visible)
	if e != nil || !visible {
		return restInventory{}, control.ErrRecovery
	}
	e = conn.QueryRow(ctx, "SELECT rolcanlogin AND NOT rolsuper AND NOT rolbypassrls AND NOT rolreplication AND NOT rolinherit, rolconnlimit FROM pg_roles WHERE rolname=$1", role).Scan(&safe, &limit)
	if e != nil || !safe || limit < 1 || limit > allocation {
		return restInventory{}, control.ErrRecovery
	}
	e = conn.QueryRow(ctx, "SELECT count(*) FILTER (WHERE NOT (usename=$1 AND application_name=$2 AND $2<>'' AND (state='idle' OR ($3 AND state='active')))), count(*) FILTER (WHERE usename=$1 AND application_name=$2), count(*) FILTER (WHERE usename=$1 AND application_name=$2 AND state='idle') FROM pg_stat_activity WHERE backend_type='client backend' AND pid<>pg_backend_pid()", role, application, allowActive).Scan(&others, &owned, &idle)
	if e != nil || others != 0 || owned > allocation {
		return restInventory{}, control.ErrRecovery
	}
	e = conn.QueryRow(ctx, "SELECT count(*) FROM pg_prepared_xacts").Scan(&prepared)
	if e != nil || prepared != 0 {
		return restInventory{}, control.ErrRecovery
	}
	e = conn.QueryRow(ctx, "SELECT current_setting('max_connections')::int-current_setting('superuser_reserved_connections')::int-COALESCE(current_setting('reserved_connections',true),'0')::int").Scan(&slots)
	if e != nil || slots < allocation+1 {
		return restInventory{}, control.ErrRecovery
	}
	return restInventory{Slots: slots, Owned: owned, Idle: idle}, nil
}

func (t *RESTTraffic) startPostgREST(ctx context.Context, db *pgx.ConnConfig, jwks []byte) error {
	c := t.cfg
	info, e := os.Stat(c.PostgRESTExecutable)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return ErrConfig
	}
	socket := filepath.Join(c.Directory, "postgrest.sock")
	// Exclusive cluster ownership is also inherited by PostgREST. A surviving
	// old child blocks this constructor even when its parent and socket are gone.
	if info, err := os.Lstat(socket); err == nil {
		if c.RecoveryDirectory == "" || t.Engine.View().Active.Revision == "" || info.Mode()&os.ModeSocket == 0 {
			return control.ErrRecovery
		}
		conn, err := net.DialTimeout("unix", socket, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return control.ErrRecovery
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			return control.ErrRecovery
		}
		if err = os.Remove(socket); err != nil {
			return control.ErrRecovery
		}
	} else if !os.IsNotExist(err) {
		return control.ErrRecovery
	}
	u := url.URL{Scheme: "postgresql", Host: c.Upstream, Path: "/" + c.Database, User: url.UserPassword(db.User, db.Password)}
	q := url.Values{"sslmode": {"verify-full"}, "sslrootcert": {c.CAFile}, "connect_timeout": {"3"}, "application_name": {t.applicationName}}
	u.RawQuery = q.Encode()
	child := exec.Command(c.PostgRESTExecutable)
	for _, v := range os.Environ() {
		name, _, _ := strings.Cut(v, "=")
		if !strings.HasPrefix(name, "PGRST_") && !strings.HasPrefix(name, "PG") {
			child.Env = append(child.Env, v)
		}
	}
	settings := map[string]string{"DB_URI": u.String(), "DB_SCHEMAS": c.DatabaseSchema, "DB_ANON_ROLE": c.AnonymousRole, "DB_POOL": strconv.Itoa(c.PhysicalAllocation), "DB_POOL_ACQUISITION_TIMEOUT": "1", "DB_CONFIG": "false", "DB_CHANNEL_ENABLED": "false", "SERVER_UNIX_SOCKET": socket, "SERVER_UNIX_SOCKET_MODE": "600", "JWT_SECRET": string(jwks), "JWT_AUD": c.Audience, "LOG_LEVEL": "crit", "SERVER_TRACE_HEADER": ""}
	for k, v := range settings {
		child.Env = append(child.Env, "PGRST_"+k+"="+v)
	}
	// Child output can include connection diagnostics. It is intentionally not
	// surfaced to the admin UI or logs, which must never disclose DSNs or tokens.
	child.Stdout = io.Discard
	child.Stderr = io.Discard
	lease, e := t.databaseOwner.InheritLock()
	if e != nil {
		return e
	}
	defer lease.Close()
	child.ExtraFiles = []*os.File{lease}
	if e = child.Start(); e != nil {
		return errors.New("PostgREST could not start")
	}
	t.child = child
	t.childDone = make(chan error, 1)
	go func() { t.childDone <- child.Wait() }()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 200 * time.Millisecond}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e := <-t.childDone:
			t.childDone <- e
			return errors.New("PostgREST exited during startup")
		case <-deadline.C:
			return errors.New("PostgREST startup unavailable")
		case <-ticker.C:
			r, e := client.Get("http://postgrest.local/")
			if e == nil {
				io.Copy(io.Discard, r.Body)
				r.Body.Close()
				if r.StatusCode == 200 {
					return nil
				}
			}
		}
	}
}
func (t *RESTTraffic) Close() error {
	var result error
	t.close.Do(func() {
		if t.Handler != nil {
			t.Handler.Close()
		}
		if t.child != nil {
			t.child.Process.Signal(os.Interrupt)
			select {
			case <-t.childDone:
			case <-time.After(2 * time.Second):
				t.child.Process.Kill()
				<-t.childDone
			}
		}
		if t.Engine != nil {
			result = t.Engine.Close()
		}
		if t.databaseOwner != nil {
			if e := t.databaseOwner.Close(); result == nil {
				result = e
			}
		}
	})
	return result
}
func (t *RESTTraffic) Serve(ctx context.Context, health net.Listener) error {
	if health == nil || !localAddress(health.Addr().String()) {
		return ErrConfig
	}
	ln, e := ListenLocal(t.cfg.Listen)
	if e != nil {
		return e
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	server := &http.Server{Handler: t.Handler, ReadHeaderTimeout: time.Second, ReadTimeout: t.cfg.Timeout, WriteTimeout: t.cfg.Timeout, IdleTimeout: 10 * time.Second, MaxHeaderBytes: t.cfg.MaxHeaderBytes, BaseContext: func(net.Listener) context.Context { return ctx }}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "alive\n") })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !t.Engine.Ready() || t.Scheduler.Stats().Effective == 0 {
			http.Error(w, "traffic unavailable", 503)
			return
		}
		io.WriteString(w, "ready\n")
	})
	hs := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	resourcesDone := make(chan struct{})
	go func() {
		defer close(resourcesDone)
		sampler := &hostmetrics.Sampler{}
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			r, e := sampler.Sample(ctx)
			t.recordHostSample(ctx, r, e)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	defer func() { cancel(); <-resourcesDone }()
	done := make(chan error, 3)
	go func() {
		done <- server.Serve(tls.NewListener(ln, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{t.cert}}))
	}()
	go func() { done <- t.Engine.Serve(ctx) }()
	go func() { done <- hs.Serve(health) }()
	received := 0
	var first error
	select {
	case <-ctx.Done():
	case first = <-done:
		received++
	case e := <-t.childDone:
		t.childDone <- e
		first = errors.New("PostgREST process stopped")
	}
	// Quarantine before closing listeners; no new work can enter during shutdown.
	t.Engine.Quarantine()
	cancel()
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 3*time.Second)
	_ = server.Shutdown(shutdown)
	cancelShutdown()
	server.Close()
	hs.Close()
	for ; received < 3; received++ {
		if e := <-done; first == nil && e != nil && !errors.Is(e, http.ErrServerClosed) {
			first = e
		}
	}
	if errors.Is(first, http.ErrServerClosed) {
		first = nil
	}
	if t.Scheduler.Stats().Active > 0 {
		return errors.New("REST stopped with unresolved requests; recovery required")
	}
	return first
}
func RunRESTFile(ctx context.Context, path string, health net.Listener) error {
	f, e := os.Open(path)
	if e != nil {
		return ErrConfig
	}
	defer f.Close()
	c, e := LoadREST(f)
	if e != nil {
		return e
	}
	t, e := NewREST(ctx, c)
	if e != nil {
		return e
	}
	defer t.Close()
	return t.Serve(ctx, health)
}

func (t *RESTTraffic) postgRESTReady(ctx context.Context) bool {
	if t.child == nil {
		return false
	}
	select {
	case err := <-t.childDone:
		t.childDone <- err
		return false
	default:
	}

	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(t.cfg.Directory, "postgrest.sock"))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	request, err := http.NewRequestWithContext(ctx, "GET", "http://postgrest.local/", nil)
	if err != nil {
		return false
	}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	return err == nil && response.StatusCode == 200
}
