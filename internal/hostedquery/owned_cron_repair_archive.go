package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// PrepareOwnedCronPeriodArchive creates a private worker-state archive. The
// fixture observer needs an authorized state connection, never a customer API.
func PrepareOwnedCronPeriodArchive(ctx context.Context, state *pgx.Conn) error {
	_, err := state.Exec(ctx, `CREATE TABLE IF NOT EXISTS pooltune_worker.owned_cron_periods (
		project_id text NOT NULL, database_oid oid NOT NULL, target_job_id bigint NOT NULL,
		role_oid oid NOT NULL, query_id bigint NOT NULL, cycle timestamptz NOT NULL,
		payload jsonb NOT NULL,
		PRIMARY KEY (project_id,database_oid,target_job_id,role_oid,query_id,cycle)
	)`)
	return err
}

// ReadOwnedCronPeriods returns one three-cycle group for the exact enrolled
// workload. It leaves missing periods missing.
func ReadOwnedCronPeriods(ctx context.Context, state *pgx.Conn, req OwnedCronRequest, first time.Time) ([]CronPeriod, error) {
	if first.IsZero() || (!first.Equal(req.FirstCycle) && !first.Equal(req.FirstCycle.Add(6*time.Minute))) {
		return nil, errors.New("Cron period archive read is outside the qualified cycles")
	}
	rows, err := state.Query(ctx, `SELECT cycle,payload FROM pooltune_worker.owned_cron_periods
		WHERE project_id=$1 AND database_oid=$2 AND target_job_id=$3 AND role_oid=$4 AND query_id=$5
		  AND cycle >= $6 AND cycle < $7 ORDER BY cycle LIMIT 4`,
		req.Target.Project, req.Target.DatabaseOID, req.Target.JobID, req.Observed.Role, req.Observed.Query, first, first.Add(6*time.Minute))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (CronPeriod, error) {
		var cycle time.Time
		var raw []byte
		var period CronPeriod
		if err := row.Scan(&cycle, &raw); err != nil {
			return period, err
		}
		if err := json.Unmarshal(raw, &period); err != nil {
			return period, err
		}
		if !cycle.Equal(period.Cycle) {
			return period, errors.New("stored Cron period did not match its archive identity")
		}
		return period, nil
	})
}

// RecordOwnedCronPeriod accepts one observed period from the trusted fixture
// observer. The scheduler history, protected query identities and exact fixture
// are independently checked before the archive accepts it.
func RecordOwnedCronPeriod(ctx context.Context, cfg *pgx.ConnConfig, state *pgx.Conn, req OwnedCronRequest, period CronPeriod) error {
	if state == nil || !validOwnedCronRoute(cfg, req) || period.Cycle.Before(req.FirstCycle) ||
		!period.Cycle.Before(req.FirstCycle.Add(12*time.Minute)) || !period.Cycle.Equal(cronCycle(period.Cycle)) {
		return errors.New("Cron period is outside the authorized Rookery fixture")
	}
	original := period.Cycle.Before(req.FirstCycle.Add(6 * time.Minute))
	if err := validateCronPeriod(period, original); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg.Copy())
	if err != nil {
		return err
	}
	defer closeVacuumConnection(conn)
	provider := newOwnedCronProvider(req)
	if err := provider.check(ctx, conn); err != nil {
		return err
	}
	now, generation, err := provider.clock(ctx, conn)
	if err != nil {
		return err
	}
	if !generation.Postmaster.Equal(req.Observed.Boot) || !now.Before(req.Expires) ||
		now.Before(period.Quiet.End) || now.Sub(period.Quiet.End) > 15*time.Second {
		return errors.New("Cron recorder lost the observed period, fixture lifetime or database generation")
	}
	if !ownedCronWindowMatches(req, period) {
		return errors.New("Cron recorder received a different workload")
	}
	if _, err := readOwnedCronScope(ctx, conn, req, &period.Busy); err != nil {
		return err
	}
	current, err := ReadProjectCron(ctx, conn, req.Target)
	if err != nil || (original && current.Schedule != cronOriginal) || (!original && current.Schedule != cronAlternate) {
		return errors.New("Cron schedule changed before recording its period")
	}
	history, err := projectCronRuns(ctx, conn, req.Target.JobID, period.Cycle, now)
	if err != nil {
		return err
	}
	if err := validateCronCycles(req.Target.JobID, history, period.Cycle, 1, !original); err != nil {
		return err
	}
	if original {
		action := ProjectCronRequest{Target: req.Target, Window: ownedCronWindow(), Generation: generation, Periods: []CronPeriod{period}}
		if err := verifyProjectCronPeriod(action, period, period.Cycle, period.Cycle, period.Cycle.Add(time.Minute), history[0]); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(period)
	if err != nil {
		return err
	}
	return writeOwnedCronPeriod(ctx, state, req, period, raw)
}

func writeOwnedCronPeriod(ctx context.Context, state *pgx.Conn, req OwnedCronRequest, period CronPeriod, raw []byte) error {
	tag, err := state.Exec(ctx, `INSERT INTO pooltune_worker.owned_cron_periods AS recorded
		(project_id,database_oid,target_job_id,role_oid,query_id,cycle,payload)
		VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb)
		ON CONFLICT (project_id,database_oid,target_job_id,role_oid,query_id,cycle)
		DO UPDATE SET payload=excluded.payload WHERE recorded.payload=excluded.payload`,
		req.Target.Project, req.Target.DatabaseOID, req.Target.JobID, req.Observed.Role, req.Observed.Query, period.Cycle, string(raw))
	if err == nil && tag.RowsAffected() != 1 {
		return errors.New("Cron period already recorded with different observations")
	}
	return err
}
