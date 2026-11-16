package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"
)

// saveManagedStatisticsIntent must run outside the epoch-fence transaction.
// Its independent commit makes the receipt durable before the native DB commit.
func saveManagedStatisticsIntent(ctx context.Context, journal *pgx.Conn, project string, epoch int64, claim investigationClaim, prior, next ManagedStatisticsIntent) error {
	if journal == nil || journal.PgConn().TxStatus() != 'I' || !validManagedStatisticsTransition(prior, next) {
		return errors.New("statistics receipt requires an independent committed journal transition")
	}
	a, err := json.Marshal(prior)
	if err != nil {
		return err
	}
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	tag, err := journal.Exec(ctx, `UPDATE pooltune_worker.owned_project_actions a SET intent=$6::jsonb
 FROM pooltune_worker.investigations j, pooltune_worker.projects p
 WHERE a.project_id=$1 AND a.job_id=$2 AND a.operation='statistics' AND a.outcome='pending' AND a.intent=$5::jsonb
 AND j.id=a.job_id AND j.project_id=a.project_id AND j.attempts=$3 AND j.finished_at IS NULL AND j.lease_until>clock_timestamp()
 AND p.project_id=a.project_id AND p.monitor_epoch=$4`, project, claim.ID, claim.Attempt, epoch, string(a), string(b))
	if err == nil && tag.RowsAffected() != 1 {
		return errClaimLost
	}
	return err
}

func validManagedStatisticsTransition(prior, next ManagedStatisticsIntent) bool {
	if !validManagedStatisticsIntent(prior, false) || !validManagedStatisticsIntent(next, true) {
		return false
	}
	if next.Phase == "commit_ready" {
		if prior.Phase != "intent" || prior.TrialVersion != "" || next.RestoredVersion != "" || next.RestoreTransactionID != "" {
			return false
		}
	} else if next.Phase == "restore_ready" {
		if prior.Phase != "commit_ready" && prior.Phase != "restore_ready" {
			return false
		}
		if next.TrialVersion != prior.TrialVersion || next.ObjectOID != prior.ObjectOID || next.TrialTransactionID != prior.TrialTransactionID || next.RestoreTransactionID == "" {
			return false
		}
	} else {
		return false
	}
	normalized := next
	normalized.Phase = prior.Phase
	normalized.ObjectOID = prior.ObjectOID
	normalized.TrialVersion = prior.TrialVersion
	normalized.RestoredVersion = prior.RestoredVersion
	normalized.TrialTransactionID = prior.TrialTransactionID
	normalized.RestoreTransactionID = prior.RestoreTransactionID
	a, _ := json.Marshal(prior)
	b, _ := json.Marshal(normalized)
	return slices.Equal(a, b)
}
