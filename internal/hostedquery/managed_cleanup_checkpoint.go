package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"time"
)

type managedCleanupPublication struct {
	Action managedCleanupAction
	Origin int64
	Next   *time.Time
	Ack    bool
}

func readManagedCleanupAction(ctx context.Context, state trialReader, claim investigationClaim) (*managedCleanupAction, int64, error) {
	var raw []byte
	var origin int64
	err := state.QueryRow(ctx, `WITH RECURSIVE chain AS (
 SELECT id,parent_id FROM pooltune_worker.investigations WHERE id=$1 AND project_id=$2
 UNION SELECT p.id,p.parent_id FROM pooltune_worker.investigations p JOIN chain c ON c.parent_id=p.id WHERE p.project_id=$2)
 SELECT a.job_id,a.intent FROM chain c JOIN pooltune_worker.owned_project_actions a ON a.job_id=c.id
 WHERE a.project_id=$2 AND a.operation='vacuum' AND a.outcome='pending' AND a.intent->>'Version'='2' AND a.intent->>'RunID' LIKE 'managed-maintenance-%'`, claim.ID, claim.Project).Scan(&origin, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	var a managedCleanupAction
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, 0, err
	}
	if err := validManagedCleanupAction(a, claim); err != nil {
		return nil, 0, err
	}
	parts := managedMaintenanceRunID.FindStringSubmatch(a.RunID)
	if len(parts) != 2 || parts[1] != fmt.Sprint(origin) {
		return nil, 0, errors.New("cleanup action origin changed")
	}
	return &a, origin, nil
}

func publishManagedCleanup(ctx context.Context, state *pgx.Conn, epoch int64, claim investigationClaim, result DispatchResult, p managedCleanupPublication) error {
	a := p.Action
	if result.ManagedCleanup == nil || result.Outcome != result.ManagedCleanup.Outcome || (p.Next != nil) != (result.Outcome == "awaiting_verification") || (p.Next != nil && (a.Receipt == nil || p.Next.After(a.Deadline))) {
		return errors.New("invalid managed cleanup publication")
	}
	if result.Outcome == "improved_tested_inputs" && (a.Receipt == nil || a.Restore != nil || !result.ManagedCleanup.Trigger.AutomaticCleaned || !ownedAutomaticCleanupProven(a.owned(), result.ManagedCleanup.Trigger.After) || result.ManagedCleanup.After == nil) {
		return errors.New("cleanup benefit lacks exact automatic cleanup proof")
	}
	return pgx.BeginTxFunc(ctx, state, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := lockOwnedCronCheckpointEpoch(ctx, tx, claim.Project, epoch); err != nil {
			return err
		}
		if err := checkOwnedCronCheckpointLease(ctx, tx, claim); err != nil {
			return err
		}
		old, origin, err := readManagedCleanupAction(ctx, tx, claim)
		if err != nil {
			return err
		}
		if old == nil || origin != p.Origin {
			return errClaimLost
		}
		prior := a
		if p.Ack {
			prior.Receipt = nil
		}
		before, _ := json.Marshal(old)
		expected, _ := json.Marshal(prior)
		if string(before) != string(expected) {
			return errors.New("cleanup acknowledgement changed before publication")
		}
		if p.Ack {
			if p.Origin != claim.ID || old.Receipt != nil || a.Receipt == nil {
				return errClaimLost
			}
			payload, err := json.Marshal(a)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE pooltune_worker.owned_project_actions SET intent=$3::jsonb WHERE project_id=$1 AND job_id=$2 AND operation='vacuum' AND outcome='pending'`, claim.Project, p.Origin, string(payload)); err != nil {
				return err
			}
		}
		payload, err := json.Marshal(result)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE pooltune_worker.investigations SET finished_at=clock_timestamp(),lease_until=NULL,result=$4::jsonb WHERE project_id=$1 AND id=$2 AND attempts=$3 AND finished_at IS NULL AND lease_until>clock_timestamp()`, claim.Project, claim.ID, claim.Attempt, string(payload))
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errClaimLost
		}
		if p.Next != nil {
			_, err = tx.Exec(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence,parent_id,not_before) SELECT project_id,database_oid,role_oid,query_id,evidence,id,$2 FROM pooltune_worker.investigations WHERE id=$1`, claim.ID, *p.Next)
		} else {
			_, err = tx.Exec(ctx, `UPDATE pooltune_worker.owned_project_actions SET outcome=$3 WHERE project_id=$1 AND job_id=$2 AND operation='vacuum' AND outcome='pending'`, claim.Project, p.Origin, result.Outcome)
		}
		return err
	})
}

func recordManagedCleanupRestore(ctx context.Context, state *pgx.Conn, epoch int64, claim investigationClaim, origin int64, a managedCleanupAction, restore ProjectCleanupSettingsIntent) error {
	if a.Restore != nil || !reversesOwnedCleanup(a.Intent, restore) {
		return errors.New("invalid cleanup restoration")
	}
	before, _ := json.Marshal(a)
	next := a
	next.Restore = &restore
	after, _ := json.Marshal(next)
	return pgx.BeginTxFunc(ctx, state, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := lockOwnedCronCheckpointEpoch(ctx, tx, claim.Project, epoch); err != nil {
			return err
		}
		if err := checkOwnedCronCheckpointLease(ctx, tx, claim); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE pooltune_worker.owned_project_actions SET intent=$4::jsonb WHERE project_id=$1 AND job_id=$2 AND operation='vacuum' AND outcome='pending' AND intent=$3::jsonb`, claim.Project, origin, string(before), string(after))
		if err == nil && tag.RowsAffected() != 1 {
			return errClaimLost
		}
		return err
	})
}

func ensureManagedCleanupContinuation(ctx context.Context, state *pgx.Conn, project string, epoch int64) error {
	return pgx.BeginTxFunc(ctx, state, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := lockOwnedCronCheckpointEpoch(ctx, tx, project, epoch); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `WITH RECURSIVE chain AS (
 SELECT j.id,j.evidence,j.database_oid,j.role_oid,j.query_id,j.finished_at,j.result,0 depth FROM pooltune_worker.owned_project_actions a JOIN pooltune_worker.investigations j ON j.id=a.job_id
 WHERE a.project_id=$1 AND a.operation='vacuum' AND a.outcome='pending' AND a.intent->>'Version'='2' AND a.intent->>'RunID' LIKE 'managed-maintenance-%'
 UNION ALL SELECT j.id,j.evidence,j.database_oid,j.role_oid,j.query_id,j.finished_at,j.result,c.depth+1 FROM chain c JOIN pooltune_worker.investigations j ON j.parent_id=c.id WHERE j.project_id=$1 AND c.depth<32)
 INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence,parent_id)
 SELECT $1,c.database_oid,c.role_oid,c.query_id,c.evidence,c.id FROM chain c WHERE c.finished_at IS NOT NULL AND c.result->>'outcome' IN ('retry_exhausted','project_disconnected') AND c.depth<32
 AND NOT EXISTS(SELECT FROM pooltune_worker.investigations j WHERE j.parent_id=c.id) AND NOT EXISTS(SELECT FROM pooltune_worker.investigations j WHERE j.project_id=$1 AND j.finished_at IS NULL)
 ORDER BY c.id LIMIT 1 ON CONFLICT(parent_id) DO NOTHING`, project)
		return err
	})
}
