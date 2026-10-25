package runtime

import (
	"context"
	"github.com/jackc/pgx/v5"
	"pooltune/internal/control"
	"pooltune/internal/controller"
	"pooltune/internal/domain"
	"time"
)

func (t *Traffic) sampleCapacity(ctx context.Context, r domain.Resources) {
	// Failed monitoring immediately replaces the cache with missing evidence.
	m := controller.MetricsSnapshot{}
	defer func() {
		t.capacityMetrics.Store(&m)
		healthy := m.Ready.Valid && m.Idle.Valid && m.Ready.Value == m.Idle.Value && m.Physical.Valid && m.Physical.Other == 0
		capacity := min(int(m.Ready.Value), t.config.Capacity, m.Physical.Max-m.Physical.Reserved)
		t.resume.observe(time.Now(), t.Engine.View(), r, t.config.CapacitySafety, capacity, healthy)
	}()
	if t.config.CapacitySafety == nil || r.Source != "local-host" || !t.Engine.Ready() {
		return
	}
	m.AuthoritySHA256 = control.CapacityChecksum(*t.config.CapacitySafety)
	m.CPU = controller.Observation{Value: r.CPU, At: r.At, Source: r.Source, Valid: true}
	m.Memory = controller.Observation{Value: r.Memory, At: r.At, Source: r.Source, Valid: true}
	if t.pool != nil {
		obs, err := t.pool.Observe(ctx)
		if err != nil {
			return
		}
		now := time.Now()
		allocation := t.config.MaxSessions
		m.Ready = controller.Observation{Value: float64(min(obs.Working, obs.Target)), At: now, Source: "verified-local-pgbouncer", Valid: true}
		m.Idle = controller.Observation{Value: float64(obs.Idle), At: now, Source: "pg_stat_activity", Valid: true}
		m.Physical = controller.PhysicalBudget{Valid: true, At: now, Source: "local-pgbouncer", Owner: t.config.Target, Version: t.config.Guardrails.Version, Max: obs.Slots, Reserved: 1, PGAllocation: allocation, User: allocation, Provider: allocation, Memory: allocation, ObservedPG: obs.Working}
		m.Pool = &controller.PoolState{Version: obs.Version, Desired: obs.Target, Pending: obs.Pending}
		return
	}
	probe, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(probe, t.monitor)
	if err != nil {
		return
	}
	defer conn.Close(context.Background())
	ids := t.Server.BackendPIDs()
	var visible bool
	if conn.QueryRow(probe, "SELECT rolsuper OR pg_has_role(current_user, 'pg_read_all_stats', 'MEMBER') FROM pg_roles WHERE rolname=current_user").Scan(&visible) != nil || !visible {
		return
	}
	var total, reserved, owned, other, idle int
	if conn.QueryRow(probe, "SELECT current_setting('max_connections')::int, current_setting('superuser_reserved_connections')::int + COALESCE(current_setting('reserved_connections', true),'0')::int").Scan(&total, &reserved) != nil {
		return
	}
	if conn.QueryRow(probe, "SELECT count(*) FILTER (WHERE pid=ANY($1::int[])), count(*) FILTER (WHERE NOT(pid=ANY($1::int[]))), count(*) FILTER (WHERE pid=ANY($1::int[]) AND state='idle') FROM pg_stat_activity WHERE backend_type='client backend' AND pid<>pg_backend_pid()", ids).Scan(&owned, &other, &idle) != nil || owned != len(ids) {
		return
	}
	var prepared int
	if conn.QueryRow(probe, "SELECT count(*) FROM pg_prepared_xacts").Scan(&prepared) != nil || prepared != 0 {
		return
	}
	now := time.Now()
	c := t.config
	m.Ready = controller.Observation{Value: float64(owned), At: now, Source: "postgres-authenticated-sessions", Valid: true}
	m.Idle = controller.Observation{Value: float64(idle), At: now, Source: "pg_stat_activity", Valid: true}
	// The configured allocation is an owner ceiling, never readiness. Reserve a
	// monitoring connection explicitly; account for every other cluster client.
	m.Physical = controller.PhysicalBudget{Valid: true, At: now, Source: "local-postgres", Owner: c.Target, Version: c.Guardrails.Version, Max: total, Reserved: reserved + 1, Other: other, PGAllocation: c.MaxSessions, User: c.MaxSessions, Provider: c.MaxSessions, Memory: c.MaxSessions, ObservedPG: owned}
}

// HTTP readiness is the approved request envelope, conditional on fresh owned
// PostgREST/cluster evidence. It is never presented as warm SQL connections.
func (t *RESTTraffic) sampleCapacity(ctx context.Context, r domain.Resources) {
	m := controller.MetricsSnapshot{}
	idle := false
	defer func() {
		t.capacityMetrics.Store(&m)
		t.resume.observe(time.Now(), t.Engine.View(), r, t.cfg.CapacitySafety, int(m.Ready.Value), m.Ready.Valid && idle)
	}()
	if t.cfg.CapacitySafety == nil || r.Source != "local-host" || !t.Engine.Ready() {
		return
	}
	inv, err := inspectRESTDatabase(ctx, t.monitor, t.appRole, t.cfg.PhysicalAllocation, t.applicationName, true)
	if err != nil || !t.postgRESTReady(ctx) {
		return
	}
	now := time.Now()
	g := t.cfg.CapacitySafety
	m.AuthoritySHA256 = control.CapacityChecksum(*g)
	m.CPU = controller.Observation{Value: r.CPU, At: r.At, Source: r.Source, Valid: true}
	m.Memory = controller.Observation{Value: r.Memory, At: r.At, Source: r.Source, Valid: true}
	m.Ready = controller.Observation{Value: float64(min(t.cfg.Capacity, g.HTTPEnvelope)), At: now, Source: "verified-local-http-envelope", Valid: true}
	m.Physical = controller.PhysicalBudget{Valid: true, At: now, Source: "local-postgrest-allocation", Owner: t.cfg.Target, Version: t.cfg.Guardrails.Version, Max: inv.Slots, Reserved: 1, RESTEnvelope: t.cfg.PhysicalAllocation, ObservedREST: inv.Owned, RESTVerified: true}
	idle = inv.Owned == inv.Idle
}
