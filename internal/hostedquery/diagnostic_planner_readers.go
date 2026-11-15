package hostedquery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// AnalyzeDiagnosticEvidence reads only the enrolled stale-statistics fixture.
// The model chooses a named observation, never SQL, objects or query parameters.
func AnalyzeDiagnosticEvidence(state *pgx.Conn, config *pgx.ConnConfig, route, fixture string) DiagnosticEvidenceReader {
	return plannerDiagnosticEvidence(state, config, route, fixture, false)
}

// StatisticsDiagnosticEvidence measures the two enrolled correlated predicates.
// It does not run ANALYZE or the baseline sampler's protected write experiments.
func StatisticsDiagnosticEvidence(state *pgx.Conn, config *pgx.ConnConfig, route, fixture string) DiagnosticEvidenceReader {
	return plannerDiagnosticEvidence(state, config, route, fixture, true)
}

func plannerDiagnosticEvidence(state *pgx.Conn, config *pgx.ConnConfig, route, fixture string, statistics bool) DiagnosticEvidenceReader {
	return func(ctx context.Context, tool string, base IndexEvidence) (out IndexEvidence, available bool, err error) {
		switch tool {
		case "plan", "indexes", "analyze":
		case "statistics":
			if !statistics {
				return out, false, nil
			}
		case "vacuum", "memory", "cron":
			return out, false, nil
		default:
			return out, false, errors.New("unknown diagnostic tool")
		}
		age := time.Since(base.At)
		if ValidateConsumerFixtureID(fixture) != nil || (route != "local" && route != "rookery") || state == nil || config == nil || base.RunID == "" || base.TableOID == 0 || base.Column != 2 || len(base.Columns) != 0 || base.Decision != Investigate || base.At.IsZero() || age < 0 || age > 30*time.Second {
			return out, false, errors.New("invalid planner diagnostic enrollment")
		}
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		status := "analyze_ready"
		if statistics {
			status = "statistics_ready"
		}
		var ready bool
		if err = state.QueryRow(ctx, "SELECT EXISTS(SELECT FROM "+journalTable+" WHERE id=$1 AND target=$2 AND status=$3)", fixture, journalTargetConfig(config, route), status).Scan(&ready); err != nil {
			return out, false, err
		}
		if !ready {
			return out, false, errors.New("planner diagnostic fixture is not enrolled for inspection")
		}
		expected, err := expectedFixture(ctx, state, fixture)
		if err != nil {
			return out, false, err
		}
		if expected.Candidate != base.TableOID {
			return out, false, errors.New("planner diagnostic target changed")
		}
		target, err := pgx.ConnectConfig(ctx, config)
		if err != nil {
			return out, false, err
		}
		defer func() {
			closeCtx, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			target.Close(closeCtx)
		}()
		err = pgx.BeginTxFunc(ctx, target, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
			// Match the qualified fixture's planner settings, with a smaller inspection
			// budget. Locking protects catalog identity for the measured transaction.
			if _, err := tx.Exec(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL statement_timeout='750ms'; SET LOCAL lock_timeout='250ms'; SET LOCAL max_parallel_workers_per_gather=0"); err != nil {
				return err
			}
			for _, table := range []string{"candidate", "healthy"} {
				if _, err := tx.Exec(ctx, "LOCK TABLE ONLY "+qualified(trialSchema(fixture), table)+" IN ACCESS SHARE MODE NOWAIT"); err != nil {
					return err
				}
			}
			if statistics {
				if _, err := tx.Exec(ctx, "SET LOCAL enable_bitmapscan=off; SET LOCAL random_page_cost=5"); err != nil {
					return err
				}
				if err := validateStatisticsFixture(ctx, tx, fixture, expected, false); err != nil {
					return err
				}
			} else {
				if err := validateSampleFixture(ctx, tx, fixture, expected); err != nil {
					return err
				}
				index, err := inspectTrialIndex(ctx, tx, trialSchema(fixture))
				if err != nil {
					return err
				}
				if !index.Valid || !index.Ready || !index.Matches || index.Bytes > maxTrialIndexBytes {
					return errors.New("expected the existing fixture lookup index")
				}
			}
			catalog, err := collectIndexCatalog(ctx, tx, fixture)
			if err != nil {
				return err
			}
			if catalog.TableOID != base.TableOID || catalog.Column != base.Column || !catalog.Existing {
				return errors.New("planner diagnostic catalog identity changed")
			}
			out = catalog
			out.RunID = base.RunID
			out.Decision = base.Decision
			if tool != "indexes" {
				schema := trialSchema(fixture)
				inputs := []statisticsInput{{Lookup: analyzeInput, FirstID: analyzeInput, Rows: 1}}
				if statistics {
					inputs = []statisticsInput{statisticsCases[0]}
					if tool == "statistics" {
						inputs = statisticsCases[:]
					}
				}
				var cases []AnalyzeEvidence
				for i, input := range inputs {
					query := analyzeCandidate(schema)
					args := []any{input.Lookup}
					if statistics {
						query = statisticsLookup(schema)
						args = append(args, input.Padding)
					}
					measured, err := readLookupPlan(ctx, tx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, VERBOSE, TIMING OFF) "+query, args...)
					if err != nil {
						return err
					}
					p := measured.Node
					if p.Schema != schema || p.Relation != "candidate" || p.Loops != 1 || p.Rows != int64(input.Rows) || p.EstRows < 1 {
						return errors.New("planner diagnostic predicate changed")
					}
					if i == 0 {
						out.Plan = PlanEvidence{Node: p.Type, ExaminedRows: p.Rows + p.Filtered, ReturnedRows: p.Rows, SharedBlocks: p.Hit + p.Read}
					}
					cases = append(cases, AnalyzeEvidence{EstimatedRows: p.EstRows, ActualRows: p.Rows})
				}
				if tool == "analyze" {
					out.Analyze = &cases[0]
					// A singleton recorded frequency can prove the old distribution for this
					// exact value. A multi-valued statistic is not treated as stale evidence.
					if err := tx.QueryRow(ctx, `SELECT (SELECT most_common_freqs[1]::float8 FROM pg_catalog.pg_stats WHERE schemaname=$1 AND tablename='candidate' AND attname='lookup' AND NOT inherited AND array_length(most_common_freqs,1)=1 AND most_common_vals::text=$2)`, schema, fmt.Sprintf("{%d}", inputs[0].Lookup)).Scan(&out.Analyze.SampledValueFraction); err != nil {
						return err
					}
				}
				if tool == "statistics" {
					correlated, err := currentCorrelation(ctx, tx, qualified(schema, "candidate"), expected.Candidate)
					if err != nil {
						return err
					}
					out.Statistics = &StatisticsEvidence{Correlated: correlated, Cases: cases}
				}
			}
			out.At = time.Now().UTC()
			return nil
		})
		return out, err == nil, err
	}
}
