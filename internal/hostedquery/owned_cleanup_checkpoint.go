package hostedquery

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

const maxOwnedCleanupRecoveryDepth = 32

type ownedCleanupCheckpoint struct {
	Origin  int64
	Action  OwnedCleanupSettingsAction
	Restore *ProjectCleanupSettingsIntent
}

func enqueueOwnedCleanupRecovery(ctx context.Context, state *pgx.Conn, project string, epoch int64,
	req OwnedVacuumRequest, role uint32, query int64) error {
	target, err := json.Marshal(req.Workload.Target)
	if err != nil {
		return err
	}
	return pgx.BeginTxFunc(ctx, state, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if err := lockOwnedCronCheckpointEpoch(ctx, tx, project, epoch); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `WITH RECURSIVE chain AS (
			SELECT j.id,j.query_id,j.finished_at,j.result,j.evidence,0 AS depth
			FROM pooltune_worker.owned_project_actions a
			JOIN pooltune_worker.investigations j ON j.id=a.job_id AND j.project_id=a.project_id
			WHERE a.project_id=$1 AND a.operation='vacuum' AND a.outcome='pending'
			  AND a.intent#>'{Intent,Target}'=$5::jsonb AND a.intent#>'{Request,IndexOID}'=to_jsonb($6::bigint)
			  AND a.intent#>'{Request,IndexName}'=to_jsonb($7::text)
			  AND a.intent#>'{Request,Workload,HealthyOID}'=to_jsonb($8::bigint)
			  AND j.database_oid=$2 AND j.role_oid=$3 AND ($4::bigint=0 OR j.query_id=$4)
			UNION ALL
			SELECT j.id,j.query_id,j.finished_at,j.result,j.evidence,c.depth+1
			FROM chain c JOIN pooltune_worker.investigations j ON j.parent_id=c.id
			WHERE j.project_id=$1 AND j.database_oid=$2 AND j.role_oid=$3 AND j.query_id=c.query_id
			  AND c.depth<$9
		)
		INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence,parent_id)
		SELECT $1,$2,$3,c.query_id,c.evidence,c.id FROM chain c
		WHERE c.depth<$9 AND c.finished_at IS NOT NULL
		  AND c.result->>'outcome' IN ('retry_exhausted','project_disconnected')
		  AND NOT EXISTS(SELECT FROM pooltune_worker.investigations j WHERE j.parent_id=c.id)
		  AND NOT EXISTS(SELECT FROM pooltune_worker.investigations j WHERE j.finished_at IS NULL AND j.project_id=$1
		                 AND j.database_oid=$2 AND j.role_oid=$3 AND j.query_id=c.query_id)
		ORDER BY c.id LIMIT 1 ON CONFLICT(parent_id) DO NOTHING`, project, req.Workload.Target.DatabaseOID, role, query,
			string(target), int64(req.IndexOID), req.IndexName, int64(req.Workload.HealthyOID), maxOwnedCleanupRecoveryDepth)
		return err
	})
}

func readOwnedCleanupCheckpoint(ctx context.Context, state trialReader, claim investigationClaim) (*ownedCleanupCheckpoint, error) {
	var checkpoint ownedCleanupCheckpoint
	var intent, acknowledgement, restore []byte
	err := state.QueryRow(ctx, `
		WITH RECURSIVE chain AS (
			SELECT id,parent_id FROM pooltune_worker.investigations
			WHERE id=$1 AND project_id=$2 AND database_oid=$3 AND role_oid=$4 AND query_id=$5
			UNION
			SELECT p.id,p.parent_id FROM pooltune_worker.investigations p JOIN chain c ON c.parent_id=p.id
			WHERE p.project_id=$2 AND p.database_oid=$3 AND p.role_oid=$4 AND p.query_id=$5
		)
		SELECT a.job_id,a.intent,k.observation,a.restore_intent FROM chain c
		JOIN pooltune_worker.owned_project_actions a ON a.job_id=c.id AND a.project_id=$2
		LEFT JOIN pooltune_worker.owned_cleanup_acknowledgements k ON k.action_job_id=a.job_id
		WHERE a.operation='vacuum' AND a.outcome='pending' AND a.intent ? 'Request'
	`, claim.ID, claim.Project, claim.Database, claim.Role, claim.Query).Scan(&checkpoint.Origin, &intent, &acknowledgement, &restore)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(intent, &checkpoint.Action); err != nil {
		return nil, err
	}
	action := &checkpoint.Action
	w := action.Request.Workload
	if !w.valid("pt_hosted_vc_") || w.Project != claim.Project || w.Target.DatabaseOID != claim.Database ||
		w.RoleOID != claim.Role || w.QueryID != claim.Query || w.Target != action.Intent.Target ||
		action.Intent.Restoring || w.RunID != action.Intent.RunID || action.Report.CleanupTrigger == nil ||
		!action.Receipt.At.IsZero() || !action.VerificationEnds.After(action.Before.At) {
		return nil, errors.New("saved cleanup change does not match the claimed workload")
	}
	if len(acknowledgement) != 0 {
		if err := json.Unmarshal(acknowledgement, &action.Receipt); err != nil {
			return nil, err
		}
		if !validOwnedCleanupReceipt(*action, action.Receipt) {
			return nil, errors.New("saved cleanup acknowledgement does not match its action")
		}
	}
	if len(restore) != 0 {
		if err := json.Unmarshal(restore, &checkpoint.Restore); err != nil {
			return nil, err
		}
		if checkpoint.Restore == nil || !reversesOwnedCleanup(action.Intent, *checkpoint.Restore) {
			return nil, errors.New("saved cleanup restore does not reverse its original action")
		}
	}
	return &checkpoint, nil
}

func recordOwnedCleanupRestore(ctx context.Context, state *pgx.Conn, epoch int64,
	claim investigationClaim, checkpoint *ownedCleanupCheckpoint, intent ProjectCleanupSettingsIntent) error {
	if checkpoint == nil || checkpoint.Restore != nil || !validOwnedCleanupReceipt(checkpoint.Action, checkpoint.Action.Receipt) ||
		!reversesOwnedCleanup(checkpoint.Action.Intent, intent) {
		return errors.New("cleanup restore requires an acknowledged exact original action")
	}
	payload, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	return pgx.BeginTxFunc(ctx, state, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if err := lockOwnedCronCheckpointEpoch(ctx, tx, claim.Project, epoch); err != nil {
			return err
		}
		if err := checkOwnedCronCheckpointLease(ctx, tx, claim); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE pooltune_worker.owned_project_actions SET restore_intent=$3::jsonb
			WHERE job_id=$1 AND project_id=$2 AND operation='vacuum' AND outcome='pending' AND restore_intent IS NULL`,
			checkpoint.Origin, claim.Project, string(payload))
		if err == nil && tag.RowsAffected() != 1 {
			return errClaimLost
		}
		return err
	})
}

func reversesOwnedCleanup(applied, restore ProjectCleanupSettingsIntent) bool {
	return !applied.Restoring && restore.Restoring && applied.Target == restore.Target &&
		applied.RunID == restore.RunID && applied.BeforeOption == restore.AfterOption && applied.AfterOption == restore.BeforeOption
}

func publishOwnedCleanupCheckpoint(ctx context.Context, state *pgx.Conn, epoch int64, claim investigationClaim,
	result DispatchResult, decision OwnedCleanupSettingsDecision) (child int64, err error) {
	report := decision.Report
	waiting := report.Outcome == "awaiting_verification"
	if waiting == decision.NextAt.IsZero() || report.CleanupTrigger == nil ||
		(waiting && claim.RecoveryOnly) || result.Outcome != report.Outcome {
		return 0, errors.New("invalid cleanup checkpoint phase")
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return 0, err
	}
	err = pgx.BeginTxFunc(ctx, state, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if err := lockOwnedCronCheckpointEpoch(ctx, tx, claim.Project, epoch); err != nil {
			return err
		}
		if err := checkOwnedCronCheckpointLease(ctx, tx, claim); err != nil {
			return err
		}
		checkpoint, err := readOwnedCleanupCheckpoint(ctx, tx, claim)
		if err != nil {
			return err
		}
		if waiting && checkpoint == nil {
			return errors.New("cleanup verification needs an existing durable action")
		}
		if waiting && decision.Action != nil && checkpoint.Action.Receipt.At.IsZero() {
			receipt := decision.Action.Receipt
			if checkpoint.Origin != claim.ID || decision.Action.Intent != checkpoint.Action.Intent ||
				decision.Action.Request != checkpoint.Action.Request || !validOwnedCleanupReceipt(checkpoint.Action, receipt) {
				return errors.New("cleanup acknowledgement does not match its original action")
			}
			raw, err := json.Marshal(receipt)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO pooltune_worker.owned_cleanup_acknowledgements(action_job_id,observation)
				VALUES($1,$2::jsonb)`, checkpoint.Origin, string(raw)); err != nil {
				return err
			}
			checkpoint.Action.Receipt = receipt
		}
		switch report.Outcome {
		case "awaiting_verification":
			if decision.NextAt.After(checkpoint.Action.VerificationEnds) && !checkpoint.Action.Receipt.At.IsZero() && checkpoint.Restore == nil {
				return errors.New("cleanup follow-up is outside its recorded observation window")
			}
		case "improved_tested_inputs":
			if !verifiedCleanupCheckpoint(checkpoint, claim.ID, report) {
				return errors.New("cleanup improvement requires a committed action and later natural cleanup evidence")
			}
		case "inconclusive", "regressed", "repair_interrupted":
		case "no_change", "insufficient_evidence", "diagnosis_unavailable":
			if checkpoint != nil {
				return errors.New("a recorded cleanup action must be inspected before it is settled")
			}
		default:
			return errors.New("unsupported cleanup checkpoint outcome")
		}
		tag, err := tx.Exec(ctx, `UPDATE pooltune_worker.investigations SET finished_at=clock_timestamp(),lease_until=NULL,result=$4::jsonb
			WHERE id=$1 AND project_id=$2 AND attempts=$3 AND finished_at IS NULL AND lease_until>clock_timestamp()`,
			claim.ID, claim.Project, claim.Attempt, string(payload))
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errClaimLost
		}
		if waiting {
			return tx.QueryRow(ctx, `INSERT INTO pooltune_worker.investigations
				(project_id,database_oid,role_oid,query_id,evidence,parent_id,not_before)
				SELECT project_id,database_oid,role_oid,query_id,evidence,id,$2
				FROM pooltune_worker.investigations WHERE id=$1 RETURNING id`, claim.ID, decision.NextAt).Scan(&child)
		}
		if checkpoint != nil {
			tag, err := tx.Exec(ctx, `UPDATE pooltune_worker.owned_project_actions SET outcome=$3
				WHERE job_id=$1 AND project_id=$2 AND outcome='pending' AND operation='vacuum'`,
				checkpoint.Origin, claim.Project, report.Outcome)
			if err == nil && tag.RowsAffected() != 1 {
				return errClaimLost
			}
			return err
		}
		return nil
	})
	return child, err
}

func verifiedCleanupCheckpoint(checkpoint *ownedCleanupCheckpoint, claimID int64, report VacuumReport) bool {
	if checkpoint == nil || checkpoint.Origin == claimID || checkpoint.Restore != nil || report.CleanupTrigger == nil {
		return false
	}
	action, trigger := checkpoint.Action, report.CleanupTrigger
	outcome, _ := verifyVacuum(action.Report.Before, report.After)
	return validOwnedCleanupReceipt(action, action.Receipt) && outcome == "improved_tested_inputs" &&
		trigger.SettingChanged && trigger.AutomaticCleaned && !trigger.Restored && trigger.After.ScaleOption == action.Intent.AfterOption &&
		ownedAutomaticCleanupProven(action, trigger.After)
}
