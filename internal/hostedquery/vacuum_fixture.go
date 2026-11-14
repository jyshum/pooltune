package hostedquery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

func vacuumQuery(schema string) string {
	return "SELECT sum(lookup) FROM ONLY " + qualified(schema, "candidate") + " WHERE lookup >= $1 AND lookup <= $2"
}

func seedVacuumFixture(ctx context.Context, target *pgx.Conn, schema string) error {
	err := pgx.BeginFunc(ctx, target, func(tx pgx.Tx) error {
		table := qualified(schema, "candidate")
		for _, sql := range []string{"SET LOCAL statement_timeout='5s'", "SET LOCAL lock_timeout='250ms'",
			"ALTER TABLE " + table + " SET (autovacuum_enabled=false)", "TRUNCATE " + table,
			fmt.Sprintf("INSERT INTO %s SELECT n,n,repeat('p',64) FROM generate_series(1,%d) n ORDER BY (n::bigint*7919) %% 100003", table, fixtureRows),
			"CREATE INDEX " + pgx.Identifier{trialIndex}.Sanitize() + " ON " + table + " (lookup)", "ANALYZE " + table} {
			if _, err := tx.Exec(ctx, sql); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return boundedVacuum(ctx, target, schema)
}

func boundedVacuum(ctx context.Context, target *pgx.Conn, schema string) error {
	// Settings and VACUUM are separate protocol commands: VACUUM cannot run inside a transaction.
	if _, err := target.Exec(ctx, "SET search_path=pg_catalog; SET statement_timeout='2s'; SET lock_timeout='250ms'; SET maintenance_work_mem='16MB'"); err != nil {
		return err
	}
	_, err := target.Exec(ctx, "VACUUM (ANALYZE FALSE, FULL FALSE, TRUNCATE FALSE, PARALLEL 0) "+qualified(schema, "candidate"))
	return err
}

func validateVacuumFixture(ctx context.Context, tx trialReader, id string, expected fixtureObjects, indexOID uint32) error {
	if err := validateSampleFixture(ctx, tx, id, expected); err != nil {
		return err
	}
	index, err := inspectTrialIndex(ctx, tx, trialSchema(id))
	if err != nil {
		return err
	}
	var current uint32
	var unchanged bool
	err = tx.QueryRow(ctx, "SELECT oid,reloptions IS NULL FROM pg_class WHERE oid=to_regclass($1)", qualified(trialSchema(id), trialIndex)).Scan(&current, &unchanged)
	if err != nil {
		return err
	}
	if current != indexOID || indexOID == 0 || !unchanged || !index.Valid || !index.Ready || !index.Matches || index.Bytes > maxTrialIndexBytes {
		return errors.New("vacuum fixture index identity or definition changed")
	}
	return nil
}

// Missing visibility into activity fails closed. Short ordinary traffic is permitted;
// snapshots older than five seconds, prepared work and replication horizons defer cleanup.
func vacuumConditions(ctx context.Context, target *pgx.Conn, table uint32, guardPID uint32) (bool, error) {
	var clear bool
	err := target.QueryRow(ctx, `SELECT
  NOT EXISTS(SELECT FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND pid<>$2 AND backend_type='client backend' AND (state IS NULL OR ((backend_xid IS NOT NULL OR backend_xmin IS NOT NULL) AND xact_start<clock_timestamp()-interval '5 seconds')))
  AND NOT EXISTS(SELECT FROM pg_stat_progress_vacuum WHERE relid=$1)
  AND NOT EXISTS(SELECT FROM pg_stat_progress_cluster WHERE relid=$1)
  AND NOT EXISTS(SELECT FROM pg_locks WHERE relation=$1 AND pid IS DISTINCT FROM pg_backend_pid() AND pid IS DISTINCT FROM $2::int AND mode NOT IN ('AccessShareLock','RowShareLock','RowExclusiveLock'))
  AND NOT EXISTS(SELECT FROM pg_prepared_xacts WHERE database=current_database())
  AND NOT EXISTS(SELECT FROM pg_replication_slots WHERE xmin IS NOT NULL OR catalog_xmin IS NOT NULL)
 `, table, guardPID).Scan(&clear)
	return clear, err
}

func applyFixtureVacuum(ctx context.Context, target *pgx.Conn, config *pgx.ConnConfig, id string, objects fixtureObjects, index uint32) (bool, error) {
	guard, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return false, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		guard.Close(cleanup)
	}()
	tx, err := guard.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		tx.Rollback(cleanup)
	}()
	if _, err := tx.Exec(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL statement_timeout='750ms'; SET LOCAL idle_in_transaction_session_timeout='5s'"); err != nil {
		return false, err
	}
	schema := trialSchema(id)
	if _, err := tx.Exec(ctx, "LOCK TABLE ONLY "+qualified(schema, "candidate")+", ONLY "+qualified(schema, "healthy")+" IN ACCESS SHARE MODE NOWAIT"); err != nil {
		return false, err
	}
	if err := validateVacuumFixture(ctx, tx, id, objects, index); err != nil {
		return false, err
	}
	clear, err := vacuumConditions(ctx, target, objects.Candidate, guard.PgConn().PID())
	if err != nil || !clear {
		return false, err
	}
	if _, err := target.Exec(ctx, "SET max_parallel_workers_per_gather=0; SET random_page_cost=1; SET statement_timeout='750ms'"); err != nil {
		return false, err
	}
	// Re-measure the specific cause after the model. Another vacuum may already have restored visibility.
	plan, err := scanTrialPlan(target.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, TIMING OFF) "+vacuumQuery(schema), int32(1), int32(50000)))
	if err != nil {
		return false, err
	}
	node, err := vacuumScan(plan, "")
	if err != nil {
		return false, err
	}
	if *node.HeapFetches < 1000 || float64(*node.HeapFetches)/float64(node.Rows) < .5 {
		return false, nil
	}
	if err := validateVacuumFixture(ctx, tx, id, objects, index); err != nil {
		return false, err
	}
	clear, err = vacuumConditions(ctx, target, objects.Candidate, guard.PgConn().PID())
	if err != nil || !clear {
		return false, err
	}
	if err := boundedVacuum(ctx, target, schema); err != nil {
		return false, err
	}
	return true, nil
}

// Startup owns the same target lock as preparation. Only unfinished preparation
// is eligible; ready, pending, verified and declined fixtures remain untouched.
func recoverVacuumPreparations(ctx context.Context, target, state *pgx.Conn, route string) (err error) {
	if err := prepareJournal(ctx, state); err != nil {
		return err
	}
	if err := lockTrial(ctx, target); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, releaseTrialLock(target)) }()
	return reconcileTrialStatuses(ctx, target, state, journalTarget(target, route), []string{"vacuum_preparing"})
}
