package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"
)

// The parent finish and child insertion share the project lock and transaction.
// A failed or lost acknowledgement can be discovered by the unique parent link.
func continueInvestigation(ctx context.Context, state *pgx.Conn, claim investigationClaim, result DispatchResult, notBefore time.Time) (child int64, err error) {
	if notBefore.IsZero() || (result.Outcome != "awaiting_evidence" && result.Outcome != "awaiting_verification") {
		return 0, errors.New("invalid delayed continuation")
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return 0, err
	}
	err = pgx.BeginTxFunc(ctx, state, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		var project string
		if e := tx.QueryRow(ctx, "SELECT project_id FROM pooltune_worker.projects WHERE project_id=$1 FOR UPDATE", claim.Project).Scan(&project); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE pooltune_worker.investigations SET finished_at=clock_timestamp(),lease_until=NULL,result=$4::jsonb WHERE id=$1 AND project_id=$2 AND attempts=$3 AND finished_at IS NULL AND lease_until>clock_timestamp()`, claim.ID, claim.Project, claim.Attempt, string(payload))
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return errClaimLost
		}
		return tx.QueryRow(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence,parent_id,not_before) SELECT project_id,database_oid,role_oid,query_id,evidence,id,$2 FROM pooltune_worker.investigations WHERE id=$1 RETURNING id`, claim.ID, notBefore).Scan(&child)
	})
	return
}
