package hostedquery

import (
	"context"
	"errors"
	"time"

	"pooltune/internal/labdb"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Both the dashboard and the write path select from the same durable evidence.
// Historical results lack the original object receipt and are ineligible.
const customerIndexChangeSQL = `
    a.project_id=j.project_id AND a.job_id=j.id AND j.finished_at IS NOT NULL
    AND NOT EXISTS (SELECT FROM pooltune_worker.projects p WHERE p.project_id=j.project_id AND p.last_disconnected_at>=j.created_at)
    AND a.outcome='improved_tested_inputs' AND j.result->>'outcome'=a.outcome
    AND j.result#>>'{repair,measurement_scope}'='managed_sampled_input'
    AND j.result#>>'{repair,outcome}'=a.outcome
    AND j.result#>'{repair,index_valid}'='true'::jsonb
    AND NOT coalesce(j.result#>'{repair,reverted}'='true'::jsonb,false)
    AND a.intent->>'RunID'=j.result#>>'{repair,run_id}'
    AND a.intent->>'RunID'='managed-index-' || j.id::text || '-' || j.attempts::text
    AND NOT (a.intent ? 'RollbackRequested' OR a.intent ? 'AppliedIndexOID')
    AND j.result#>>'{repair,created_index_oid}' ~ '^[1-9][0-9]{0,9}$'
    AND j.result#>>'{repair,created_index_version}' ~ '^[0-9a-f]{64}$'
`

// pg_index.xmin distinguishes a catalog generation; the definition, physical
// identity, ownership and settings detect independent alterations. Both uses
// execute with search_path=pg_catalog for identical rendered definitions.
const managedIndexVersionSQL = `
    SELECT encode(sha256(convert_to(jsonb_build_array(i.xmin::text,i.indrelid,
        c.oid,c.relnamespace,c.relowner,c.reltablespace,c.relfilenode,c.reloptions,
        pg_get_indexdef(c.oid),obj_description(c.oid,'pg_class'),i.indisclustered,i.indisreplident)::text,'UTF8')),'hex')
    FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class c ON c.oid=i.indexrelid
    WHERE c.oid=$1`

// The caller holds the owner/installation row for the whole operation and
// supplies a fresh, verified customer connection. State and customer DBs do
// not share a transaction. Save the native transaction ID independently before
// committing the DROP, so a retry can distinguish an aborted DROP from one
// that already committed, without inferring success from an absent name.
func RevertManagedProjectIndex(ctx context.Context, fence pgx.Tx, journal *pgxpool.Pool, project string, epoch, job int64, connect func() (context.Context, *pgx.Conn, error)) error {
	var locked string
	if err := fence.QueryRow(ctx, `SELECT project_id FROM pooltune_worker.projects WHERE project_id=$1 AND monitor_epoch=CASE WHEN agent_paused THEN -$2::bigint ELSE $2::bigint END AND $2::bigint>0 FOR UPDATE NOWAIT`, project, epoch).Scan(&locked); err != nil {
		return err
	}
	claim := investigationClaim{InvestigationJob: InvestigationJob{ID: job}, Project: project}
	var intent ProjectIndexActionIntent
	var xid *string
	var reverted *time.Time
	var version string
	var oid uint32
	err := fence.QueryRow(ctx, `SELECT j.database_oid,a.intent,(j.result#>>'{repair,created_index_oid}')::oid,
		j.result#>>'{repair,created_index_version}',a.customer_revert_xid,a.customer_reverted_at
		FROM pooltune_worker.investigations j JOIN pooltune_worker.owned_index_actions a
		ON a.job_id=j.id WHERE j.id=$1 AND j.project_id=$2 AND `+customerIndexChangeSQL, job, project).
		Scan(&claim.Database, &intent, &oid, &version, &xid, &reverted)
	if err != nil || reverted != nil {
		return err
	}
	bound, target, err := connect()
	if err != nil {
		return err
	}
	defer labdb.Close(target)
	committed := false
	if xid != nil {
		var status *string
		if err := target.QueryRow(bound, `SELECT pg_catalog.pg_xact_status($1::xid8)`, *xid).Scan(&status); err != nil {
			return err
		}
		if status == nil || (*status != "committed" && *status != "aborted") {
			return errors.New("customer revert transaction is unresolved")
		}
		committed = *status == "committed"
		if committed {
			var removed bool
			if err := target.QueryRow(bound, `SELECT EXISTS(SELECT FROM pg_catalog.pg_database WHERE datname=current_database() AND oid=$1) AND NOT EXISTS(SELECT FROM pg_catalog.pg_class WHERE oid=$2)`, claim.Database, oid).Scan(&removed); err != nil {
				return err
			}
			if !removed {
				return ErrProjectIndexTargetChanged
			}
		}
	}
	if !committed {
		intent.AppliedIndexOID, intent.RollbackRequested = oid, true
		err := rollbackManagedIndexWithReceipt(bound, target, claim, intent, version, func(locked context.Context, next string) error {
			tag, err := journal.Exec(locked, `UPDATE pooltune_worker.owned_index_actions SET customer_revert_xid=$3
				WHERE project_id=$1 AND job_id=$2 AND customer_revert_xid IS NOT DISTINCT FROM $4 AND customer_reverted_at IS NULL`, project, job, next, xid)
			if err == nil && tag.RowsAffected() != 1 {
				return errClaimLost
			}
			return err
		})
		if err != nil {
			return err
		}
	}
	tag, err := fence.Exec(ctx, `UPDATE pooltune_worker.owned_index_actions SET customer_reverted_at=statement_timestamp()
		WHERE project_id=$1 AND job_id=$2 AND customer_revert_xid IS NOT NULL AND customer_reverted_at IS NULL`, project, job)
	if err == nil && tag.RowsAffected() != 1 {
		return errClaimLost
	}
	return err
}
