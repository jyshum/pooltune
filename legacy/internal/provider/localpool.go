package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const LocalPgBouncer Kind = "local-pgbouncer"

type LocalPoolConfig struct {
	Target                      string
	Admin, Application, Monitor *pgx.ConnConfig
	Allocation                  int
	Journal                     Journal
}
type LocalPoolStatus struct {
	Transactions                 *Transactions
	Identity                     string
	Version                      string
	Target, Working, Idle, Slots int
	Waiting                      int
	At                           time.Time
	Pending                      bool
}

// LocalPool manages one verified local database/role pool. Limits are connection
// units. SQL is fixed management syntax; no caller-supplied SQL or forced close.
type LocalPool struct {
	blocked   atomic.Bool
	mu        sync.Mutex
	cfg       LocalPoolConfig
	warm      *pgx.Conn
	operation *Operation
}

func NewLocalPool(c LocalPoolConfig) (*LocalPool, error) {
	if !identifier.MatchString(c.Target) || c.Journal == nil || c.Allocation < 1 || c.Allocation > 10000 {
		return nil, ErrInvalid
	}
	for _, p := range []*pgx.ConnConfig{c.Admin, c.Application, c.Monitor} {
		if p == nil || net.ParseIP(p.Host) == nil || !net.ParseIP(p.Host).IsLoopback() || p.TLSConfig == nil || p.TLSConfig.InsecureSkipVerify || p.TLSConfig.ServerName == "" || len(p.Fallbacks) > 0 {
			return nil, ErrInvalid
		}
	}
	if c.Admin.Database != "pgbouncer" || c.Admin.Host != c.Application.Host || c.Admin.Port != c.Application.Port || c.Application.Database != c.Monitor.Database || c.Application.User == c.Monitor.User || c.Application.User == c.Admin.User {
		return nil, ErrInvalid
	}
	c.Admin = c.Admin.Copy()
	c.Admin.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	c.Application = c.Application.Copy()
	c.Monitor = c.Monitor.Copy()
	op, err := c.Journal.Load(c.Target)
	if err != nil {
		return nil, ErrStorage
	}
	if op != nil && (op.Kind != LocalPgBouncer || op.Parameter != "pool_size" || op.Value < 1 || op.Value > c.Allocation) {
		return nil, ErrStorage
	}
	pool := &LocalPool{cfg: c, operation: op}
	pool.blocked.Store(true)
	return pool, nil
}
func (p *LocalPool) Blocked() bool { return p.blocked.Load() }
func (p *LocalPool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.warm != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		return p.warm.Close(ctx)
	}
	return nil
}
func poolRows(ctx context.Context, c *pgx.Conn, sql string) ([]map[string]string, error) {
	rows, err := c.Query(ctx, sql)
	if err != nil {
		return nil, ErrAPI
	}
	defer rows.Close()
	var out []map[string]string
	for rows.Next() {
		if len(out) >= 10001 {
			return nil, ErrInvalid
		}
		values, err := rows.Values()
		if err != nil {
			return nil, ErrAPI
		}
		m := map[string]string{}
		for i, f := range rows.FieldDescriptions() {
			if values[i] != nil {
				m[f.Name] = fmt.Sprint(values[i])
			}
		}
		out = append(out, m)
	}
	if rows.Err() != nil {
		return nil, ErrAPI
	}
	return out, nil
}
func poolInt(s string) (int, error) {
	n, e := strconv.Atoi(s)
	if e != nil || n < 0 || n > 1000000 {
		return 0, ErrAPI
	}
	return n, nil
}
func (p *LocalPool) inspect(ctx context.Context) (LocalPoolStatus, error) {
	c, err := pgx.ConnectConfig(ctx, p.cfg.Admin)
	if err != nil {
		return LocalPoolStatus{}, ErrAPI
	}
	defer c.Close(context.Background())
	configs, err := poolRows(ctx, c, "SHOW CONFIG")
	if err != nil {
		return LocalPoolStatus{}, err
	}
	values := map[string]string{}
	for _, r := range configs {
		values[r["key"]] = r["value"]
	}
	max, e1 := poolInt(values["default_pool_size"])
	min, e2 := poolInt(values["min_pool_size"])
	if e1 != nil || e2 != nil || max < 1 || max > p.cfg.Allocation || min != max || values["reserve_pool_size"] != "0" || values["pool_mode"] != "transaction" {
		return LocalPoolStatus{}, ErrUnsupported
	}
	dbs, err := poolRows(ctx, c, "SHOW DATABASES")
	if err != nil {
		return LocalPoolStatus{}, err
	}
	found := 0
	for _, d := range dbs {
		if d["name"] == "pgbouncer" {
			continue
		}
		found++
		if d["name"] != p.cfg.Application.Database || d["database"] != p.cfg.Monitor.Database || d["host"] != p.cfg.Monitor.Host || d["port"] != strconv.Itoa(int(p.cfg.Monitor.Port)) || d["force_user"] != "" || d["pool_size"] != strconv.Itoa(max) || d["min_pool_size"] != strconv.Itoa(min) || d["reserve_pool_size"] != "0" || (d["pool_mode"] != "" && d["pool_mode"] != "transaction") {
			return LocalPoolStatus{}, ErrUnsupported
		}
	}
	if found != 1 {
		return LocalPoolStatus{}, ErrUnsupported
	}
	waiting, err := p.waiting(ctx, c)
	if err != nil {
		return LocalPoolStatus{}, err
	}
	servers, err := poolRows(ctx, c, "SHOW SERVERS")
	if err != nil {
		return LocalPoolStatus{}, err
	}
	ids := make([]int32, 0, len(servers))
	seen := map[int]bool{}
	for _, s := range servers {
		pid, e := poolInt(s["remote_pid"])
		if e != nil || pid == 0 || seen[pid] || s["database"] != p.cfg.Application.Database || s["user"] != p.cfg.Application.User {
			return LocalPoolStatus{}, ErrUnsupported
		}
		seen[pid] = true
		ids = append(ids, int32(pid))
	}
	if len(ids) > p.cfg.Allocation {
		return LocalPoolStatus{}, ErrUnsupported
	}
	db, err := pgx.ConnectConfig(ctx, p.cfg.Monitor)
	if err != nil {
		return LocalPoolStatus{}, ErrAPI
	}
	defer db.Close(context.Background())
	identity, err := localResourceIdentity(ctx, c, db)
	if err != nil {
		return LocalPoolStatus{}, err
	}
	var visible bool
	var owned, idle, other, prepared, slots int
	if db.QueryRow(ctx, "SELECT rolsuper OR pg_has_role(current_user, 'pg_read_all_stats', 'MEMBER') FROM pg_roles WHERE rolname=current_user").Scan(&visible) != nil || !visible {
		return LocalPoolStatus{}, ErrUnsupported
	}
	if db.QueryRow(ctx, "SELECT count(*) FILTER (WHERE pid=ANY($1::int[]) AND usename=$2 AND datname=$3),count(*) FILTER (WHERE pid=ANY($1::int[]) AND state='idle'),count(*) FILTER (WHERE NOT(pid=ANY($1::int[]))) FROM pg_stat_activity WHERE backend_type='client backend' AND pid<>pg_backend_pid()", ids, p.cfg.Application.User, p.cfg.Monitor.Database).Scan(&owned, &idle, &other) != nil || owned != len(ids) || other != 0 {
		return LocalPoolStatus{}, ErrUnsupported
	}
	if db.QueryRow(ctx, "SELECT count(*) FROM pg_prepared_xacts").Scan(&prepared) != nil || prepared != 0 {
		return LocalPoolStatus{}, ErrUnsupported
	}
	if db.QueryRow(ctx, "SELECT current_setting('max_connections')::int-current_setting('superuser_reserved_connections')::int-COALESCE(current_setting('reserved_connections',true),'0')::int").Scan(&slots) != nil || slots < p.cfg.Allocation+1 {
		return LocalPoolStatus{}, ErrUnsupported
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%s/%d/%d", p.cfg.Target, p.cfg.Application.Database, p.cfg.Application.User, max, min)))
	observedAt := time.Now()
	transactions := PollTransactions(ctx, p.cfg.Admin, p.cfg.Application.Database, identity)
	return LocalPoolStatus{Transactions: transactions, Identity: identity, Version: identity + ":" + hex.EncodeToString(sum[:]), Target: max, Slots: slots, Working: owned, Idle: idle, Waiting: waiting, At: observedAt}, nil
}
func (p *LocalPool) Observe(ctx context.Context) (result LocalPoolStatus, resultErr error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	defer func() { p.blocked.Store(resultErr != nil || result.Pending) }()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// A real application-role client keeps this scope eligible for min_pool_size.
	// It is not a backend reservation or an invented ready count.
	if p.warm == nil || p.warm.IsClosed() {
		var err error
		p.warm, err = pgx.ConnectConfig(ctx, p.cfg.Application)
		if err != nil {
			return LocalPoolStatus{}, ErrAPI
		}
		if _, err = p.warm.Exec(ctx, "SELECT 1"); err != nil {
			p.warm.Close(ctx)
			return LocalPoolStatus{}, ErrAPI
		}
	}
	obs, err := p.inspect(ctx)
	if err != nil {
		return LocalPoolStatus{}, err
	}
	if op := p.operation; op != nil {
		if !strings.HasPrefix(op.ExpectedVersion, obs.Identity+":") || obs.Target != op.Value {
			return LocalPoolStatus{}, ErrUncertain
		}
		if op.State != "complete" {
			obs.Pending = true
			if obs.Working == op.Value {
				next := *op
				next.State = "complete"
				if p.cfg.Journal.Save(next) != nil {
					return LocalPoolStatus{}, ErrStorage
				}
				p.operation = &next
				obs.Pending = false
			}
		}
	}
	return obs, nil
}
func (p *LocalPool) Apply(ctx context.Context, revision, version string, target int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if !identifier.MatchString(revision) || target < 1 || target > p.cfg.Allocation {
		return ErrInvalid
	}
	if p.operation != nil && (p.operation.State != "complete" || p.operation.Revision == revision) {
		return ErrPending
	}
	before, err := p.inspect(ctx)
	if err != nil {
		return err
	}
	if before.Version != version {
		return ErrStale
	}
	if target == before.Target {
		return nil
	}
	op := Operation{Target: Target{ID: p.cfg.Target}, Kind: LocalPgBouncer, Revision: revision, ExpectedVersion: version, Parameter: "pool_size", Value: target, StartedAt: time.Now().UTC(), State: "intent"}
	// Retain uncertain intent in memory even if its first durable write fails.
	p.operation = &op
	p.blocked.Store(true)
	if p.cfg.Journal.Save(op) != nil {
		return ErrStorage
	}
	c, err := pgx.ConnectConfig(ctx, p.cfg.Admin)
	if err == nil {
		defer c.Close(context.Background())
		order := []string{"default_pool_size", "min_pool_size"}
		if target < before.Target {
			order[0], order[1] = order[1], order[0]
		}
		for _, key := range order {
			if _, err = c.Exec(ctx, fmt.Sprintf("SET %s = %d", key, target)); err != nil {
				break
			}
		}
	}
	if err != nil {
		op.State = "uncertain"
		_ = p.cfg.Journal.Save(op)
		return ErrUncertain
	}
	op.State = "accepted"
	if p.cfg.Journal.Save(op) != nil {
		return ErrStorage
	}
	return nil
}
