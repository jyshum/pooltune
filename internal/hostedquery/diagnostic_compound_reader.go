package hostedquery

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"
)

// CompoundDiagnosticEvidence reads only the enrolled two-column fixture. The
// model chooses a named observation, never SQL, identifiers or parameter values.
func CompoundDiagnosticEvidence(state *pgx.Conn, config *pgx.ConnConfig, route, fixture string) DiagnosticEvidenceReader {
	return func(ctx context.Context, tool string, base IndexEvidence) (out IndexEvidence, ok bool, err error) {
		switch tool {
		case "plan", "indexes":
		case "analyze", "statistics", "vacuum", "memory", "cron":
			return out, false, nil
		default:
			return out, false, errors.New("unknown diagnostic tool")
		}
		if ValidateConsumerFixtureID(fixture) != nil || (route != "local" && route != "rookery") || state == nil || config == nil || base.RunID == "" || base.TableOID == 0 || len(base.Columns) != 2 || !ValidIndexColumns(base.Column, base.Columns) || base.Decision != Investigate || base.At.IsZero() || time.Since(base.At) < 0 || time.Since(base.At) > 30*time.Second {
			return out, false, errors.New("invalid compound enrollment")
		}
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		var ready bool
		if err = state.QueryRow(ctx, "SELECT EXISTS(SELECT FROM "+journalTable+" WHERE id=$1 AND target=$2 AND status='compound_ready' AND index_definition=$3)", fixture, journalTargetConfig(config, route), compoundDefinition).Scan(&ready); err != nil {
			return out, false, err
		}
		if !ready {
			return out, false, errors.New("compound fixture is not enrolled")
		}
		expected, err := expectedFixture(ctx, state, fixture)
		if err != nil {
			return out, false, err
		}
		if expected.Candidate != base.TableOID {
			return out, false, errors.New("compound target changed")
		}
		target, err := pgx.ConnectConfig(ctx, config)
		if err != nil {
			return out, false, err
		}
		defer closeVacuumConnection(target)
		err = pgx.BeginTxFunc(ctx, target, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL statement_timeout='750ms'; SET LOCAL lock_timeout='250ms'; SET LOCAL max_parallel_workers_per_gather=0"); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "LOCK TABLE ONLY "+qualified(trialSchema(fixture), "candidate")+", ONLY "+qualified(trialSchema(fixture), "healthy")+" IN ACCESS SHARE MODE NOWAIT"); err != nil {
				return err
			}
			if err := validateSampleFixture(ctx, tx, fixture, expected); err != nil {
				return err
			}
			var err error
			out, err = collectCompoundCatalog(ctx, tx, fixture)
			if err != nil {
				return err
			}
			if out.TableOID != base.TableOID {
				return errors.New("compound catalog identity changed")
			}
			out.RunID, out.Decision = base.RunID, base.Decision
			if tool == "plan" {
				plan, err := readLookupPlan(ctx, tx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, VERBOSE, TIMING OFF) "+compoundQuery(trialSchema(fixture)), indexReadArgs(inputIDs[0], true)...)
				if err != nil {
					return err
				}
				p := plan.Node
				if p.Schema != trialSchema(fixture) || p.Relation != "candidate" || p.Rows != 1 || p.Loops != 1 {
					return errors.New("compound query shape changed")
				}
				out.Plan = PlanEvidence{Node: p.Type, ExaminedRows: p.Rows + p.Filtered, ReturnedRows: p.Rows, SharedBlocks: p.Hit + p.Read}
			}
			out.At = time.Now().UTC()
			return nil
		})
		return out, err == nil, err
	}
}
