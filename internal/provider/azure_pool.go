package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"strings"
	"sync"
	"time"
)

type AzurePoolConfig struct {
	ExclusiveManagement   bool
	API                   Config
	Admin, Monitor        *pgx.ConnConfig
	Database, Role        string
	Maximum, OtherReserve int
	Journal               Journal
}
type AzurePool struct {
	mu             sync.Mutex
	cfg            AzurePoolConfig
	api            *Adapter
	admin, monitor *pgx.Conn
	operation      *Operation
}

func NewAzurePool(c AzurePoolConfig) (*AzurePool, error) {
	if !c.ExclusiveManagement || c.API.Kind != Azure || c.Journal == nil || c.Maximum < 1 || c.Maximum > 10000 || c.OtherReserve < 1 || !identifier.MatchString(c.Role) || !identifier.MatchString(c.Database) {
		return nil, ErrInvalid
	}
	host := c.API.Instance + ".postgres.database.azure.com"
	for _, p := range []*pgx.ConnConfig{c.Admin, c.Monitor} {
		if p == nil || p.Host != host || p.TLSConfig == nil || p.TLSConfig.InsecureSkipVerify || p.TLSConfig.ServerName != host || len(p.Fallbacks) != 0 {
			return nil, ErrInvalid
		}
	}
	if c.Admin.Database != "pgbouncer" || c.Admin.Port != 6432 || c.Monitor.Port != 5432 || c.Monitor.Database != c.Database {
		return nil, ErrInvalid
	}
	api, e := NewDiscovery(c.API)
	if e != nil {
		return nil, e
	}
	op, e := c.Journal.Load(c.API.Target.ID)
	if e != nil {
		return nil, ErrStorage
	}
	if op != nil && (op.Kind != Azure || op.Parameter != "pgbouncer.default_pool_size" || op.Value < 1 || op.Value > c.Maximum || op.Before < 1 || op.Before > c.Maximum) {
		return nil, ErrUncertain
	}
	c.Admin = c.Admin.Copy()
	c.Admin.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	c.Monitor = c.Monitor.Copy()
	return &AzurePool{cfg: c, api: api, operation: op}, nil
}
func (p *AzurePool) Source() string                                 { return "azure:" + p.api.azureBase() }
func (p *AzurePool) Pressure(ctx context.Context) (Pressure, error) { return p.api.Pressure(ctx) }
func (p *AzurePool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if p.admin != nil {
		p.admin.Close(ctx)
	}
	if p.monitor != nil {
		p.monitor.Close(ctx)
	}
}
func azurePoolCounts(rows []map[string]string, database, role string) (working, idle, waiting int, err error) {
	for _, r := range rows {
		if r["database"] == "pgbouncer" || r["user"] == "azuresu" {
			continue
		}
		if r["database"] != database || r["user"] != role || r["pool_mode"] != "transaction" {
			return 0, 0, 0, ErrUnsupported
		}
		a, e1 := poolInt(r["sv_active"])
		i, e2 := poolInt(r["sv_idle"])
		q, e3 := poolInt(r["cl_waiting"])
		if e1 != nil || e2 != nil || e3 != nil {
			return 0, 0, 0, ErrAPI
		}
		working += a + i
		idle += i
		waiting += q
		for _, key := range []string{"sv_used", "sv_tested", "sv_login"} {
			if r[key] != "" && r[key] != "0" {
				return 0, 0, 0, ErrPending
			}
		}
	}
	return
}
func azureCapacityConfirmed(op Operation, before, target, working, waiting int) bool {
	if target != op.Value {
		return false
	}
	if op.Value < before {
		return working <= op.Value && waiting == 0
	}
	return working == op.Value
}
func (p *AzurePool) inspect(ctx context.Context) (LocalPoolStatus, error) {
	var e error
	if p.admin == nil || p.admin.IsClosed() {
		p.admin, e = pgx.ConnectConfig(ctx, p.cfg.Admin)
		if e != nil {
			return LocalPoolStatus{}, ErrAPI
		}
	}
	if p.monitor == nil || p.monitor.IsClosed() {
		p.monitor, e = pgx.ConnectConfig(ctx, p.cfg.Monitor)
		if e != nil {
			return LocalPoolStatus{}, ErrAPI
		}
	}
	start := time.Now()
	var one int
	if p.monitor.QueryRow(ctx, "SELECT 1").Scan(&one) != nil || time.Since(start) > 500*time.Millisecond {
		return LocalPoolStatus{}, ErrAPI
	}
	config, e := poolRows(ctx, p.admin, "SHOW CONFIG")
	if e != nil {
		return LocalPoolStatus{}, e
	}
	v := map[string]string{}
	for _, r := range config {
		v[r["key"]] = r["value"]
	}
	target, e := poolInt(v["default_pool_size"])
	if e != nil || target < 1 || target > p.cfg.Maximum || v["pool_mode"] != "transaction" || v["reserve_pool_size"] != "0" || v["min_pool_size"] != "0" {
		return LocalPoolStatus{}, ErrUnsupported
	}
	rows, e := poolRows(ctx, p.admin, "SHOW POOLS")
	if e != nil {
		return LocalPoolStatus{}, e
	}
	w, i, q, e := azurePoolCounts(rows, p.cfg.Database, p.cfg.Role)
	if e != nil {
		return LocalPoolStatus{}, e
	}
	var slots, total, prepared int
	var visible bool
	var identity string
	sql := `SELECT current_setting('max_connections')::int-current_setting('superuser_reserved_connections')::int-COALESCE(current_setting('reserved_connections',true),'0')::int,(SELECT count(*) FROM pg_stat_activity WHERE backend_type='client backend'),(SELECT count(*) FROM pg_prepared_xacts),pg_has_role(current_user,'pg_read_all_stats','MEMBER'),(SELECT oid::text FROM pg_database WHERE datname=current_database())||':'||pg_postmaster_start_time()::text`
	if p.monitor.QueryRow(ctx, sql).Scan(&slots, &total, &prepared, &visible, &identity) != nil || !visible || prepared != 0 || slots < p.cfg.Maximum+p.cfg.OtherReserve || total-w > p.cfg.OtherReserve || w > p.cfg.Maximum {
		return LocalPoolStatus{}, ErrUnsupported
	}
	sum := sha256.Sum256([]byte(p.Source() + "/" + identity + "/" + p.cfg.Database + "/" + p.cfg.Role))
	id := hex.EncodeToString(sum[:])
	raw, _ := json.Marshal(v)
	hash := sha256.Sum256(raw)
	return LocalPoolStatus{Identity: id, Version: id + ":" + hex.EncodeToString(hash[:]), Target: target, Working: w, Idle: i, Waiting: q, Slots: slots, At: time.Now()}, nil
}
func (p *AzurePool) Observe(ctx context.Context) (LocalPoolStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	o, e := p.inspect(ctx)
	if e != nil {
		return o, e
	}
	if op := p.operation; op != nil {
		if !strings.HasPrefix(op.ExpectedVersion, o.Identity+":") {
			return o, ErrUncertain
		}
		if op.State != "complete" {
			o.Pending = true
			if op.ProviderID == "" || !p.api.validAzureOperation(op.ProviderID) {
				return o, ErrUncertain
			}
			raw, _, e := p.api.request(ctx, "GET", op.ProviderID, nil)
			if e != nil {
				return o, e
			}
			var r struct{ Status string }
			if json.Unmarshal(raw, &r) != nil {
				return o, ErrAPI
			}
			if r.Status == "Failed" || r.Status == "Canceled" {
				return o, ErrUncertain
			}
			if r.Status == "Succeeded" && azureCapacityConfirmed(*op, op.Before, o.Target, o.Working, o.Waiting) {
				next := *op
				next.State = "complete"
				if p.cfg.Journal.Save(next) != nil {
					return o, ErrStorage
				}
				p.operation = &next
				o.Pending = false
			}
		}
	}
	return o, nil
}
func (p *AzurePool) Apply(ctx context.Context, revision, version string, target int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !identifier.MatchString(revision) || target < 1 || target > p.cfg.Maximum {
		return ErrInvalid
	}
	if p.operation != nil && (p.operation.State != "complete" || p.operation.Revision == revision) {
		return ErrPending
	}
	before, e := p.inspect(ctx)
	if e != nil {
		return e
	}
	if before.Version != version {
		return ErrStale
	}
	if target == before.Target {
		return ErrInvalid
	}
	// A size decrease is never treated as an immediate overload stop.
	if target < before.Target && (before.Waiting != 0 || before.Working != before.Idle) {
		return ErrUnsupported
	}
	op := Operation{Target: p.cfg.API.Target, Kind: Azure, Revision: revision, ExpectedVersion: version, Parameter: "pgbouncer.default_pool_size", Before: before.Target, Value: target, StartedAt: time.Now().UTC(), State: "intent"}
	p.operation = &op
	if p.cfg.Journal.Save(op) != nil {
		return ErrStorage
	}
	op.ProviderID, e = p.api.azureApply(ctx, TargetChange{Target: op.Target, Revision: revision, ExpectedVersion: version, Parameter: op.Parameter, Value: target})
	if e != nil {
		op.State = "uncertain"
		p.cfg.Journal.Save(op)
		return ErrUncertain
	}
	op.State = "accepted"
	if p.cfg.Journal.Save(op) != nil {
		return ErrStorage
	}
	return nil
}
