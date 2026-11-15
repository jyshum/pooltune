package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"time"
)

// DiagnosticTrace records the bounded investigator's work, not execution authority.
type DiagnosticTrace struct {
	ModelAttempts int               `json:"model_attempts"`
	ModelCalls    int               `json:"model_calls"`
	Reads         int               `json:"reads"`
	ModelMS       float64           `json:"model_ms"`
	ToolMS        float64           `json:"tool_ms"`
	TotalMS       float64           `json:"total_ms"`
	StopReason    string            `json:"stop_reason"`
	History       []json.RawMessage `json:"history"`
}

// DiagnosticEvidenceReader accepts a tool name and an already enrolled identity.
// False means unavailable, never an observed healthy measurement.
type DiagnosticEvidenceReader func(context.Context, string, IndexEvidence) (IndexEvidence, bool, error)

// FixtureDiagnosticEvidence qualifies only the existing synthetic index fixture.
// No model-provided SQL, object name, query parameter or connection is accepted.
func FixtureDiagnosticEvidence(state *pgx.Conn, config *pgx.ConnConfig, route, fixture string) DiagnosticEvidenceReader {
	return func(ctx context.Context, tool string, base IndexEvidence) (out IndexEvidence, available bool, err error) {
		switch tool {
		case "statistics", "vacuum", "memory", "cron":
			return out, false, nil
		case "plan", "indexes", "analyze":
		default:
			return out, false, errors.New("unknown diagnostic tool")
		}
		if ValidateConsumerFixtureID(fixture) != nil || (route != "local" && route != "rookery") || state == nil || config == nil || base.RunID == "" || base.TableOID == 0 || base.Column != 2 || base.Decision != Investigate || base.At.IsZero() || time.Since(base.At) < 0 || time.Since(base.At) > 30*time.Second {
			return out, false, errors.New("invalid diagnostic fixture enrollment")
		}
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		var ready bool
		if err = state.QueryRow(ctx, "SELECT EXISTS(SELECT FROM "+journalTable+" WHERE id=$1 AND target=$2 AND status='consumer_ready')", fixture, journalTargetConfig(config, route)).Scan(&ready); err != nil {
			return out, false, err
		}
		if !ready {
			return out, false, errors.New("diagnostic fixture is not enrolled for inspection")
		}
		expected, err := expectedFixture(ctx, state, fixture)
		if err != nil {
			return out, false, err
		}
		if expected.Candidate != base.TableOID {
			return out, false, errors.New("diagnostic target changed")
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
			if _, err := tx.Exec(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL statement_timeout='750ms'; SET LOCAL lock_timeout='250ms'; SET LOCAL max_parallel_workers_per_gather=0"); err != nil {
				return err
			}
			if err := lockSampleTables(ctx, tx, trialSchema(fixture)); err != nil {
				return err
			}
			if err := validateSampleFixture(ctx, tx, fixture, expected); err != nil {
				return err
			}
			catalog, err := collectIndexCatalog(ctx, tx, fixture)
			if err != nil {
				return err
			}
			if catalog.TableOID != base.TableOID || catalog.Column != base.Column {
				return errors.New("diagnostic catalog identity changed")
			}
			out = catalog
			out.RunID = base.RunID
			out.Decision = base.Decision
			if tool != "indexes" {
				plan, err := readPlan(ctx, tx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, VERBOSE, TIMING OFF) SELECT id FROM "+qualified(trialSchema(fixture), "candidate")+" WHERE lookup=$1")
				if err != nil {
					return err
				}
				p := plan.Node
				if p.Schema != trialSchema(fixture) || p.Relation != "candidate" || p.Loops != 1 || p.Rows != 1 {
					return errors.New("diagnostic lookup plan changed")
				}
				out.Plan = PlanEvidence{Node: p.Type, ExaminedRows: p.Rows + p.Filtered, ReturnedRows: p.Rows, SharedBlocks: p.Hit + p.Read}
				if tool == "analyze" {
					out.Analyze = &AnalyzeEvidence{EstimatedRows: p.EstRows, ActualRows: p.Rows}
					if err = tx.QueryRow(ctx, `SELECT (SELECT most_common_freqs[1]::float8 FROM pg_catalog.pg_stats WHERE schemaname=$1 AND tablename='candidate' AND attname='lookup' AND NOT inherited AND array_length(most_common_freqs,1)=1 AND most_common_vals::text=$2)`, trialSchema(fixture), fmt.Sprintf("{%d}", inputIDs[0])).Scan(&out.Analyze.SampledValueFraction); err != nil {
						return err
					}
				}
			}
			out.At = time.Now().UTC()
			return nil
		})
		return out, err == nil, err
	}
}
