package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"pooltune/internal/supabaseproject"
	"time"

	"github.com/jackc/pgx/v5"
)

// requestManagedIndexRollback durably binds reversion to the object returned by
// this action, before any destructive command. The original keys are retained.
func requestManagedIndexRollback(ctx context.Context, state *pgx.Conn, project string, epoch int64, claim investigationClaim, createdOID uint32) (intent ProjectIndexActionIntent, err error) {
	if createdOID == 0 || claim.RecoveryOnly {
		return intent, errClaimLost
	}
	err = pgx.BeginTxFunc(ctx, state, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var locked string
		if err := tx.QueryRow(ctx, `SELECT p.project_id FROM pooltune_worker.projects p JOIN pooltune_worker.investigations j USING(project_id) WHERE p.project_id=$1 AND p.monitor_epoch=$2 AND j.id=$3 AND j.attempts=$4 AND j.finished_at IS NULL AND j.lease_until>clock_timestamp() FOR UPDATE OF p`, project, epoch, claim.ID, claim.Attempt).Scan(&locked); err != nil {
			return err
		}
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT intent FROM pooltune_worker.owned_index_actions WHERE project_id=$1 AND job_id=$2 AND outcome='pending' FOR UPDATE`, project, claim.ID).Scan(&raw); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &intent); err != nil {
			return err
		}
		parts := managedIndexRunID.FindStringSubmatch(intent.RunID)
		if len(parts) != 2 || parts[1] != fmt.Sprint(claim.ID) || intent.Target.DatabaseOID != claim.Database || intent.RollbackRequested || intent.AppliedIndexOID != 0 {
			return errClaimLost
		}
		intent.RollbackRequested = true
		intent.AppliedIndexOID = createdOID
		payload, err := json.Marshal(intent)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE pooltune_worker.owned_index_actions SET intent=$3::jsonb WHERE project_id=$1 AND job_id=$2`, project, claim.ID, string(payload))
		return err
	})
	return intent, err
}

// rollbackManagedIndex only removes the exact newly created object. A short
// exclusive relation lock prevents replacement between the catalog check and
// transactional DROP. Busy tables time out and retain an unsettled journal.
func rollbackManagedIndex(ctx context.Context, db *pgx.Conn, claim investigationClaim, intent ProjectIndexActionIntent) error {
	if !intent.RollbackRequested || intent.AppliedIndexOID == 0 {
		return ErrProjectIndexTargetChanged
	}
	observed, err := inspectManagedIndexIntent(ctx, db, claim, intent)
	if err != nil {
		return err
	}
	if observed.Status != "exact_index_present" || observed.IndexOID != intent.AppliedIndexOID {
		return ErrProjectIndexTargetChanged
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return pgx.BeginTxFunc(ctx, db, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL search_path=pg_catalog; SET LOCAL lock_timeout='250ms'; SET LOCAL statement_timeout='750ms'`); err != nil {
			return err
		}
		var locked bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1,$2)`, projectIndexLockNamespace, int32(intent.Target.TableOID)).Scan(&locked); err != nil {
			return err
		}
		if !locked {
			return errOwnedIndexActionActive
		}
		if _, err := tx.Exec(ctx, "LOCK TABLE ONLY "+qualified(intent.Target.Schema, intent.Target.Relation)+" IN ACCESS EXCLUSIVE MODE"); err != nil {
			return err
		}
		var owned bool
		if err := tx.QueryRow(ctx, supabaseproject.RepairQuery(ctx, tx, `SELECT EXISTS(SELECT FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.oid=$1 AND c.relname=$2 AND n.nspname=$3 AND c.relowner=current_user::regrole::oid AND current_user=session_user AND pg_relation_size(c.oid)<=$4)`), intent.Target.TableOID, intent.Target.Relation, intent.Target.Schema, projectIndexMaxRelationBytes).Scan(&owned); err != nil {
			return err
		}
		if !owned {
			return ErrProjectIndexTargetChanged
		}
		keys := make([]int16, len(intent.Keys))
		for i, key := range intent.Keys {
			keys[i] = key.Number
		}
		var oid uint32
		var bytes int64
		var exact bool
		if err := tx.QueryRow(ctx, projectIndexReadbackSQL, intent.Target.TableOID, intent.Target.Schema, intent.IndexName, keys).Scan(&oid, &bytes, &exact); err != nil {
			return err
		}
		if !exact || oid != intent.AppliedIndexOID || bytes <= 0 || bytes > projectIndexMaxIndexBytes {
			return ErrProjectIndexTargetChanged
		}
		if _, err := tx.Exec(ctx, "DROP INDEX "+qualified(intent.Target.Schema, intent.IndexName)); err != nil {
			return err
		}
		var absent bool
		if err := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT FROM pg_class WHERE oid=$1)`, oid).Scan(&absent); err != nil {
			return err
		}
		if !absent {
			return errors.New("created index is still present after rollback")
		}
		return nil
	})
}
