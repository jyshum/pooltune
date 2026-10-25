package burst

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sys/unix"
	"pooltune/internal/durable"
	"pooltune/internal/pgwire"
	"pooltune/internal/provider"
)

// Settings describes one exclusively budgeted, single-host experimental gateway.
// Provider settings are never mutated. Separate gateways must not share this budget.
type Settings struct {
	BackendIP                                               string
	VerifyTarget                                            func(context.Context, *pgx.ConnConfig, *pgx.ConnConfig) error
	Control                                                 Config
	Backend, Monitor                                        *pgx.ConnConfig
	SQLRole                                                 string
	OtherReserve, ProviderLimit                             int
	Exclusive                                               bool
	Directory, Listen, CertificateFile, KeyFile, UpstreamCA string
	Pressure                                                func(context.Context) (provider.Pressure, error)
}
type Runtime struct {
	transactions           *provider.Transactions
	trafficEpoch           string
	marked, started        bool
	dns                    net.PacketConn
	Controller             *Controller
	cfg                    Settings
	files, owner           *durable.Files
	cmd                    *exec.Cmd
	log                    *os.File
	monitor, admin, keeper *pgx.Conn
	client                 *pgx.ConnConfig
	server                 *pgwire.Server
	listener               net.Listener
	cancel                 context.CancelFunc
	wg                     sync.WaitGroup
	mu                     sync.Mutex
	pressure               provider.Pressure
	pids                   []int32
	identity               string
	closed                 bool
}

var atom = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)

func privatePath(s string) bool { return filepath.IsAbs(s) && !strings.ContainsAny(s, "\r\n\x00#;= ") }
func verified(c *pgx.ConnConfig) bool {
	return c != nil && c.TLSConfig != nil && !c.TLSConfig.InsecureSkipVerify && c.TLSConfig.ServerName != "" && len(c.Fallbacks) == 0
}
func (s Settings) validate() error {
	host, _, e := net.SplitHostPort(s.Listen)
	if e != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || !s.Exclusive || s.OtherReserve < 1 || s.ProviderLimit < s.Control.Ceiling || s.Pressure == nil || !verified(s.Backend) || !verified(s.Monitor) {
		return ErrUnsafe
	}
	for _, v := range []string{s.Backend.Host, s.Backend.User, s.Backend.Database, s.SQLRole} {
		if !atom.MatchString(v) {
			return ErrUnsafe
		}
	}
	for _, v := range []string{s.Directory, s.CertificateFile, s.KeyFile, s.UpstreamCA} {
		if !privatePath(v) {
			return ErrUnsafe
		}
	}
	if s.Backend.Database == "pgbouncer" || strings.ContainsAny(s.Backend.Password, "\x00\r\n") || s.Backend.Password == "" {
		return ErrUnsafe
	}
	return nil
}

func Start(ctx context.Context, c Settings) (r *Runtime, err error) {
	if e := c.validate(); e != nil {
		return nil, e
	}
	parent := ctx
	ctx, startupCancel := context.WithTimeout(ctx, 90*time.Second)
	defer startupCancel()
	if c.VerifyTarget != nil {
		if e := c.VerifyTarget(ctx, c.Backend, c.Monitor); e != nil {
			return nil, ErrUnsafe
		}
	} else if strings.HasPrefix(c.Control.PressureSource, "azure:") || strings.HasPrefix(c.Control.PressureSource, "google:") || strings.HasPrefix(c.Control.PressureSource, "supabase:") {
		return nil, ErrUnsafe
	}
	if c.BackendIP != "" {
		if net.ParseIP(c.BackendIP) == nil {
			return nil, ErrUnsafe
		}
		c.Backend = c.Backend.Copy()
		c.Monitor = c.Monitor.Copy()
		lookup := func(context.Context, string) ([]string, error) { return []string{c.BackendIP}, nil }
		c.Backend.LookupFunc = lookup
		c.Monitor.LookupFunc = lookup
	}
	r = &Runtime{cfg: c, trafficEpoch: strconv.FormatInt(time.Now().UnixNano(), 10)}
	// Cleanup is safe here: no public listener is exposed until startup succeeds.
	defer func(owned *Runtime) {
		if err != nil {
			owned.dispose(owned.marked && !owned.started)
		}
	}(r)
	r.files, err = durable.Open(c.Directory)
	if err != nil {
		return nil, err
	}
	prior, e := r.files.Load("lifecycle")
	if e == nil && string(prior) != "clean" {
		return nil, errors.New("unclean gateway requires manual backend reconciliation")
	}
	if e != nil && !os.IsNotExist(e) {
		return nil, e
	}
	r.Controller, err = NewController(c.Control, func(s State) error {
		b, e := json.Marshal(s)
		if e != nil {
			return e
		}
		return r.files.Save("state.json", b)
	})
	if err != nil {
		return nil, err
	}
	r.monitor, err = pgx.ConnectConfig(ctx, c.Monitor.Copy())
	if err != nil {
		return nil, errors.New("verified monitor connection failed")
	}
	if err = r.inventory(ctx, false); err != nil {
		return nil, err
	}
	// A machine-wide lock also fences aliases and different state directories.
	key := sha256.Sum256([]byte(c.Control.PressureSource))
	root := fmt.Sprintf("/tmp/pooltune-burst-owners-%d", os.Getuid())
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	lockdir := filepath.Join(root, hex.EncodeToString(key[:]))
	if err = os.MkdirAll(lockdir, 0700); err != nil {
		return nil, err
	}
	r.owner, err = durable.Open(lockdir)
	if err != nil {
		return nil, errors.New("database budget already owned on this host")
	}
	if err = r.inventory(ctx, false); err != nil {
		return nil, err
	}
	cert, e := tls.LoadX509KeyPair(c.CertificateFile, c.KeyFile)
	if e != nil {
		return nil, errors.New("gateway TLS keypair invalid")
	}
	pem, e := os.ReadFile(c.CertificateFile)
	if e != nil {
		return nil, e
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, ErrUnsafe
	}
	reserve, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return nil, e
	}
	port := reserve.Addr().(*net.TCPAddr).Port
	reserve.Close()
	secret := make([]byte, 32)
	if _, e = rand.Read(secret); e != nil {
		return nil, e
	}
	adminPassword := hex.EncodeToString(secret)
	quote := func(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
	if err = r.files.Save("users.txt", []byte(quote(c.Backend.User)+" "+quote(c.Backend.Password)+"\n\"pooltune_admin\" \""+adminPassword+"\"\n")); err != nil {
		return nil, err
	}
	ini := fmt.Sprintf(`[databases]
%s = host=%s port=%d dbname=%s
[pgbouncer]
listen_addr = 127.0.0.1
listen_port = %d
unix_socket_dir =
auth_type = scram-sha-256
auth_file = %s
admin_users = pooltune_admin
pool_mode = transaction
default_pool_size = %d
min_pool_size = %d
reserve_pool_size = 0
max_db_connections = %d
max_user_connections = %d
max_client_conn = %d
server_idle_timeout = 3600
server_lifetime = 3600
client_tls_sslmode = require
client_tls_cert_file = %s
client_tls_key_file = %s
server_tls_sslmode = verify-full
server_tls_ca_file = %s
log_connections = 0
log_disconnections = 0
`, c.Backend.Database, c.Backend.Host, c.Backend.Port, c.Backend.Database, port, filepath.Join(c.Directory, "users.txt"), c.Control.Ceiling, c.Control.Ceiling, c.Control.Ceiling, c.Control.Ceiling, c.Control.Queue+c.Control.Ceiling+16, c.CertificateFile, c.KeyFile, c.UpstreamCA)
	if c.BackendIP != "" {
		r.dns, e = startPinnedDNS(c.Backend.Host, c.BackendIP)
		if e != nil {
			return nil, e
		}
		if e = r.files.Save("resolv.conf", []byte("nameserver "+r.dns.LocalAddr().String()+"\n")); e != nil {
			return nil, e
		}
		ini += "resolv_conf = " + filepath.Join(c.Directory, "resolv.conf") + "\n"
	}
	if err = r.files.Save("pool.ini", []byte(ini)); err != nil {
		return nil, err
	}
	if err = r.files.Save("lifecycle", []byte("running")); err != nil {
		return nil, err
	}
	r.marked = true
	binary, e := exec.LookPath("pgbouncer")
	if e != nil {
		return nil, e
	}
	r.log, err = os.OpenFile(filepath.Join(c.Directory, "pool.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	info, e := r.log.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrUnsafe
	}
	r.cmd = exec.Command(binary, "-q", filepath.Join(c.Directory, "pool.ini"))
	r.cmd.Stdout = r.log
	r.cmd.Stderr = r.log
	a, e := r.files.InheritLock()
	if e != nil {
		return nil, e
	}
	defer a.Close()
	b, e := r.owner.InheritLock()
	if e != nil {
		return nil, e
	}
	defer b.Close()
	r.cmd.ExtraFiles = []*os.File{a, b}
	if err = r.cmd.Start(); err != nil {
		return nil, err
	}
	r.started = true
	local := c.Backend.Copy()
	local.Host = "127.0.0.1"
	local.LookupFunc = net.DefaultResolver.LookupHost
	local.Port = uint16(port)
	local.Fallbacks = nil
	local.TLSConfig = &tls.Config{RootCAs: roots, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12}
	local.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	local.RuntimeParams = map[string]string{"client_encoding": "UTF8"}
	admin := local.Copy()
	admin.Database = "pgbouncer"
	admin.User = "pooltune_admin"
	admin.Password = adminPassword
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.admin, e = pgx.ConnectConfig(ctx, admin)
		if e == nil {
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, errors.New("owned pool startup failed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err = r.warm(ctx, local); err != nil {
		return nil, err
	}
	r.keeper, err = pgx.ConnectConfig(ctx, local)
	if err != nil {
		return nil, errors.New("owned pool keeper failed")
	}
	// Initial pressure can require two scrapes; callers provide a bounded startup context.
	for {
		r.pressure, e = c.Pressure(ctx)
		if e == nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil, errors.New("provider pressure unavailable")
		case <-time.After(15 * time.Second):
		}
	}
	r.transactions = provider.PollTransactions(ctx, admin, c.Backend.Database, r.trafficEpoch)
	for {
		err = r.health(ctx)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !r.Controller.valid(time.Now()) {
		return nil, ErrUnsafe
	}
	r.listener, err = net.Listen("tcp", c.Listen)
	if err != nil {
		return nil, err
	}
	r.client = local.Copy()
	r.client.Port = uint16(r.listener.Addr().(*net.TCPAddr).Port)
	r.server, err = pgwire.NewServer(pgwire.TargetConfig{PreserveReadIdle: true, AllowedUser: local.User, Ready: r.Controller.Ready, ClassifySession: r.Controller.Classify, Target: c.Control.Target, Database: local.Database, Upstream: net.JoinHostPort(local.Host, strconv.Itoa(int(local.Port))), ServerTLS: &tls.Config{Certificates: []tls.Certificate{cert}}, UpstreamTLS: local.TLSConfig, MaxSessions: c.Control.Queue + c.Control.Ceiling, MaxHandshakes: 128, MaxMessageBytes: 1 << 20, HandshakeTimeout: 5 * time.Second, IOTimeout: 60 * time.Second}, r.Controller.Scheduler)
	if err != nil {
		return nil, err
	}
	// Parent cancellation freezes admission first. Close refuses to interrupt active work.
	run, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.wg.Add(5)
	go func() {
		defer r.wg.Done()
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-run.Done():
				return
			case <-tick.C:
				counter := provider.PollTransactions(run, admin, c.Backend.Database, r.trafficEpoch)
				r.mu.Lock()
				r.transactions = counter
				r.mu.Unlock()
			}
		}
	}()
	go func() { defer r.wg.Done(); _ = r.server.Serve(run, r.listener) }()
	go func() {
		defer r.wg.Done()
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-run.Done():
				return
			case now := <-tick.C:
				r.Controller.Tick(now)
			}
		}
	}()
	go func() {
		defer r.wg.Done()
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-run.Done():
				return
			case <-tick.C:
				q, done := context.WithTimeout(run, time.Second)
				e := r.health(q)
				done()
				if e != nil {
					r.Controller.Update(Health{})
				}
			}
		}
	}()
	go func() {
		defer r.wg.Done()
		interval := 15 * time.Second
		if strings.HasPrefix(c.Control.PressureSource, "azure:") {
			interval = 5 * time.Second
		}
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			select {
			case <-run.Done():
				return
			case <-parent.Done():
				r.Controller.Freeze()
				return
			case <-tick.C:
				q, done := context.WithTimeout(run, 3*time.Second)
				p, e := c.Pressure(q)
				done()
				r.recordPressure(p, e)
			}
		}
	}()
	return r, nil
}

func (r *Runtime) inventory(ctx context.Context, warmed bool) error {
	var slots, total, prepared, owned int
	var visible bool
	var identity, role, db string
	e := r.monitor.QueryRow(ctx, `SELECT current_setting('max_connections')::int-current_setting('superuser_reserved_connections')::int-COALESCE(current_setting('reserved_connections',true),'0')::int,(SELECT count(*) FROM pg_stat_activity WHERE backend_type='client backend'),(SELECT count(*) FROM pg_prepared_xacts),(SELECT rolsuper OR pg_has_role(current_user,'pg_read_all_stats','MEMBER') FROM pg_roles WHERE rolname=current_user),(SELECT oid::text FROM pg_database WHERE datname=current_database())||':'||pg_postmaster_start_time()::text,current_user,current_database()`).Scan(&slots, &total, &prepared, &visible, &identity, &role, &db)
	if e != nil || !visible || prepared != 0 || db != r.cfg.Backend.Database {
		return errors.New("database inventory unavailable")
	}
	if r.identity != "" && identity != r.identity {
		return errors.New("database identity changed")
	}
	r.identity = identity
	if warmed {
		e = r.monitor.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE pid=ANY($1::int[]) AND usename=$2 AND datname=$3 AND backend_type='client backend'`, r.pids, r.cfg.SQLRole, r.cfg.Backend.Database).Scan(&owned)
		if e != nil || owned != r.cfg.Control.Ceiling {
			return fmt.Errorf("warm backend inventory changed: matched=%d expected=%d query=%v", owned, r.cfg.Control.Ceiling, e)
		}
	}
	if total-owned+r.cfg.Control.Ceiling+r.cfg.OtherReserve > slots {
		return errors.New("database connection budget exceeded")
	}
	return nil
}
func (r *Runtime) warm(ctx context.Context, c *pgx.ConnConfig) error {
	conns := make([]*pgx.Conn, 0, r.cfg.Control.Ceiling)
	defer func() {
		for _, v := range conns {
			v.Close(context.Background())
		}
	}()
	// Hold explicit transactions until every distinct backend is established.
	ids := map[int32]bool{}
	for i := 0; i < r.cfg.Control.Ceiling; i++ {
		v, e := pgx.ConnectConfig(ctx, c)
		if e != nil {
			return errors.New("warm authentication failed")
		}
		conns = append(conns, v)
		if _, e = v.Exec(ctx, "BEGIN"); e != nil {
			return e
		}
		var id int32
		var role, db string
		if e = v.QueryRow(ctx, "SELECT pg_backend_pid(),current_user,current_database()").Scan(&id, &role, &db); e != nil || ids[id] || role != r.cfg.SQLRole || db != r.cfg.Backend.Database {
			return errors.New("warm backend identity mismatch")
		}
		ids[id] = true
		r.pids = append(r.pids, id)
	}
	for _, v := range conns {
		if _, e := v.Exec(ctx, "COMMIT"); e != nil {
			return errors.New("warm transaction completion failed")
		}
	}
	return nil
}
func (r *Runtime) health(ctx context.Context) error {
	if e := r.inventory(ctx, true); e != nil {
		return e
	}
	rows, e := r.admin.Query(ctx, "SHOW POOLS")
	if e != nil {
		return errors.New("pool readback failed")
	}
	defer rows.Close()
	warm, scopes := 0, 0
	for rows.Next() {
		vals, e := rows.Values()
		if e != nil {
			return e
		}
		m := map[string]any{}
		for i, f := range rows.FieldDescriptions() {
			m[f.Name] = vals[i]
		}
		if fmt.Sprint(m["database"]) == "pgbouncer" {
			continue
		}
		if fmt.Sprint(m["database"]) != r.cfg.Backend.Database || fmt.Sprint(m["user"]) != r.cfg.Backend.User {
			return errors.New("unexpected owned pool scope")
		}
		scopes++
		number := func(k string) int { v, _ := strconv.Atoi(fmt.Sprint(m[k])); return v }
		// Used/tested connections are already authenticated backends. PgBouncer
		// performs its normal liveness check before assigning them. Rejecting
		// these states deadlocks readiness after an idle period. Exact PID and
		// budget verification above remain mandatory; login is not capacity.
		warm += number("sv_active") + number("sv_idle") + number("sv_used") + number("sv_tested")
		if number("sv_login") != 0 {
			return fmt.Errorf("pool connections settling: login=%d tested=%d used=%d", number("sv_login"), number("sv_tested"), number("sv_used"))
		}
	}
	if rows.Err() != nil || scopes != 1 || warm != r.cfg.Control.Ceiling {
		return errors.New("warm pool readback incomplete")
	}
	r.mu.Lock()
	p := r.pressure
	transactions := r.transactions
	r.mu.Unlock()
	// The owned pooler measures routed traffic for Azure, Google and Supabase.
	// Provider management metrics remain separate delayed pressure signals.
	r.Controller.Update(Health{Transactions: transactions, At: time.Now(), PressureAt: p.At, Source: p.Source, CPU: p.CPU, Memory: p.Memory, Warm: warm, BudgetVerified: true})
	return nil
}
func (r *Runtime) ClientConfig() *pgx.ConnConfig { return r.client.Copy() }

// Close freezes new work and refuses to interrupt active or uncertain transactions.
// The caller retries after drain. There is no automatic bypass or SQL replay.
func (r *Runtime) Close() error {
	r.Controller.Freeze()
	stats := r.Controller.Scheduler.Stats()
	if stats.Active != 0 || stats.Uncertain != 0 {
		return errors.New("gateway still owns active or uncertain work")
	}
	r.dispose(true)
	return nil
}
func (r *Runtime) dispose(clean bool) {
	if r.closed {
		return
	}
	r.closed = true
	if r.cancel != nil {
		r.cancel()
	}
	if r.listener != nil {
		r.listener.Close()
	}
	r.wg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, v := range []*pgx.Conn{r.keeper, r.admin, r.monitor} {
		if v != nil {
			v.Close(ctx)
		}
	}
	if r.cmd != nil && r.cmd.Process != nil {
		r.cmd.Process.Kill()
		r.cmd.Wait()
	}
	if r.dns != nil {
		r.dns.Close()
	}
	if r.log != nil {
		r.log.Close()
	}
	if r.files != nil {
		if clean {
			_ = r.files.Save("lifecycle", []byte("clean"))
		}
		r.files.Close()
	}
	if r.owner != nil {
		r.owner.Close()
	}
}

// A failed refresh does not refresh or erase the last valid source sample.
// Controller.valid independently expires it at the original source deadline.
func (r *Runtime) recordPressure(p provider.Pressure, err error) {
	if err != nil {
		return
	}
	r.mu.Lock()
	r.pressure = p
	r.mu.Unlock()
}
