package hostedquery

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ValidateConsumerFixtureID accepts only the canonical identifier used by the fixture journal.
func ValidateConsumerFixtureID(fixture string) error {
	raw, err := hex.DecodeString(fixture)
	if err != nil || len(raw) != 12 || hex.EncodeToString(raw) != fixture {
		return errors.New("expected a journaled fixture ID")
	}
	return nil
}

// FixtureEvidence only measures the lookup from an explicitly enrolled, journaled
// test fixture. A fresh target session exists only during evidence collection.
func FixtureEvidence(state *pgx.Conn, config *pgx.ConnConfig, route, fixture string) InvestigationEvidence {
	return func(ctx context.Context, job InvestigationJob) (IndexEvidence, error) {
		if len(job.Generation) != 3 {
			return IndexEvidence{}, nil
		}
		ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		var bound bool
		err := state.QueryRow(ctx, "SELECT EXISTS (SELECT FROM "+journalTable+" WHERE id=$1 AND target=$2 AND status='consumer_ready')",
			fixture, journalTargetConfig(config, route)).Scan(&bound)
		if err != nil || !bound {
			return IndexEvidence{}, err
		}
		expected, err := expectedFixture(ctx, state, fixture)
		if err != nil {
			return IndexEvidence{}, err
		}
		target, err := pgx.ConnectConfig(ctx, config)
		if err != nil {
			return IndexEvidence{}, err
		}
		defer func() {
			closeCtx, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			target.Close(closeCtx)
		}()
		var evidence IndexEvidence
		err = pgx.BeginTxFunc(ctx, target, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `
				SET LOCAL search_path=pg_catalog;
				SET LOCAL statement_timeout='750ms';
				SET LOCAL lock_timeout='250ms';
				SET LOCAL max_parallel_workers_per_gather=0
			`); err != nil {
				return err
			}
			schema := trialSchema(fixture)
			if _, err := tx.Exec(ctx, "LOCK TABLE "+qualified(schema, "candidate")+", "+qualified(schema, "healthy")+" IN ACCESS SHARE MODE"); err != nil {
				return err
			}
			actual, err := loadFixture(ctx, tx, fixture)
			if err != nil {
				return err
			}
			if actual != expected {
				return nil
			}
			var supported bool
			if err := tx.QueryRow(ctx, `
				SELECT NOT relrowsecurity AND NOT relhassubclass AND NOT relhasrules
				FROM pg_class WHERE oid=$1
			`, actual.Candidate).Scan(&supported); err != nil || !supported {
				return err
			}
			sql := "SELECT id FROM " + qualified(schema, "candidate") + " WHERE lookup=$1"
			current, err := readPlan(ctx, tx, "EXPLAIN (FORMAT JSON, VERBOSE) "+sql)
			if err != nil || current.QueryID != job.Query {
				return err
			}
			if current.Node.Type != "Seq Scan" && current.Node.Type != "Index Scan" && current.Node.Type != "Index Only Scan" && current.Node.Type != "Bitmap Heap Scan" {
				return nil
			}
			var statsSchema string
			if err := tx.QueryRow(ctx, `
				SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace
				WHERE e.extname='pg_stat_statements'
			`).Scan(&statsSchema); err != nil {
				return err
			}
			var matches bool
			err = tx.QueryRow(ctx, fmt.Sprintf(`
				SELECT EXISTS (
					SELECT FROM %s(false) AS s CROSS JOIN %s AS i
					WHERE s.toplevel AND s.dbid=$1 AND s.userid=$2 AND s.queryid=$3
					  AND s.dbid=(SELECT oid FROM pg_database WHERE datname=current_database())
					  AND s.userid=current_user::regrole::oid AND pg_postmaster_start_time()=$4
					  AND i.stats_reset=$5 AND s.stats_since=$6
				)
			`, qualified(statsSchema, "pg_stat_statements"), qualified(statsSchema, "pg_stat_statements_info")),
				job.Database, job.Role, job.Query, job.Generation[0], job.Generation[1], job.Generation[2]).Scan(&matches)
			if err != nil || !matches {
				return err
			}
			evidence, err = collectIndexCatalog(ctx, tx, fixture)
			if err != nil {
				return err
			}
			if evidence.TableOID != actual.Candidate {
				evidence = IndexEvidence{}
				return nil
			}
			measured, err := readPlan(ctx, tx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, VERBOSE, TIMING OFF) "+sql)
			if err != nil {
				return err
			}
			if measured.QueryID != job.Query || measured.Node.Loops != 1 || measured.Node.Rows != 1 {
				evidence = IndexEvidence{}
				return nil
			}
			node := measured.Node
			evidence.At, evidence.Decision = time.Now().UTC(), Investigate
			evidence.ExecutionMS = measured.ExecutionMS
			evidence.Plan = PlanEvidence{Node: node.Type, ExaminedRows: node.Rows + node.Filtered,
				ReturnedRows: node.Rows, SharedBlocks: node.Hit + node.Read}
			return nil
		})
		return evidence, err
	}
}
