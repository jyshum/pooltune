package hostedquery

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func trafficEvidence(ctx context.Context, tx pgx.Tx, schema string, query *labCase, enrolled Counter, job InvestigationJob) (IndexEvidence, error) {
	if job.queryKey != enrolled.key() {
		return IndexEvidence{}, errors.New("queued job does not match the enrolled lookup")
	}
	current, err := query.snapshot(ctx, tx)
	if err != nil {
		return IndexEvidence{}, err
	}
	if _, err := counterDelta(enrolled, current); err != nil {
		return IndexEvidence{}, err
	}
	plan, err := readPlan(ctx, tx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, VERBOSE, TIMING OFF) "+query.sql)
	if err != nil {
		return IndexEvidence{}, err
	}
	if plan.QueryID != job.Query || plan.Node.Loops != 1 || plan.Node.Rows != 1 {
		return IndexEvidence{}, errors.New("measured plan does not match the queued lookup")
	}
	evidence := IndexEvidence{
		At: time.Now().UTC(), Decision: Investigate, Label: "lookup",
		Plan: PlanEvidence{
			Node: plan.Node.Type, ExaminedRows: plan.Node.Rows + plan.Node.Filtered,
			ReturnedRows: plan.Node.Rows, SharedBlocks: plan.Node.Hit + plan.Node.Read,
		},
	}
	err = tx.QueryRow(ctx, `
		SELECT c.oid,a.attnum, EXISTS (
			SELECT FROM pg_index i JOIN pg_class ix ON ix.oid=i.indexrelid
			JOIN pg_am am ON am.oid=ix.relam
			WHERE i.indrelid=c.oid AND i.indisvalid AND i.indisready AND i.indpred IS NULL
			  AND i.indkey[0]=a.attnum AND am.amname='btree'
		)
		FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		JOIN pg_attribute a ON a.attrelid=c.oid
		WHERE n.nspname=$1 AND c.relname='candidate' AND c.relkind='r'
		  AND c.relowner=current_user::regrole::oid AND a.attname='lookup'
		  AND a.attnum>0 AND NOT a.attisdropped
	`, schema).Scan(&evidence.TableOID, &evidence.Column, &evidence.Existing)
	return evidence, err
}
