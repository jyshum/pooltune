package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func readManagedCronAction(ctx context.Context, state trialReader, claim investigationClaim) (*managedCronAction, int64, error) {
	var raw []byte
	var origin int64
	err := state.QueryRow(ctx, `WITH RECURSIVE chain AS (
 SELECT id,parent_id FROM pooltune_worker.investigations WHERE id=$1 AND project_id=$2
 UNION SELECT p.id,p.parent_id FROM pooltune_worker.investigations p JOIN chain c ON c.parent_id=p.id WHERE p.project_id=$2)
 SELECT a.job_id,a.intent FROM chain c JOIN pooltune_worker.owned_project_actions a ON a.job_id=c.id
 WHERE a.project_id=$2 AND a.operation='cron' AND a.outcome='pending' AND a.intent->>'RunID' LIKE 'managed-cron-%'`, claim.ID, claim.Project).Scan(&origin, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	var a managedCronAction
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, 0, err
	}
	if a.RunID == "" || a.RunID != a.Intent.RunID || a.Enrollment.Target != a.Intent.Target || a.Intent.Target.Project != claim.Project || a.Intent.Target.DatabaseOID != claim.Database || a.Queries.Target.Role != claim.Role || a.Queries.Target.Query != claim.Query || !ManagedCronCause(a.Evidence) {
		return nil, 0, errors.New("pending scheduling intent does not match its claimed job")
	}
	if a.Receipt != nil && (a.Followup == nil || a.Receipt.CommitAt.IsZero() || a.Receipt.After.Schedule != a.Intent.ProposedSchedule || a.Receipt.After.Fingerprint != a.Intent.Before.Fingerprint || a.Receipt.After.Generation == a.Intent.Before.Generation || !a.Receipt.FirstFutureCycle.Equal(a.Intent.FirstFutureCycle) || !a.VerifyAfter.Equal(a.Intent.FirstFutureCycle.Add(3*time.Duration(a.Intent.Window.EveryMinutes)*time.Minute+2*time.Second))) {
		return nil, 0, errors.New("pending scheduling receipt is inconsistent")
	}
	if a.Followup != nil && (a.Receipt == nil || !a.Followup.After || a.Followup.Target != a.Intent.Target || a.Followup.Window != a.Intent.Window || a.Followup.Schedule != a.Receipt.After.Schedule || a.Followup.Generation != a.Receipt.After.Generation || len(a.Followup.Fingerprint) != 64) {
		return nil, 0, errors.New("future scheduling observation is not bound to its receipt")
	}
	return &a, origin, nil
}

func publishManagedCronAction(ctx context.Context, state *pgx.Conn, project string, epoch int64, claim investigationClaim, origin int64, a managedCronAction, result DispatchResult, next *time.Time, ack bool) error {
	if result.ManagedCron == nil || (next != nil) != (result.Outcome == "awaiting_verification") || (next != nil && (a.Receipt == nil || !next.Equal(a.VerifyAfter))) {
		return errors.New("invalid managed scheduling publication")
	}
	return pgx.BeginTxFunc(ctx, state, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := lockOwnedCronCheckpointEpoch(ctx, tx, project, epoch); err != nil {
			return err
		}
		if err := checkOwnedCronCheckpointLease(ctx, tx, claim); err != nil {
			return err
		}
		old, oldOrigin, err := readManagedCronAction(ctx, tx, claim)
		if err != nil {
			return err
		}
		if old == nil || oldOrigin != origin {
			return errClaimLost
		}
		prior := a
		if ack {
			prior.Receipt = nil
			prior.Followup = nil
			prior.VerifyAfter = time.Time{}
		}
		x, _ := json.Marshal(old)
		y, _ := json.Marshal(prior)
		if string(x) != string(y) {
			return errors.New("scheduling receipt changed before publication")
		}
		if ack {
			if origin != claim.ID || old.Receipt != nil || a.Receipt == nil {
				return errClaimLost
			}
			raw, err := json.Marshal(a)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE pooltune_worker.owned_project_actions SET intent=$3::jsonb WHERE project_id=$1 AND job_id=$2 AND operation='cron' AND outcome='pending'`, project, origin, string(raw)); err != nil {
				return err
			}
		}
		payload, err := json.Marshal(result)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE pooltune_worker.investigations SET finished_at=clock_timestamp(),lease_until=NULL,result=$4::jsonb WHERE project_id=$1 AND id=$2 AND attempts=$3 AND finished_at IS NULL AND lease_until>clock_timestamp()`, project, claim.ID, claim.Attempt, string(payload))
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errClaimLost
		}
		if next != nil {
			_, err = tx.Exec(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence,parent_id,not_before) SELECT project_id,database_oid,role_oid,query_id,evidence,id,$2 FROM pooltune_worker.investigations WHERE id=$1`, claim.ID, *next)
		} else {
			_, err = tx.Exec(ctx, `UPDATE pooltune_worker.owned_project_actions SET outcome=$3 WHERE project_id=$1 AND job_id=$2 AND operation='cron' AND outcome='pending'`, project, origin, result.Outcome)
		}
		return err
	})
}

func cleanupManagedCronWitnesses(ctx context.Context, state, owner *pgx.Conn, project string) error {
	pending, err := PendingManagedCronWitnesses(ctx, state, project)
	if err != nil {
		return err
	}
	for _, w := range pending {
		if err := RemoveManagedCronWitness(ctx, owner, w); err != nil {
			return err
		}
		if err := SettleManagedCronWitness(ctx, state, w); err != nil {
			return err
		}
	}
	return nil
}

// A failed continuation must not strand an already committed schedule. Reopen
// only its exact child chain; no new model call or native mutation is authorized.
func ensureManagedCronContinuation(ctx context.Context, state *pgx.Conn, project string, epoch int64) error {
	return pgx.BeginTxFunc(ctx, state, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := lockOwnedCronCheckpointEpoch(ctx, tx, project, epoch); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `WITH RECURSIVE chain AS (
 SELECT j.id,j.evidence,j.database_oid,j.role_oid,j.query_id,j.finished_at,j.result,0 depth
 FROM pooltune_worker.owned_project_actions a JOIN pooltune_worker.investigations j ON j.id=a.job_id
 WHERE a.project_id=$1 AND a.operation='cron' AND a.outcome='pending' AND a.intent->>'RunID' LIKE 'managed-cron-%'
 UNION ALL SELECT j.id,j.evidence,j.database_oid,j.role_oid,j.query_id,j.finished_at,j.result,c.depth+1 FROM chain c
 JOIN pooltune_worker.investigations j ON j.parent_id=c.id WHERE j.project_id=$1 AND c.depth<32)
 INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence,parent_id)
 SELECT $1,c.database_oid,c.role_oid,c.query_id,c.evidence,c.id FROM chain c
 WHERE c.finished_at IS NOT NULL AND c.result->>'outcome' IN ('retry_exhausted','project_disconnected') AND c.depth<32
 AND NOT EXISTS(SELECT FROM pooltune_worker.investigations j WHERE j.parent_id=c.id)
 AND NOT EXISTS(SELECT FROM pooltune_worker.investigations j WHERE j.project_id=$1 AND j.finished_at IS NULL)
 ORDER BY c.id LIMIT 1 ON CONFLICT(parent_id) DO NOTHING`, project)
		return err
	})
}
