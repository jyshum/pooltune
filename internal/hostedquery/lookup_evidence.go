package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"pooltune/internal/labdb"
	"pooltune/internal/supabaseproject"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type LookupEvidence struct {
	TrafficPlan          *TrafficPlan        `json:"traffic_plan,omitempty"`
	MeasuredPlan         *LookupMeasuredPlan `json:"measured_plan,omitempty"`
	ObjectiveMS          float64             `json:"objective_ms"`
	WindowMeansMS        []float64           `json:"recent_window_mean_ms"`
	WindowCalls          []int64             `json:"recent_window_calls"`
	SharedBlockAccesses  []int64             `json:"recent_window_shared_block_accesses"`
	TablePages           int32               `json:"table_pages"`
	EstimatedTableRows   *float64            `json:"estimated_table_rows"`
	LeadingBTree         bool                `json:"leading_btree"`
	ModifiedSinceAnalyze int64               `json:"modified_since_analyze"`
	EstimatedDeadRows    int64               `json:"estimated_dead_rows"`
}

type LookupDiagnosis struct {
	Reason      string          `json:"reason"`
	Assessments json.RawMessage `json:"assessments"`
	Model       string          `json:"model"`
	ModelMS     float64         `json:"model_ms"`
}

type EnrolledLookupInvestigator func(ctx context.Context, runID string, tableOID uint32, column int16, at time.Time,
	evidence LookupEvidence, admit func(context.Context) error) (*LookupDiagnosis, error)

// ErrProjectModelBudget indicates the project did not authorize a new provider request.
var ErrProjectModelBudget = errors.New("project model call budget reached")

var errLookupScope = errors.New("lookup evidence grant or queued observations unavailable")

type lookupGrant struct {
	id         int64
	tableOID   uint32
	column     int16
	discovered bool
}

func readProjectLookup(ctx context.Context, target *pgx.Conn, project supabaseproject.Project, claim investigationClaim, windows *ObservedLookupLatency) (lookupGrant, LookupEvidence, error) {
	if age := time.Since(windows.Through); age < 0 || age > maxObservedLookupAge {
		return lookupGrant{}, LookupEvidence{}, errLookupScope
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var grant lookupGrant
	var evidence LookupEvidence
	var database uint32
	var generation [3]time.Time
	read := func(name string) error {
		return target.QueryRow(ctx, "SELECT * FROM "+qualified(project.Role(), name)+"($1::oid,$2::bigint)", claim.Role, claim.Query).
			Scan(&grant.id, &database, &grant.tableOID, &grant.column, &evidence.ObjectiveMS,
				&generation[0], &generation[1], &generation[2],
				&evidence.TablePages, &evidence.EstimatedTableRows, &evidence.LeadingBTree, &evidence.ModifiedSinceAnalyze, &evidence.EstimatedDeadRows)
	}
	err := read("read_lookup")
	var pgErr *pgconn.PgError
	if errors.Is(err, pgx.ErrNoRows) || (errors.As(err, &pgErr) && pgErr.Code == "42883") {
		err = read("read_discovered_lookup")
		grant.discovered = true
	}
	if errors.Is(err, pgx.ErrNoRows) || (errors.As(err, &pgErr) && (pgErr.Code == "42501" || pgErr.Code == "42883")) {
		return lookupGrant{}, LookupEvidence{}, errLookupScope
	}
	if err != nil {
		return lookupGrant{}, LookupEvidence{}, err
	}
	if grant.id <= 0 || grant.tableOID == 0 || grant.column <= 0 || database != claim.Database ||
		evidence.ObjectiveMS != windows.ObjectiveMS || len(claim.Generation) != len(generation) {
		return lookupGrant{}, LookupEvidence{}, errLookupScope
	}
	for n, at := range generation {
		if at.IsZero() || !at.Equal(claim.Generation[n]) {
			return lookupGrant{}, LookupEvidence{}, errLookupScope
		}
	}
	evidence.WindowMeansMS = windows.WindowMeansMS
	evidence.WindowCalls = windows.WindowCalls
	evidence.SharedBlockAccesses = windows.SharedBlockAccesses
	return grant, evidence, nil
}

// DiagnoseProjectLookup consumes only an active managed project's queue. Its
// result has no repair proposal, action or continuation.
func DiagnoseProjectLookup(ctx context.Context, state, target *pgx.Conn, project supabaseproject.Project, epoch int64, investigate EnrolledLookupInvestigator) (*DispatchResult, error) {
	return diagnoseLookup(ctx, state, project, epoch, investigate, nil, nil, nil, nil, func(context.Context) (*pgx.Conn, func(), error) {
		return target, func() {}, nil
	})
}

// DiagnoseHostedProjectLookup opens a second Supabase session only when a claimed
// job has eligible detector evidence. Idle queue checks stay on the state database.
func DiagnoseHostedProjectLookup(ctx context.Context, state *pgx.Conn, target *pgx.ConnConfig, project supabaseproject.Project, epoch int64, investigate EnrolledLookupInvestigator, logs *TrafficPlanReader, owned ...*OwnedProjectIndex) (*DispatchResult, error) {
	var fixture *OwnedProjectIndex
	if len(owned) > 0 {
		fixture = owned[0]
	}
	return DiagnoseHostedProjectWithRepair(ctx, state, target, project, epoch, investigate, logs, fixture, nil, nil)
}

func DiagnoseHostedProjectWithRepair(ctx context.Context, state *pgx.Conn, target *pgx.ConnConfig, project supabaseproject.Project,
	epoch int64, investigate EnrolledLookupInvestigator, logs *TrafficPlanReader, fixture *OwnedProjectIndex,
	repair *OwnedProjectRepair, cron *OwnedProjectCron) (*DispatchResult, error) {
	return diagnoseLookup(ctx, state, project, epoch, investigate, logs, fixture, repair, cron, func(ctx context.Context) (*pgx.Conn, func(), error) {
		conn, err := pgx.ConnectConfig(ctx, target)
		return conn, func() { labdb.Close(conn) }, err
	})
}

func diagnoseLookup(ctx context.Context, state *pgx.Conn, project supabaseproject.Project, epoch int64, investigate EnrolledLookupInvestigator,
	logs *TrafficPlanReader, fixture *OwnedProjectIndex, repair *OwnedProjectRepair, cron *OwnedProjectCron,
	targetSession func(context.Context) (*pgx.Conn, func(), error)) (*DispatchResult, error) {
	if epoch <= 0 || investigate == nil {
		return nil, ErrProjectDisconnected
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, investigationTimeout)
	defer cancel()
	var scope []ownedIndexRecoveryScope
	if fixture.Matches(project) {
		scope = append(scope, ownedIndexRecoveryScope{Database: fixture.Target.DatabaseOID, Role: fixture.OwnerRoleOID, Query: fixture.QueryID})
	}
	if err := cron.ensureClaim(ctx, state, project, epoch); err != nil {
		return nil, err
	}
	if err := repair.ensureCleanupClaim(ctx, state, project, epoch); err != nil {
		return nil, err
	}
	claim, err := claimProjectInvestigation(ctx, state, project.ID(), &epoch, scope...)
	if err != nil || claim == nil {
		return nil, err
	}
	result := DispatchResult{JobID: claim.ID, Attempt: claim.Attempt, Outcome: "insufficient_evidence", DiagnosticReason: "lookup_grant_or_fresh_windows_unavailable"}
	recovered, err := fixture.recover(ctx, state, project, *claim, &result)
	if err != nil {
		return nil, err
	}
	if claim.RecoveryOnly && !recovered {
		return nil, errClaimLost
	}
	windows := observedLookupLatency(claim.queueEvidence, started)
	if !recovered {
		report, err := cron.dispatch(ctx, state, project, epoch, *claim, windows, targetSession)
		if err != nil || report != nil {
			return report, err
		}
		report, err = repair.dispatchCleanup(ctx, state, project, epoch, *claim, windows, targetSession)
		if err != nil || report != nil {
			return report, err
		}
	}
	if !recovered && windows != nil && len(windows.SharedBlockAccesses) == 5 {
		target, closeTarget, err := targetSession(ctx)
		if err != nil {
			return nil, err
		}
		defer closeTarget()
		bound, err := fixture.bindClaim(ctx, target, project, *claim, windows.ObjectiveMS)
		if err != nil {
			return nil, err
		}
		if bound != nil {
			err = bound.run(ctx, state, target, project, epoch, *claim, windows, &result)
		} else if repair.matchesClaim(project, *claim) && repair.Cleanup == nil {
			err = repair.run(ctx, state, target, project, epoch, *claim, windows, &result)
		} else {
			err = diagnoseProjectClaim(ctx, state, target, project, epoch, *claim, windows, investigate, logs, &result)
		}
		if err != nil {
			return nil, err
		}
	}
	result.ElapsedMS = elapsedMS(started)
	if result.continuationAt != nil {
		if _, err := continueInvestigation(ctx, state, *claim, result, *result.continuationAt); err != nil {
			return nil, err
		}
	} else if err := finishInvestigation(ctx, state, *claim, result, epoch); err != nil {
		return nil, err
	}
	return &result, nil
}

func diagnoseProjectClaim(ctx context.Context, state, target *pgx.Conn, project supabaseproject.Project, epoch int64,
	claim investigationClaim, windows *ObservedLookupLatency, investigate EnrolledLookupInvestigator, logs *TrafficPlanReader, result *DispatchResult) error {
	var evidence LookupEvidence
	var grant lookupGrant
	var trafficGrant *trafficPlanGrant
	if logs != nil {
		var err error
		trafficGrant, err = readProjectTrafficGrant(ctx, target, project, claim, windows.ObjectiveMS)
		if err != nil {
			return err
		}
		if trafficGrant != nil {
			now := time.Now().UTC()
			start := now.Add(-trafficPlanAge)
			if trafficGrant.grantedAt.After(start) {
				start = trafficGrant.grantedAt
			}
			for _, at := range claim.Generation {
				if at.After(start) {
					start = at
				}
			}
			plan, providerStatus := logs.read(ctx, project.Ref(), trafficGrant.login, claim.Query, start, now)
			result.TrafficPlanStatus = providerStatus
			if plan != nil {
				grant = lookupGrant{tableOID: trafficGrant.tableOID, column: trafficGrant.column}
				evidence = LookupEvidence{TrafficPlan: plan, ObjectiveMS: windows.ObjectiveMS,
					WindowMeansMS: windows.WindowMeansMS, WindowCalls: windows.WindowCalls, SharedBlockAccesses: windows.SharedBlockAccesses}
			} else {
				result.DiagnosticReason = "traffic_plan_unavailable"
			}
		}
	}
	if evidence.TrafficPlan == nil {
		var err error
		grant, evidence, err = readProjectLookup(ctx, target, project, claim, windows)
		if errors.Is(err, errLookupScope) {
			// Supabase's log index can lag normal traffic. Give a plan-only
			// job one delayed read without holding the worker or calling Sonnet.
			if trafficGrant != nil && claim.ParentID == 0 && claim.Attempt == 1 {
				next := time.Now().Add(20 * time.Second)
				result.Outcome, result.continuationAt = "awaiting_evidence", &next
			}
			return nil
		}
		if err != nil {
			return err
		}
		if !grant.discovered {
			evidence.MeasuredPlan, err = readProjectLookupPlan(ctx, target, project, claim.Role, claim.Query)
			if err != nil {
				return err
			}
		}
	}

	recheckEvidence := func(ctx context.Context) error {
		if evidence.TrafficPlan != nil {
			current, err := readProjectTrafficGrant(ctx, target, project, claim, windows.ObjectiveMS)
			if err != nil {
				return err
			}
			if current == nil || *current != *trafficGrant || time.Since(evidence.TrafficPlan.CapturedAt) > trafficPlanAge {
				return errLookupScope
			}
			return nil
		}
		current, _, err := readProjectLookup(ctx, target, project, claim, windows)
		if err != nil {
			return err
		}
		if current != grant {
			return errLookupScope
		}
		if evidence.MeasuredPlan != nil {
			plan, err := readProjectLookupPlan(ctx, target, project, claim.Role, claim.Query)
			if err != nil {
				return err
			}
			if plan == nil || *plan != *evidence.MeasuredPlan {
				return errLookupScope
			}
		}
		return nil
	}

	recheck := func(ctx context.Context) error {
		if err := recheckEvidence(ctx); err != nil {
			return err
		}
		var active bool
		err := state.QueryRow(ctx, `
			SELECT EXISTS (SELECT FROM pooltune_worker.projects p JOIN pooltune_worker.investigations j USING(project_id)
				WHERE p.project_id=$1 AND p.monitor_epoch=$2 AND j.id=$3 AND j.attempts=$4
				AND j.finished_at IS NULL AND j.lease_until>clock_timestamp())
		`, project.ID(), epoch, claim.ID, claim.Attempt).Scan(&active)
		if err == nil && !active {
			return errClaimLost
		}
		return err
	}

	if evidence.TrafficPlan != nil {
		if err := recheck(ctx); err != nil {
			if errors.Is(err, errLookupScope) {
				return nil
			}
			return err
		}
	}

	runID := fmt.Sprintf("project-lookup-%d-%d", claim.ID, claim.Attempt)
	diagnosis, err := investigate(ctx, runID, grant.tableOID, grant.column, time.Now().UTC(), evidence, recheck)
	if errors.Is(err, ErrProjectModelBudget) {
		result.Outcome, result.DiagnosticReason = "diagnosis_unavailable", "daily_model_limit"
		return nil
	}
	if errors.Is(err, errLookupScope) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := recheck(ctx); err != nil {
		if errors.Is(err, errLookupScope) {
			return nil
		}
		return err
	}
	if diagnosis == nil {
		result.Outcome, result.DiagnosticReason = "diagnosis_unavailable", "model_did_not_return_diagnosis"
		return nil
	}
	result.Outcome, result.DiagnosticReason, result.ThreadID = "diagnosed", "", runID
	result.ObservedLatency, result.LookupDiagnosis, result.LookupPlan = windows, diagnosis, evidence.MeasuredPlan
	result.TrafficPlan = evidence.TrafficPlan
	return nil
}
