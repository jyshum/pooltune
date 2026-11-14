package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type EnrolledLookup struct {
	Schema   string `json:"schema"`
	Table    string `json:"table"`
	Column   string `json:"column"`
	TableOID uint32 `json:"table_oid"`
	Database uint32 `json:"database_oid"`
	Role     uint32 `json:"role_oid"`
	Query    int64  `json:"query_id,string"`
}

// EstimatedLookupEvidence stays separate from the measured IndexEvidence accepted by the graph.
type EstimatedLookupEvidence struct {
	ObservedAt    time.Time     `json:"observed_at"`
	TableOID      uint32        `json:"table_oid"`
	ColumnNumber  int16         `json:"column_number"`
	CoveringIndex bool          `json:"covering_index"`
	Estimated     EstimatedPlan `json:"estimated_plan"`
}

type EstimatedPlan struct {
	Node       string  `json:"node"`
	OutputRows int64   `json:"estimated_output_rows"`
	TotalCost  float64 `json:"estimated_total_cost"`
}

// collectEnrolledLookup is the qualification path for explicitly allowed synthetic lookups.
// The caller must close its transaction promptly to release the relation lock.
func collectEnrolledLookup(ctx context.Context, tx pgx.Tx, binding EnrolledLookup, generation [3]time.Time, input int32) (*EstimatedLookupEvidence, error) {
	if binding.Schema == "" || binding.Table == "" || binding.Column == "" || binding.TableOID == 0 ||
		binding.Database == 0 || binding.Role == 0 || binding.Query == 0 ||
		generation[0].IsZero() || generation[1].IsZero() || generation[2].IsZero() {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var allowed bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
			WHERE n.nspname=$1 AND c.relname=$2 AND c.oid=$3 AND c.relkind='r'
			  AND pg_catalog.has_schema_privilege(n.oid,'USAGE')
			  AND pg_catalog.has_table_privilege(c.oid,'SELECT')
		)
		AND current_user::pg_catalog.regrole::oid=$4
		AND (SELECT oid FROM pg_catalog.pg_database WHERE datname=current_database())=$5
	`, binding.Schema, binding.Table, binding.TableOID, binding.Role, binding.Database).Scan(&allowed)
	if err != nil || !allowed {
		return nil, err
	}
	table := qualified(binding.Schema, binding.Table)
	if _, err := tx.Exec(ctx, "LOCK TABLE ONLY "+table+" IN ACCESS SHARE MODE NOWAIT"); err != nil {
		return nil, err
	}
	column := pgx.Identifier{binding.Column}.Sanitize()
	statement := "SELECT " + column + " FROM " + table + " WHERE " + column + " OPERATOR(pg_catalog.=) $1"
	var evidence EstimatedLookupEvidence
	err = tx.QueryRow(ctx, `
		SELECT a.attnum, EXISTS (
			SELECT FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class ix ON ix.oid=i.indexrelid
			JOIN pg_catalog.pg_am am ON am.oid=ix.relam
			JOIN pg_catalog.pg_opclass op ON op.oid=i.indclass[0]
			WHERE i.indrelid=c.oid AND i.indisvalid AND i.indisready AND i.indislive
			  AND i.indpred IS NULL AND i.indkey[0]=a.attnum AND am.amname='btree'
			  AND op.opcdefault AND op.opcintype='pg_catalog.int4'::pg_catalog.regtype
		)
		FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
		JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid
		WHERE c.oid=$1 AND n.nspname=$2 AND c.relname=$3 AND a.attname=$4
		  AND a.attnum>0 AND NOT a.attisdropped AND a.atttypid='pg_catalog.int4'::pg_catalog.regtype
		  AND c.relkind='r' AND c.relam=(SELECT oid FROM pg_catalog.pg_am WHERE amname='heap')
		  AND NOT c.relispartition AND NOT c.relrowsecurity AND NOT c.relhassubclass AND NOT c.relhasrules
	`, binding.TableOID, binding.Schema, binding.Table, binding.Column).Scan(&evidence.ColumnNumber, &evidence.CoveringIndex)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	matches, err := currentLookupCounters(ctx, tx, binding, generation)
	if err != nil || !matches {
		return nil, err
	}
	var raw []byte
	// Keep operator lookup pinned to PostgreSQL's built-in equality operator.
	plan := "EXPLAIN (FORMAT JSON, VERBOSE) " + statement
	if err := tx.QueryRow(ctx, plan, input).Scan(&raw); err != nil {
		return nil, err
	}
	var plans []struct {
		Query int64 `json:"Query Identifier"`
		Plan  struct {
			Node       string  `json:"Node Type"`
			OutputRows int64   `json:"Plan Rows"`
			TotalCost  float64 `json:"Total Cost"`
			Schema     string  `json:"Schema"`
			Table      string  `json:"Relation Name"`
		} `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &plans); err != nil {
		return nil, err
	}
	if len(plans) != 1 || plans[0].Query != binding.Query ||
		plans[0].Plan.Schema != binding.Schema || plans[0].Plan.Table != binding.Table {
		return nil, nil
	}
	top := plans[0].Plan
	evidence.ObservedAt, evidence.TableOID = time.Now().UTC(), binding.TableOID
	evidence.Estimated = EstimatedPlan{Node: top.Node, OutputRows: top.OutputRows, TotalCost: top.TotalCost}
	return &evidence, nil
}

func currentLookupCounters(ctx context.Context, tx pgx.Tx, binding EnrolledLookup, generation [3]time.Time) (bool, error) {
	var schema string
	err := tx.QueryRow(ctx, `
		SELECT n.nspname FROM pg_catalog.pg_extension e JOIN pg_catalog.pg_namespace n ON n.oid=e.extnamespace
		WHERE e.extname='pg_stat_statements'
	`).Scan(&schema)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var matches bool
	err = tx.QueryRow(ctx, fmt.Sprintf(`
		SELECT EXISTS (
			SELECT FROM %s(false) s CROSS JOIN %s i
			WHERE s.toplevel AND s.dbid=$1 AND s.userid=$2 AND s.queryid=$3
			  AND pg_catalog.pg_postmaster_start_time()=$4 AND i.stats_reset=$5 AND s.stats_since=$6)
	`, qualified(schema, "pg_stat_statements"), qualified(schema, "pg_stat_statements_info")),
		binding.Database, binding.Role, binding.Query, generation[0], generation[1], generation[2]).Scan(&matches)
	return matches, err
}

// LookupEnrollment binds an operator-selected query and representative value to one job.
type LookupEnrollment struct {
	Project string         `json:"project"`
	Lookup  EnrolledLookup `json:"lookup"`
	Input   *int32         `json:"input"`
}

func (e LookupEnrollment) Validate() error {
	b := e.Lookup
	if e.Project == "" || len(e.Project) > 128 || e.Input == nil ||
		b.Schema == "" || b.Table == "" || b.Column == "" || b.TableOID == 0 || b.Database == 0 || b.Role == 0 || b.Query == 0 {
		return errors.New("incomplete enrolled lookup")
	}
	return nil
}

func EnrolledEstimate(config *pgx.ConnConfig, enrollment LookupEnrollment) func(context.Context, InvestigationJob) (*EstimatedLookupEvidence, error) {
	return func(ctx context.Context, job InvestigationJob) (*EstimatedLookupEvidence, error) {
		b := enrollment.Lookup
		if job.Database != b.Database || job.Role != b.Role || job.Query != b.Query || len(job.Generation) != 3 {
			return nil, nil
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		target, err := pgx.ConnectConfig(ctx, config)
		if err != nil {
			return nil, err
		}
		defer func() {
			cleanup, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			target.Close(cleanup)
		}()
		var result *EstimatedLookupEvidence
		err = pgx.BeginTxFunc(ctx, target, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SET LOCAL search_path=pg_catalog;
				SET LOCAL statement_timeout='1500ms';
				SET LOCAL idle_in_transaction_session_timeout='2500ms'`); err != nil {
				return err
			}
			var err error
			result, err = collectEnrolledLookup(ctx, tx, b, [3]time.Time(job.Generation), *enrollment.Input)
			return err
		})
		return result, err
	}
}
