package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
)

// OwnedProjectCron binds one privately configured Rookery job and witness.
// The supervisor only passes the binding while the owner permits Cron repairs.
// QueryID may be zero to let normal traffic discover the exact fixture query.
type OwnedProjectCron struct {
	Config      *pgx.ConnConfig
	Request     OwnedCronRequest
	QueryID     int64
	Investigate IndexInvestigator
	AdmitModel  func(context.Context) error
	Capacity    func(context.Context, *pgx.Conn) error
}

func (f *OwnedProjectCron) Matches(project supabaseproject.Project) bool {
	return f != nil && f.Investigate != nil && project.ID() == f.Request.Target.Project &&
		validOwnedCronFixtureRoute(f.Config, f.Request)
}

func (f *OwnedProjectCron) ensureClaim(ctx context.Context, state *pgx.Conn, project supabaseproject.Project, epoch int64) error {
	if !f.Matches(project) || f.AdmitModel == nil {
		return nil
	}
	_, err := ensureOwnedCronCheckpointClaim(ctx, state, epoch, f.Request.Target, f.QueryID)
	return err
}

type hostedCronRunner func(context.Context, *pgx.ConnConfig, OwnedCronRequest, *OwnedCronAction, OwnedCronPeriodReader, IndexInvestigator, OwnedCronGuards) (OwnedCronDecision, error)

// dispatch owns publication for a matched claim. A nil result leaves the claim
// with the normal diagnosis route. A continuation is recognized independently
// of fresh breach windows so a restart does not re-run the model or mutation.
func (f *OwnedProjectCron) dispatch(ctx context.Context, state *pgx.Conn, project supabaseproject.Project, epoch int64,
	claim investigationClaim, windows *ObservedLookupLatency,
	targetSession func(context.Context) (*pgx.Conn, func(), error)) (*DispatchResult, error) {
	return f.dispatchWith(ctx, state, project, epoch, claim, windows, targetSession, RunOwnedCronRepair)
}

func (f *OwnedProjectCron) dispatchWith(ctx context.Context, state *pgx.Conn, project supabaseproject.Project, epoch int64,
	claim investigationClaim, windows *ObservedLookupLatency,
	targetSession func(context.Context) (*pgx.Conn, func(), error), run hostedCronRunner) (*DispatchResult, error) {
	if !f.Matches(project) || f.AdmitModel == nil || claim.RecoveryOnly || claim.Project != project.ID() ||
		claim.Database != f.Request.Target.DatabaseOID || claim.Role != f.Request.Target.OwnerOID || claim.Query == 0 ||
		(f.QueryID != 0 && f.QueryID != claim.Query) {
		return nil, nil
	}
	checkpoint, err := readOwnedCronCheckpoint(ctx, state, claim)
	if err != nil {
		return nil, err
	}
	var prior *OwnedCronAction
	if checkpoint != nil {
		if checkpoint.Action.Intent.Target != f.Request.Target {
			return nil, errors.New("pending Cron action does not match the configured job and witness")
		}
		prior = &checkpoint.Action
	} else {
		delayed, err := isOwnedCronEvidenceContinuation(ctx, state, claim, f.Request.Candidate.Schema)
		if err != nil {
			return nil, err
		}
		if !delayed && (windows == nil || len(windows.SharedBlockAccesses) != 5) {
			return nil, nil
		}
		var blocked bool
		if err := state.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pooltune_worker.owned_project_actions
			WHERE project_id=$1 AND outcome='pending')`, project.ID()).Scan(&blocked); err != nil || blocked {
			return nil, err
		}
	}
	if len(claim.Generation) != 3 {
		return nil, errLookupScope
	}
	req := f.Request
	req.JobID, req.RunID = claim.ID, fmt.Sprintf("owned-cron-%d-%d", claim.ID, claim.Attempt)
	req.Observed = Counter{Database: claim.Database, Role: claim.Role, Query: claim.Query,
		Boot: claim.Generation[0], Reset: claim.Generation[1], Entry: claim.Generation[2]}
	monitor, closeMonitor, err := targetSession(ctx)
	if err != nil {
		return nil, err
	}
	defer closeMonitor()
	var evidence struct {
		Objective float64 `json:"objective_ms"`
	}
	if err := json.Unmarshal(claim.queueEvidence, &evidence); err != nil || evidence.Objective <= 0 || !nonnegativeFinite(evidence.Objective) {
		return nil, errLookupScope
	}
	verify := func(ctx context.Context, _ ProjectCronIntent) error {
		if err := checkOwnedIndexClaim(ctx, state, project, epoch, claim); err != nil {
			return err
		}
		var boot, reset, entry time.Time
		var objective float64
		var calls int64
		err := monitor.QueryRow(ctx, `SELECT boot,reset_at,entry_since,mean_execution_ms,calls FROM `+
			qualified(project.Role(), "snapshot")+`() WHERE database_oid=$1 AND role_oid=$2 AND query_id=$3`,
			claim.Database, claim.Role, claim.Query).Scan(&boot, &reset, &entry, &objective, &calls)
		if err != nil || objective != evidence.Objective || calls < 1 || !boot.Equal(req.Observed.Boot) ||
			!reset.Equal(req.Observed.Reset) || !entry.Equal(req.Observed.Entry) {
			return errors.Join(errLookupScope, err)
		}
		return nil
	}
	if err := verify(ctx, ProjectCronIntent{}); err != nil {
		return nil, err
	}
	read := func(ctx context.Context, req OwnedCronRequest, first time.Time) ([]CronPeriod, error) {
		return ReadOwnedCronPeriods(ctx, state, req, first)
	}
	var fence pgx.Tx
	var acknowledgement *OwnedCronAction
	var guards OwnedCronGuards
	var investigate IndexInvestigator
	if prior == nil {
		investigate = f.Investigate
		guards = OwnedCronGuards{CheckPermission: verify, AdmitModel: f.AdmitModel,
			CheckCapacity: func(ctx context.Context, _ ProjectCronIntent) error { return f.checkCapacity(ctx) },
			RecordIntent: func(ctx context.Context, action OwnedCronAction) error {
				if fence != nil {
					return errClaimLost
				}
				if err := recordOwnedProjectAction(ctx, state, project.ID(), epoch, claim, "cron", action); err != nil {
					return err
				}
				var err error
				fence, err = lockOwnedIndexEpoch(ctx, state, project.ID(), epoch, claim)
				return err
			},
			RecordAcknowledgement: func(_ context.Context, action OwnedCronAction) error {
				if fence == nil || acknowledgement != nil {
					return errClaimLost
				}
				acknowledgement = &action
				return nil
			},
		}
	}
	decision, err := run(ctx, f.Config, req, prior, read, investigate, guards)
	if fence != nil {
		err = errors.Join(err, releaseOwnedIndexEpoch(state, fence))
	}
	if errors.Is(err, ErrProjectModelBudget) && prior == nil && fence == nil {
		decision = ownedCronResult(CronReport{Fixture: req.Candidate.Schema, JobID: claim.ID}, "diagnosis_unavailable", "daily_model_limit")
	} else if err != nil {
		return nil, err
	}
	if _, err := publishOwnedCronCheckpoint(ctx, state, epoch, claim, decision, acknowledgement); err != nil {
		return nil, err
	}
	result := DispatchResult{JobID: claim.ID, Attempt: claim.Attempt, DiagnosticReason: decision.Report.Reason}
	cronSave(&result, &decision.Report)
	return &result, nil
}

func isOwnedCronEvidenceContinuation(ctx context.Context, state trialReader, claim investigationClaim, fixture string) (bool, error) {
	if claim.ParentID == 0 {
		return false, nil
	}
	var waiting bool
	err := state.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pooltune_worker.investigations
		WHERE id=$1 AND project_id=$2 AND database_oid=$3 AND role_oid=$4 AND query_id=$5
		  AND finished_at IS NOT NULL AND result->>'outcome'='awaiting_evidence' AND result#>>'{cron,fixture}'=$6)`,
		claim.ParentID, claim.Project, claim.Database, claim.Role, claim.Query, fixture).Scan(&waiting)
	return waiting, err
}

func (f *OwnedProjectCron) checkCapacity(ctx context.Context) error {
	writer, err := pgx.ConnectConfig(ctx, f.Config.Copy())
	if err != nil {
		return err
	}
	defer closeVacuumConnection(writer)
	var available bool
	if err := writer.QueryRow(ctx, `SELECT sum(numbackends)*3 < current_setting('max_connections')::int*2 FROM pg_stat_database`).Scan(&available); err != nil {
		return err
	}
	if !available {
		return errors.New("the database does not have connection headroom")
	}
	if f.Capacity != nil {
		return f.Capacity(ctx, writer)
	}
	return nil
}
