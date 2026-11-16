package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// PrepareOwnedCronCheckpoints runs after PrepareWorkerState and preserves the
// original intent separately from the native commit acknowledgement.
func PrepareOwnedCronCheckpoints(ctx context.Context, state *pgx.Conn) error {
	if err := PrepareOwnedCronPeriodArchive(ctx, state); err != nil {
		return err
	}
	_, err := state.Exec(ctx, `CREATE TABLE IF NOT EXISTS pooltune_worker.owned_cron_acknowledgements (
		action_job_id bigint PRIMARY KEY REFERENCES pooltune_worker.owned_project_actions(job_id),
		evidence jsonb NOT NULL
	)`)
	return err
}

type ownedCronCheckpoint struct {
	Origin         int64
	Action         OwnedCronAction
	IsAcknowledged bool
}

const maxOwnedCronRecoveryDepth = 4

// ensureOwnedCronCheckpointClaim gives a lost action a read-only successor after
// ordinary claim attempts are exhausted or the project reconnects. The caller
// supplies the exact configured fixture and checks its active repair permission.
func ensureOwnedCronCheckpointClaim(ctx context.Context, state *pgx.Conn, epoch int64, target ProjectCronTarget, query int64) (child int64, err error) {
	if !strings.HasPrefix(target.Project, "supabase/"+OwnedIndexRookeryRef+"/") ||
		target.DatabaseOID == 0 || target.OwnerOID == 0 || target.JobID <= 0 || target.WitnessJobID <= 0 {
		return 0, errors.New("Cron recovery requires an exact owned Rookery fixture")
	}
	trusted, err := json.Marshal(target)
	if err != nil {
		return 0, err
	}
	err = pgx.BeginTxFunc(ctx, state, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if err := lockOwnedCronCheckpointEpoch(ctx, tx, target.Project, epoch); err != nil {
			return err
		}
		// Generic claiming preserves unresolved mutations. Retire only the exact
		// expired prepared-Cron attempt here so its read-only child can reconcile it.
		if _, err := tx.Exec(ctx, `UPDATE pooltune_worker.investigations j
            SET finished_at=clock_timestamp(),lease_until=NULL,result=jsonb_build_object('outcome','retry_exhausted')
            FROM pooltune_worker.owned_project_actions a
            WHERE a.job_id=j.id AND a.project_id=j.project_id AND a.project_id=$1
              AND a.operation='cron' AND a.outcome='pending' AND a.intent#>'{Intent,Target}'=$5::jsonb
              AND j.database_oid=$2 AND j.role_oid=$3 AND ($4::bigint=0 OR j.query_id=$4)
              AND j.finished_at IS NULL AND j.attempts >= $6 AND j.lease_until <= statement_timestamp()`,
			target.Project, target.DatabaseOID, target.OwnerOID, query, string(trusted), maxInvestigationAttempts); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `WITH RECURSIVE chain AS (
			SELECT j.id,j.query_id,j.finished_at,j.result,j.evidence,0 AS depth,k.evidence AS acknowledgement
			FROM pooltune_worker.owned_project_actions a
			JOIN pooltune_worker.investigations j ON j.id=a.job_id AND j.project_id=a.project_id
			LEFT JOIN pooltune_worker.owned_cron_acknowledgements k ON k.action_job_id=a.job_id
			WHERE a.project_id=$1 AND a.operation='cron' AND a.outcome='pending' AND a.intent#>'{Intent,Target}'=$5::jsonb
			  AND j.database_oid=$2 AND j.role_oid=$3 AND ($4::bigint=0 OR j.query_id=$4)
			UNION ALL
			SELECT j.id,j.query_id,j.finished_at,j.result,j.evidence,c.depth+1,c.acknowledgement
			FROM chain c JOIN pooltune_worker.investigations j ON j.parent_id=c.id
			WHERE j.project_id=$1 AND j.database_oid=$2 AND j.role_oid=$3 AND j.query_id=c.query_id
			  AND c.depth<$6
		)
		INSERT INTO pooltune_worker.investigations (project_id,database_oid,role_oid,query_id,evidence,parent_id,not_before)
		SELECT $1,$2,$3,c.query_id,c.evidence,c.id,
		       GREATEST(clock_timestamp(), (c.acknowledgement#>>'{Report,verify_after}')::timestamptz)
		FROM chain c WHERE c.depth<$6 AND c.finished_at IS NOT NULL
		  AND c.result->>'outcome' IN ('retry_exhausted','project_disconnected')
		  AND NOT EXISTS(SELECT FROM pooltune_worker.investigations j WHERE j.parent_id=c.id)
		  AND NOT EXISTS(SELECT FROM pooltune_worker.investigations j WHERE j.finished_at IS NULL
		                 AND j.project_id=$1 AND j.database_oid=$2 AND j.role_oid=$3 AND j.query_id=c.query_id)
		ORDER BY c.id LIMIT 1 RETURNING id`, target.Project, target.DatabaseOID, target.OwnerOID, query,
			string(trusted), maxOwnedCronRecoveryDepth).Scan(&child)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return child, err
}

// readOwnedCronCheckpoint follows only the active claim's exact project and
// query lineage. A committed intent without an acknowledgement still requires
// inspection and never authorizes repeating a model call or schedule write.
func readOwnedCronCheckpoint(ctx context.Context, state trialReader, claim investigationClaim) (*ownedCronCheckpoint, error) {
	var checkpoint ownedCronCheckpoint
	var intent, acknowledgement []byte
	err := state.QueryRow(ctx, `
		WITH RECURSIVE chain AS (
			SELECT id,parent_id FROM pooltune_worker.investigations
			WHERE id=$1 AND project_id=$2 AND database_oid=$3 AND role_oid=$4 AND query_id=$5
			UNION
			SELECT p.id,p.parent_id FROM pooltune_worker.investigations p JOIN chain c ON c.parent_id=p.id
			WHERE p.project_id=$2 AND p.database_oid=$3 AND p.role_oid=$4 AND p.query_id=$5
		)
		SELECT a.job_id,a.intent,k.evidence FROM chain c
		JOIN pooltune_worker.owned_project_actions a ON a.job_id=c.id AND a.project_id=$2
		LEFT JOIN pooltune_worker.owned_cron_acknowledgements k ON k.action_job_id=a.job_id
		WHERE a.operation='cron' AND a.outcome='pending'
	`, claim.ID, claim.Project, claim.Database, claim.Role, claim.Query).Scan(&checkpoint.Origin, &intent, &acknowledgement)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(intent, &checkpoint.Action); err != nil {
		return nil, err
	}
	i := checkpoint.Action
	if i.Intent.Target.Project != claim.Project || i.Intent.Target.DatabaseOID != claim.Database ||
		i.Intent.Target.OwnerOID != claim.Role || i.Observed.Database != claim.Database || i.Observed.Role != claim.Role ||
		i.Observed.Query != claim.Query || i.Report.JobID != checkpoint.Origin {
		return nil, errors.New("saved Cron action does not match its project and query")
	}
	if len(acknowledgement) != 0 {
		var ack OwnedCronAction
		if err := json.Unmarshal(acknowledgement, &ack); err != nil {
			return nil, err
		}
		if !ownedCronAcknowledges(i, ack) {
			return nil, errors.New("saved Cron acknowledgement does not match its original action")
		}
		checkpoint.Action, checkpoint.IsAcknowledged = ack, true
	}
	return &checkpoint, nil
}

func ownedCronAcknowledges(intent, ack OwnedCronAction) bool {
	receipt := ack.Report
	future := intent.Intent.FirstFutureCycle
	if !sameOwnedCronIntent(intent.Intent, ack.Intent) || !sameCronCounter(intent.Observed, ack.Observed) ||
		receipt.JobID != intent.Report.JobID || !receipt.IntentAt.Equal(intent.Report.IntentAt) ||
		!receipt.FirstFutureCycle.Equal(future) || !receipt.VerifyAfter.Equal(future.Add(6*time.Minute+2*time.Second)) ||
		receipt.CommitAt.IsZero() || receipt.CommitAt.Before(receipt.IntentAt) || !receipt.CommitAt.Before(future) ||
		ack.After.Schedule != intent.Intent.ProposedSchedule || ack.After.Fingerprint != intent.Intent.Before.Fingerprint ||
		ack.After.Fingerprint == "" || ack.After.Generation == "" || ack.After.Generation == intent.Intent.Before.Generation {
		return false
	}
	deadline := minTime(receipt.CommitAt.Add(7*time.Second), ownedCronHandoffDeadline(future))
	generation := intent.Intent.Generation
	return validateCronWitness(intent.Intent.Target.WitnessJobID, receipt.PostWitness, receipt.CommitAt, deadline,
		deadline.Add(-time.Nanosecond), generation, generation) == nil
}

// publishOwnedCronCheckpoint keeps acknowledgement and delayed child creation
// atomic, and publishes the verified child and settlement together. Follow-up
// and final writes are fenced with the current positive monitor epoch.
func publishOwnedCronCheckpoint(ctx context.Context, state *pgx.Conn, epoch int64, claim investigationClaim,
	decision OwnedCronDecision, acknowledgement *OwnedCronAction) (child int64, err error) {
	if epoch <= 0 {
		return 0, errClaimLost
	}
	outcome := decision.Report.Outcome
	waiting := outcome == "awaiting_evidence" || outcome == "awaiting_verification"
	if waiting == decision.NotBefore.IsZero() || (acknowledgement != nil && outcome != "awaiting_verification") {
		return 0, errors.New("invalid Cron checkpoint phase transition")
	}
	result := DispatchResult{JobID: claim.ID, Attempt: claim.Attempt, DiagnosticReason: decision.Report.Reason}
	cronSave(&result, &decision.Report)
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
		checkpoint, err := readOwnedCronCheckpoint(ctx, tx, claim)
		if err != nil {
			return err
		}
		if acknowledgement != nil {
			if checkpoint == nil || checkpoint.Origin != claim.ID || checkpoint.IsAcknowledged ||
				!ownedCronAcknowledges(checkpoint.Action, *acknowledgement) ||
				!decision.NotBefore.Equal(acknowledgement.Report.VerifyAfter) {
				return errors.New("Cron acknowledgement does not match its recorded action and continuation")
			}
			raw, err := json.Marshal(acknowledgement)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO pooltune_worker.owned_cron_acknowledgements (action_job_id,evidence)
				VALUES($1,$2::jsonb)`, checkpoint.Origin, string(raw)); err != nil {
				return err
			}
			checkpoint.Action, checkpoint.IsAcknowledged = *acknowledgement, true
		}
		switch outcome {
		case "awaiting_evidence", "no_change", "diagnosis_unavailable":
			if checkpoint != nil {
				return errors.New("a recorded Cron action must be inspected before another decision")
			}
		case "awaiting_verification", "improved_tested_inputs", "regressed":
			if checkpoint == nil || !checkpoint.IsAcknowledged ||
				decision.Report.JobID != checkpoint.Origin ||
				!decision.Report.FirstFutureCycle.Equal(checkpoint.Action.Report.FirstFutureCycle) ||
				!decision.Report.CommitAt.Equal(checkpoint.Action.Report.CommitAt) ||
				!decision.Report.VerifyAfter.Equal(checkpoint.Action.Report.VerifyAfter) ||
				(waiting && !decision.NotBefore.Equal(checkpoint.Action.Report.VerifyAfter)) ||
				(!waiting && checkpoint.Origin == claim.ID) {
				return errors.New("Cron verification requires a committed schedule and later durable observation")
			}
			if !waiting {
				var due bool
				if err := tx.QueryRow(ctx, `SELECT clock_timestamp()>=$1::timestamptz`, decision.Report.VerifyAfter).Scan(&due); err != nil {
					return err
				}
				if !due {
					return errors.New("Cron verification cannot be published before the approved observation window")
				}
			}
		case "inconclusive":
		default:
			return errors.New("unsupported owned Cron outcome")
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
				FROM pooltune_worker.investigations WHERE id=$1 RETURNING id`,
				claim.ID, decision.NotBefore).Scan(&child)
		}
		if checkpoint != nil {
			tag, err := tx.Exec(ctx, `UPDATE pooltune_worker.owned_project_actions SET outcome=$3
				WHERE job_id=$1 AND project_id=$2 AND outcome='pending' AND operation='cron'`, checkpoint.Origin, claim.Project, outcome)
			if err == nil && tag.RowsAffected() != 1 {
				return errClaimLost
			}
			return err
		}
		return nil
	})
	return child, err
}

func lockOwnedCronCheckpointEpoch(ctx context.Context, tx pgx.Tx, project string, epoch int64) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT project_id FROM pooltune_worker.projects
		WHERE project_id=$1 AND monitor_epoch=$2 AND monitor_epoch>0 FOR UPDATE`, project, epoch).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return errClaimLost
	}
	return err
}

func checkOwnedCronCheckpointLease(ctx context.Context, state trialReader, claim investigationClaim) error {
	var active bool
	err := state.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pooltune_worker.investigations
		WHERE id=$1 AND project_id=$2 AND attempts=$3 AND database_oid=$4 AND role_oid=$5 AND query_id=$6
		  AND finished_at IS NULL AND lease_until>clock_timestamp())`,
		claim.ID, claim.Project, claim.Attempt, claim.Database, claim.Role, claim.Query).Scan(&active)
	if err == nil && !active {
		return errClaimLost
	}
	return err
}
