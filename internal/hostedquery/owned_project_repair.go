package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"pooltune/internal/labdb"
	"pooltune/internal/supabaseproject"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// OwnedProjectRepair is a single privately configured Rookery test operation.
// The owner checkbox is checked separately from this direct writer binding.
type OwnedProjectRepair struct {
	Installation string
	Operation    string
	Database     uint32
	Role         uint32
	Query        int64
	Automatic    bool
	Config       *pgx.ConnConfig
	Run          OwnedRepairRunner
	Capacity     func(context.Context, *pgx.Conn) error
	Investigate  IndexInvestigator
	AdmitModel   func(context.Context) error
}

type OwnedRepairInvocation struct {
	Project, RunID string
	Database, Role uint32
	Query          int64
	Generation     []time.Time
}

type OwnedRepairGuards struct {
	CheckPermission func(context.Context) error
	CheckCapacity   func(context.Context) error
	RecordIntent    func(context.Context, any) error
}

type OwnedRepairRunner func(context.Context, OwnedRepairInvocation, IndexInvestigator, OwnedRepairGuards) (DispatchResult, error)

const ownedProjectRepairSchema = `
	CREATE TABLE IF NOT EXISTS pooltune_worker.owned_project_actions (
		job_id bigint PRIMARY KEY REFERENCES pooltune_worker.investigations(id),
		project_id text NOT NULL,
		operation text NOT NULL CHECK (operation IN ('analyze','statistics','vacuum','memory','cron')),
		intent jsonb NOT NULL,
		restore_intent jsonb,
		outcome text NOT NULL DEFAULT 'pending',
		recorded_at timestamptz NOT NULL DEFAULT statement_timestamp()
	);`

func (r *OwnedProjectRepair) Matches(project supabaseproject.Project) bool {
	if r == nil || r.Config == nil || r.Run == nil || r.Investigate == nil ||
		r.Database == 0 || r.Role == 0 || r.Automatic != (r.Query == 0) || r.Operation == "index" {
		return false
	}
	identity, err := supabaseproject.New(OwnedIndexRookeryRef, r.Installation)
	if err != nil || project.ID() != identity.ID() {
		return false
	}
	role := identity.OwnedRepairProbeRole(r.Operation)
	config := r.Config
	return role != "" && config.User == role && config.Host == "db."+OwnedIndexRookeryRef+".supabase.co" &&
		config.Port == 5432 && config.Database == "postgres" && len(config.Fallbacks) == 0 && config.TLSConfig != nil &&
		!config.TLSConfig.InsecureSkipVerify && config.TLSConfig.RootCAs != nil && config.TLSConfig.ServerName == config.Host
}

func (r *OwnedProjectRepair) matchesClaim(project supabaseproject.Project, job investigationClaim) bool {
	return r.Matches(project) && r.Database == job.Database && r.Role == job.Role && job.Query != 0 &&
		(r.Automatic || r.Query == job.Query)
}

func (r *OwnedProjectRepair) run(ctx context.Context, state, monitor *pgx.Conn, project supabaseproject.Project,
	epoch int64, claim investigationClaim, windows *ObservedLookupLatency, result *DispatchResult) error {
	started := time.Now()
	result.ObservedLatency = windows
	var pending bool
	err := state.QueryRow(ctx, `SELECT EXISTS (SELECT FROM pooltune_worker.owned_project_actions
		WHERE project_id=$1 AND outcome='pending')`, project.ID()).Scan(&pending)
	if err != nil {
		return err
	}
	if pending {
		result.Outcome, result.DiagnosticReason = "repair_interrupted", "A recorded repair needs reconciliation before another action."
		return nil
	}
	verify := func(ctx context.Context) error {
		if err := checkOwnedIndexClaim(ctx, state, project, epoch, claim); err != nil {
			return err
		}
		if len(claim.Generation) != 3 {
			return errLookupScope
		}
		var boot, reset, entry time.Time
		var objective float64
		var calls int64
		err := monitor.QueryRow(ctx, `SELECT boot,reset_at,entry_since,mean_execution_ms,calls FROM `+
			qualified(project.Role(), "snapshot")+`() WHERE database_oid=$1 AND role_oid=$2 AND query_id=$3`,
			claim.Database, claim.Role, claim.Query).Scan(&boot, &reset, &entry, &objective, &calls)
		if err != nil || objective != windows.ObjectiveMS || calls < 1 || !boot.Equal(claim.Generation[0]) ||
			!reset.Equal(claim.Generation[1]) || !entry.Equal(claim.Generation[2]) {
			return errors.Join(errLookupScope, err)
		}
		return nil
	}
	if err := verify(ctx); err != nil {
		return err
	}
	writer, err := pgx.ConnectConfig(ctx, r.Config.Copy())
	if err != nil {
		return err
	}
	defer labdb.Close(writer)
	if r.AdmitModel == nil {
		return errors.New("owned repair requires project model admission")
	}
	investigate := func(ctx context.Context, evidence IndexEvidence) (IndexProposal, error) {
		if err := verify(ctx); err != nil {
			return IndexProposal{}, err
		}
		if err := r.AdmitModel(ctx); err != nil {
			return IndexProposal{}, err
		}
		return r.Investigate(ctx, evidence)
	}
	var fence pgx.Tx
	var journaled bool
	var memoryIntent *ProjectFunctionMemoryIntent
	invocation := OwnedRepairInvocation{Project: project.ID(), RunID: fmt.Sprintf("owned-%s-%d-%d", r.Operation, claim.ID, claim.Attempt),
		Database: claim.Database, Role: claim.Role, Query: claim.Query, Generation: claim.Generation}
	out, runErr := r.Run(ctx, invocation, investigate, OwnedRepairGuards{
		CheckPermission: verify,
		CheckCapacity: func(ctx context.Context) error {
			var available bool
			err := writer.QueryRow(ctx, `SELECT sum(numbackends)*3 < current_setting('max_connections')::int*2 FROM pg_stat_database`).Scan(&available)
			if err == nil && !available {
				return errors.New("the database does not have connection headroom")
			}
			if err == nil && r.Capacity != nil {
				return r.Capacity(ctx, writer)
			}
			return err
		},
		RecordIntent: func(ctx context.Context, intent any) error {
			if journaled {
				restore, ok := intent.(ProjectFunctionMemoryIntent)
				if r.Operation != "memory" || !ok || memoryIntent == nil ||
					restore.Phase != "restore" || restore.Target != memoryIntent.Target ||
					restore.RunID != memoryIntent.RunID || fence == nil {
					return errClaimLost
				}
				err := releaseOwnedIndexEpoch(state, fence)
				fence = nil
				if err != nil {
					return err
				}
				if err := verify(ctx); err != nil {
					return err
				}
				if err := recordOwnedMemoryRestore(ctx, state, project.ID(), epoch, claim, restore); err != nil {
					return err
				}
				memoryIntent = nil
				fence, err = lockOwnedIndexEpoch(ctx, state, project.ID(), epoch, claim)
				return err
			}
			if err := verify(ctx); err != nil {
				return err
			}
			if err := recordOwnedProjectAction(ctx, state, project.ID(), epoch, claim, r.Operation, intent); err != nil {
				return err
			}
			journaled = true
			slog.Info("owned repair intent saved", "operation", r.Operation, "job", claim.ID, "elapsed_ms", elapsedMS(started))
			if memory, ok := intent.(ProjectFunctionMemoryIntent); ok && memory.Phase == "apply" {
				memoryIntent = &memory
			}
			var err error
			fence, err = lockOwnedIndexEpoch(ctx, state, project.ID(), epoch, claim)
			return err
		},
	})
	if fence != nil {
		runErr = errors.Join(runErr, releaseOwnedIndexEpoch(state, fence))
	}
	if errors.Is(runErr, ErrProjectModelBudget) && !journaled {
		result.Outcome, result.DiagnosticReason = "diagnosis_unavailable", "daily_model_limit"
		return nil
	}
	if runErr != nil {
		slog.Info("owned repair did not finish", "operation", r.Operation, "job", claim.ID,
			"elapsed_ms", elapsedMS(started), "action_recorded", journaled, "category", ownedRepairErrorCategory(runErr))
		return runErr
	}
	if out.Outcome == "" {
		return errors.New("owned repair did not return an outcome")
	}
	out.JobID, out.Attempt, out.ObservedLatency = result.JobID, result.Attempt, windows
	if out.Outcome == "improved_tested_inputs" && !journaled {
		return errors.New("owned repair improvement requires a recorded action")
	}
	if journaled {
		switch out.Outcome {
		case "improved_tested_inputs", "inconclusive", "regressed":
			out.ownedOperation = r.Operation
		case "repair_interrupted":
		default:
			return errors.New("owned repair outcome did not settle the recorded intent")
		}
	}
	*result = out
	slog.Info("owned repair evaluation finished", "operation", r.Operation, "job", claim.ID,
		"elapsed_ms", elapsedMS(started), "outcome", out.Outcome)
	return nil
}

func ownedRepairErrorCategory(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var sqlError *pgconn.PgError
	if errors.As(err, &sqlError) {
		return sqlError.Code
	}
	if errors.Is(err, errClaimLost) {
		return "claim_lost"
	}
	return "other"
}

func recordOwnedMemoryRestore(ctx context.Context, state *pgx.Conn, project string, epoch int64,
	claim investigationClaim, intent ProjectFunctionMemoryIntent) error {
	payload, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	tag, err := state.Exec(ctx, `WITH active AS MATERIALIZED (
		SELECT project_id FROM pooltune_worker.projects WHERE project_id=$1 AND monitor_epoch=$2 FOR UPDATE
	)
	UPDATE pooltune_worker.owned_project_actions a SET restore_intent=$5::jsonb
	FROM active p, pooltune_worker.investigations j
	WHERE j.id=$3 AND j.project_id=p.project_id AND j.attempts=$4
	  AND j.finished_at IS NULL AND j.lease_until>clock_timestamp()
	  AND a.project_id=p.project_id AND a.job_id=j.id AND a.operation='memory'
	  AND a.outcome='pending' AND a.restore_intent IS NULL`, project, epoch, claim.ID, claim.Attempt, string(payload))
	if err == nil && tag.RowsAffected() != 1 {
		return errClaimLost
	}
	return err
}

func recordOwnedProjectAction(ctx context.Context, state *pgx.Conn, project string, epoch int64,
	claim investigationClaim, operation string, intent any) error {
	if claim.RecoveryOnly {
		return errClaimLost
	}
	payload, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	return pgx.BeginTxFunc(ctx, state, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		var locked string
		err := tx.QueryRow(ctx, `SELECT project_id FROM pooltune_worker.projects WHERE project_id=$1 AND monitor_epoch=$2 FOR UPDATE`, project, epoch).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return errClaimLost
		}
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO pooltune_worker.owned_project_actions(job_id,project_id,operation,intent)
		SELECT j.id,j.project_id,$4,$5::jsonb FROM pooltune_worker.investigations j WHERE j.project_id=$1 AND j.id=$2 AND j.attempts=$3
		AND j.finished_at IS NULL AND j.lease_until>clock_timestamp() AND NOT EXISTS (
			SELECT FROM pooltune_worker.owned_project_actions a WHERE a.project_id=j.project_id AND a.outcome='pending'
		) ON CONFLICT (job_id) DO NOTHING`, locked, claim.ID, claim.Attempt, operation, string(payload))
		if err == nil && tag.RowsAffected() != 1 {
			return errClaimLost
		}
		return err
	})
}
