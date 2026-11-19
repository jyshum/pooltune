package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

const maxInvestigationAttempts = 2
const maxOwnedIndexRecoveryAttempts = 4
const investigationLease = 30 * time.Second
const investigationTimeout = 25 * time.Second

var errClaimLost = errors.New("investigation claim expired or was replaced")

type investigationClaim struct {
	InvestigationJob
	ParentID             int64
	Attempt              int
	Project              string
	RecoveryOnly         bool
	MaintenanceOperation string
	queueEvidence        []byte
}

// ownedIndexRecoveryScope comes from the active, explicitly enabled fixture.
// A zero query permits discovery within the exact database and role.
type ownedIndexRecoveryScope struct {
	Database, Role uint32
	Query          int64
	Managed        bool
}

type DispatchResult struct {
	ManagedCron               *ManagedCronReport       `json:"managed_cron,omitempty"`
	ManagedMemory             *ManagedMemoryReport     `json:"managed_memory,omitempty"`
	ManagedStatistics         *ManagedStatisticsReport `json:"managed_statistics,omitempty"`
	ManagedCleanup            *ManagedCleanupReport    `json:"managed_cleanup,omitempty"`
	managedCleanupPublication *managedCleanupPublication
	ManagedMaintenance        *ManagedMaintenanceReport `json:"managed_maintenance,omitempty"`
	TrafficPlanStatus         string                    `json:"traffic_plan_status,omitempty"`
	TrafficPlan               *TrafficPlan              `json:"traffic_plan,omitempty"`
	LookupPlan                *LookupMeasuredPlan       `json:"lookup_plan,omitempty"`
	LookupDiagnosis           *LookupDiagnosis          `json:"lookup_diagnosis,omitempty"`
	JobID                     int64                     `json:"job_id"`
	Attempt                   int                       `json:"attempt"`
	Outcome                   string                    `json:"outcome"`
	ThreadID                  string                    `json:"thread_id,omitempty"`
	Estimate                  *EstimatedLookupEvidence  `json:"estimated_evidence,omitempty"`
	ObservedLatency           *ObservedLookupLatency    `json:"observed_latency,omitempty"`
	DiagnosticReason          string                    `json:"diagnostic_reason,omitempty"`
	Evidence                  *IndexEvidence            `json:"evidence,omitempty"`
	Proposal                  *IndexProposal            `json:"proposal,omitempty"`
	ElapsedMS                 float64                   `json:"elapsed_ms"`
	TargetPlanMS              float64                   `json:"target_plan_ms,omitempty"`
	RecoveredIndex            string                    `json:"recovered_index,omitempty"`
	Cron                      *CronReport               `json:"cron,omitempty"`
	Memory                    *MemoryReport             `json:"memory,omitempty"`
	Vacuum                    *VacuumReport             `json:"vacuum,omitempty"`
	Statistics                *StatisticsReport         `json:"statistics,omitempty"`
	Analyze                   *AnalyzeReport            `json:"analyze,omitempty"`
	Repair                    *IndexTrialReport         `json:"repair,omitempty"`
	IndexRecovery             *ownedIndexRecovery       `json:"index_recovery,omitempty"`
	ownedOperation            string
	managedRecoveryIntent     json.RawMessage
	continuationAt            *time.Time
	GraphMS                   float64 `json:"graph_ms,omitempty"`
}

// InvestigationEvidence supplies fresh evidence for this exact enrolled job.
type InvestigationEvidence func(context.Context, InvestigationJob) (IndexEvidence, error)

func claimInvestigation(ctx context.Context, state *pgx.Conn, project string) (*investigationClaim, error) {
	return claimProjectInvestigation(ctx, state, project, nil)
}

func claimProjectInvestigation(ctx context.Context, state *pgx.Conn, project string, epoch *int64,
	recovery ...ownedIndexRecoveryScope) (*investigationClaim, error) {
	if epoch != nil && *epoch <= 0 {
		return nil, nil
	}
	var scope ownedIndexRecoveryScope
	if epoch != nil && len(recovery) == 1 && ((recovery[0].Database != 0 && recovery[0].Role != 0) || recovery[0].Managed) {
		scope = recovery[0]
	}
	claim := investigationClaim{Project: project}
	err := pgx.BeginTxFunc(ctx, state, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		var locked string
		if err := tx.QueryRow(ctx, "SELECT project_id FROM pooltune_worker.projects WHERE project_id=$1 AND NOT agent_paused AND monitor_epoch IS NOT DISTINCT FROM $2::bigint FOR UPDATE", project, epoch).Scan(&locked); err != nil {
			return err
		}
		// A new statement after the project lock sees a competing consumer's committed claim.
		err := tx.QueryRow(ctx, `
			WITH open_jobs AS MATERIALIZED (
				SELECT j.id, j.attempts, j.not_before, j.lease_until, (a.job_id IS NOT NULL OR ($8::boolean AND b.job_id IS NOT NULL AND ((b.operation IN ('analyze','vacuum') AND b.intent->>'RunID' LIKE 'managed-maintenance-%') OR (b.operation='statistics' AND b.intent->>'RunID' LIKE 'managed-statistics-%') OR (b.operation='memory' AND b.intent->>'run_id' LIKE 'managed-memory-%') OR (b.operation='cron' AND b.intent->>'RunID' LIKE 'managed-cron-%')))) AS has_pending_index,
				       (a.job_id IS NOT NULL OR b.job_id IS NOT NULL) AS has_pending_action,
				       CASE WHEN $8::boolean AND ((b.operation IN ('analyze','vacuum') AND b.intent->>'RunID' LIKE 'managed-maintenance-%') OR (b.operation='statistics' AND b.intent->>'RunID' LIKE 'managed-statistics-%') OR (b.operation='memory' AND b.intent->>'run_id' LIKE 'managed-memory-%') OR (b.operation='cron' AND b.intent->>'RunID' LIKE 'managed-cron-%')) THEN b.operation ELSE '' END AS maintenance_operation,
				       a.job_id IS NOT NULL AND ($8::boolean OR ($4::oid<>0 AND $5::oid<>0
				         AND j.database_oid=$4::oid AND j.role_oid=$5::oid
				         AND ($6::bigint=0 OR j.query_id=$6))) OR ($8::boolean AND b.job_id IS NOT NULL AND ((b.operation IN ('analyze','vacuum') AND b.intent->>'RunID' LIKE 'managed-maintenance-%') OR (b.operation='statistics' AND b.intent->>'RunID' LIKE 'managed-statistics-%') OR (b.operation='memory' AND b.intent->>'run_id' LIKE 'managed-memory-%') OR (b.operation='cron' AND b.intent->>'RunID' LIKE 'managed-cron-%'))) AS recovery_allowed
				FROM pooltune_worker.investigations j
				LEFT JOIN pooltune_worker.owned_index_actions a ON a.job_id=j.id AND a.project_id=j.project_id AND a.outcome='pending'
				LEFT JOIN pooltune_worker.owned_project_actions b ON b.job_id=j.id AND b.project_id=j.project_id AND b.outcome='pending'
				WHERE j.project_id=$1 AND j.finished_at IS NULL
			), exhausted AS (
				UPDATE pooltune_worker.investigations j
				SET finished_at=statement_timestamp(), lease_until=NULL,
				    result=jsonb_build_object('outcome','retry_exhausted')
				FROM open_jobs o
				WHERE j.id=o.id AND NOT o.has_pending_action AND o.attempts >= $2
				  AND o.lease_until <= statement_timestamp()
			), available AS (
				SELECT id, has_pending_index, maintenance_operation FROM open_jobs
				WHERE ((NOT has_pending_index AND attempts < $2) OR (recovery_allowed AND attempts < $7))
				  AND (NOT $8::boolean OR has_pending_index OR NOT EXISTS (SELECT FROM open_jobs WHERE has_pending_action))
				  AND not_before <= statement_timestamp()
				  AND (lease_until IS NULL OR lease_until <= statement_timestamp())
				  AND NOT EXISTS (
					SELECT FROM open_jobs WHERE lease_until>statement_timestamp()
				  )
				ORDER BY id LIMIT 1
			)
			UPDATE pooltune_worker.investigations AS jobs
			SET attempts=jobs.attempts+1, lease_until=clock_timestamp()+make_interval(secs=>$3),
			    claimed_at=COALESCE(jobs.claimed_at, CASE WHEN jobs.attempts=0 THEN clock_timestamp() END)
			FROM available WHERE jobs.id=available.id
			RETURNING jobs.id,jobs.database_oid,jobs.role_oid,jobs.query_id,jobs.attempts,
			          jobs.evidence->'counter_generation',jobs.evidence,COALESCE(jobs.parent_id,0),available.has_pending_index,available.maintenance_operation
		`, project, maxInvestigationAttempts, investigationLease.Seconds(), scope.Database, scope.Role, scope.Query, maxOwnedIndexRecoveryAttempts, scope.Managed).
			Scan(&claim.ID, &claim.Database, &claim.Role, &claim.Query, &claim.Attempt, &claim.Generation, &claim.queueEvidence,
				&claim.ParentID, &claim.RecoveryOnly, &claim.MaintenanceOperation)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && claim.ID == 0) {
		return nil, nil
	}
	return &claim, err
}

func finishInvestigation(ctx context.Context, state *pgx.Conn, claim investigationClaim, result DispatchResult, expectedEpoch ...int64) error {
	maintenanceRecovery := claim.RecoveryOnly && claim.MaintenanceOperation != "" && result.ownedOperation == claim.MaintenanceOperation && (result.ManagedMaintenance != nil || (claim.MaintenanceOperation == "memory" && result.ManagedMemory != nil)) && result.Outcome == "repair_interrupted" && json.Valid(result.managedRecoveryIntent)
	if claim.RecoveryOnly && result.IndexRecovery == nil && !maintenanceRecovery {
		return errClaimLost
	}
	var epoch *int64
	if len(expectedEpoch) > 0 {
		epoch = &expectedEpoch[0]
	}
	if result.ownedOperation != "" && (epoch == nil ||
		(!maintenanceRecovery && result.Outcome != "improved_tested_inputs" && result.Outcome != "inconclusive" && result.Outcome != "regressed")) {
		return errClaimLost
	}
	terminalOwnedIndex := ""
	var recordedIntent *string
	if recovery := result.IndexRecovery; recovery != nil {
		if recovery.Status == "action_active" {
			return errOwnedIndexActionActive
		}
		if epoch == nil || result.Outcome != "repair_interrupted" || !recovery.settled() ||
			!json.Valid(recovery.intent) || recovery.Reason == "" || result.Repair != nil {
			return errors.New("index recovery publication requires a settled observation, recorded intent and active project")
		}
		terminalOwnedIndex = recovery.Status
		intent := string(recovery.intent)
		recordedIntent = &intent
	} else if result.Repair != nil && (result.Repair.Measurement == "owned_single_input" || result.Repair.Measurement == "managed_sampled_input") &&
		(result.Outcome == "improved_tested_inputs" || result.Outcome == "inconclusive" || result.Outcome == "regressed") {
		terminalOwnedIndex = result.Outcome
	}
	var maintenanceIntent *string
	if maintenanceRecovery {
		raw := string(result.managedRecoveryIntent)
		maintenanceIntent = &raw
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	var published bool
	err = state.QueryRow(ctx, `
		WITH locked_project AS MATERIALIZED (
			SELECT project_id FROM pooltune_worker.projects WHERE project_id=$4
			  AND ($5::bigint IS NULL OR monitor_epoch=$5) FOR UPDATE
		), pending_action AS MATERIALIZED (
			SELECT a.job_id, a.project_id
			FROM pooltune_worker.owned_index_actions a JOIN locked_project USING(project_id)
			WHERE $6<>'' AND a.job_id=$1 AND a.outcome='pending'
			  AND ($7::jsonb IS NULL OR a.intent=$7::jsonb)
			FOR UPDATE OF a
		), pending_project_action AS MATERIALIZED (
			SELECT a.job_id,a.project_id
			FROM pooltune_worker.owned_project_actions a JOIN locked_project USING(project_id)
			WHERE $8<>'' AND a.job_id=$1 AND a.operation=$8 AND a.outcome='pending'
			  AND ($9::jsonb IS NULL OR a.intent=$9::jsonb)
			FOR UPDATE OF a
		), published AS (
			UPDATE pooltune_worker.investigations SET finished_at=clock_timestamp(), lease_until=NULL, result=$3::jsonb
			WHERE project_id=(SELECT project_id FROM locked_project) AND id=$1 AND attempts=$2
			  AND finished_at IS NULL AND lease_until>clock_timestamp()
			  AND ($6='' OR EXISTS (SELECT FROM pending_action))
			  AND ($8='' OR EXISTS (SELECT FROM pending_project_action))
			RETURNING id,project_id
		), settled_owned_index AS (
			UPDATE pooltune_worker.owned_index_actions a SET outcome=$6
			FROM published p JOIN pending_action b ON b.job_id=p.id AND b.project_id=p.project_id
			WHERE a.job_id=b.job_id AND a.project_id=b.project_id
		), settled_owned_project_action AS (
			UPDATE pooltune_worker.owned_project_actions a SET outcome=($3::jsonb->>'outcome')
			FROM published p JOIN pending_project_action b ON b.job_id=p.id AND b.project_id=p.project_id
			WHERE a.job_id=b.job_id AND a.project_id=b.project_id
		)
		SELECT EXISTS (SELECT FROM published)
	`, claim.ID, claim.Attempt, string(payload), claim.Project, epoch, terminalOwnedIndex, recordedIntent, result.ownedOperation, maintenanceIntent).Scan(&published)
	if err == nil && !published {
		return errClaimLost
	}
	return err
}

// DispatchInvestigation processes at most one job. A failed attempt stays reserved
// until the lease expires. Attempts use distinct graph threads so stale state cannot
// provide a proposal for the retry's fresh evidence.
func DispatchInvestigation(ctx context.Context, state *pgx.Conn, project string, collect InvestigationEvidence, investigate IndexInvestigator) (*DispatchResult, error) {
	if project == "" || collect == nil || investigate == nil {
		return nil, errors.New("investigation needs a project, evidence source and graph")
	}
	return dispatchInvestigation(ctx, state, project, func(ctx context.Context, claim investigationClaim, result *DispatchResult) error {
		evidence, err := collect(ctx, claim.InvestigationJob)
		if err != nil {
			return fmt.Errorf("collect investigation evidence: %w", err)
		}
		return runIndexInvestigator(ctx, claim, result, evidence, investigate)
	})
}

func runIndexInvestigator(ctx context.Context, claim investigationClaim, result *DispatchResult, evidence IndexEvidence, investigate IndexInvestigator) error {
	now := time.Now()
	causes := 0
	for _, present := range []bool{evidence.Cron != nil, evidence.Memory != nil, evidence.Vacuum != nil, evidence.Statistics != nil, evidence.Analyze != nil} {
		if present {
			causes++
		}
	}
	if causes > 1 || (len(evidence.Columns) > 0 && causes > 0) || !ValidIndexColumns(evidence.Column, evidence.Columns) || evidence.Decision != Investigate || evidence.At.IsZero() || evidence.At.After(now) ||
		now.Sub(evidence.At) > 30*time.Second || evidence.TableOID == 0 || evidence.Column <= 0 ||
		evidence.Label != "lookup" || evidence.Plan.Node == "" {
		result.Outcome = "insufficient_evidence"
		return nil
	}
	evidence.RunID = fmt.Sprintf("worker-%d-%d", claim.ID, claim.Attempt)
	beforeGraph := time.Now()
	proposal, err := investigate(ctx, evidence)
	result.GraphMS = elapsedMS(beforeGraph)
	if err != nil {
		return fmt.Errorf("invoke investigator: %w", err)
	}
	if (proposal.Outcome != "propose" && proposal.Outcome != "no_change") ||
		(proposal.Repair != "" && proposal.Repair != expectedRepair(evidence)) ||
		(proposal.Outcome == "propose" && proposal.Repair != expectedRepair(evidence)) ||
		proposal.RunID != evidence.RunID || proposal.TableOID != evidence.TableOID ||
		proposal.Column != evidence.Column || !slices.Equal(proposal.Columns, evidence.Columns) || len(proposal.Reason) == 0 || len(proposal.Reason) > 1000 {
		return errors.New("graph response does not match the claimed investigation")
	}
	result.TargetPlanMS = evidence.ExecutionMS
	result.Outcome, result.ThreadID, result.Evidence, result.Proposal = proposal.Outcome, evidence.RunID, &evidence, &proposal
	return nil
}

func expectedRepair(evidence IndexEvidence) string {
	if evidence.Cron != nil {
		return "cron"
	}
	if evidence.Memory != nil {
		return "memory"
	}
	if evidence.Vacuum != nil {
		return "vacuum"
	}
	if evidence.Statistics != nil {
		return "statistics"
	}
	if evidence.Analyze != nil {
		return "analyze"
	}
	return ""
}

// DispatchEstimatedInvestigation stores advisory evidence and cannot invoke the repair graph.
func DispatchEstimatedInvestigation(ctx context.Context, state *pgx.Conn, project string, collect func(context.Context, InvestigationJob) (*EstimatedLookupEvidence, error)) (*DispatchResult, error) {
	if collect == nil {
		return nil, errors.New("estimate needs an evidence reader")
	}
	return dispatchEnrolledInvestigation(ctx, state, project,
		func(ctx context.Context, job InvestigationJob, _ func(*EstimatedLookupEvidence) bool) (*EstimatedLookupEvidence, *IndexEvidence, error) {
			estimate, err := collect(ctx, job)
			return estimate, nil, err
		}, nil)
}

// DispatchMeasuredStagingLookup can recommend an index for one marked staging fixture.
func DispatchMeasuredStagingLookup(ctx context.Context, state *pgx.Conn, config *pgx.ConnConfig, enrollment LookupEnrollment, investigate IndexInvestigator) (*DispatchResult, error) {
	if investigate == nil || !stagingLookup(enrollment) || enrollment.Validate() != nil {
		return nil, errors.New("measurement needs an enrolled staging fixture and investigator")
	}
	return dispatchEnrolledInvestigation(ctx, state, enrollment.Project,
		func(ctx context.Context, job InvestigationJob, admit func(*EstimatedLookupEvidence) bool) (*EstimatedLookupEvidence, *IndexEvidence, error) {
			return enrolledLookupEvidence(ctx, config, enrollment, job, admit)
		}, investigate)
}

type enrolledEvidenceReader func(context.Context, InvestigationJob, func(*EstimatedLookupEvidence) bool) (*EstimatedLookupEvidence, *IndexEvidence, error)

func dispatchEnrolledInvestigation(ctx context.Context, state *pgx.Conn, project string, collect enrolledEvidenceReader, investigate IndexInvestigator) (*DispatchResult, error) {
	if project == "" {
		return nil, errors.New("enrolled investigation needs a project")
	}
	return dispatchInvestigation(ctx, state, project, func(ctx context.Context, claim investigationClaim, result *DispatchResult) error {
		estimate, measured, err := collect(ctx, claim.InvestigationJob, func(estimate *EstimatedLookupEvidence) bool {
			return investigate != nil && claim.Attempt == 1 && !estimate.CoveringIndex && estimate.Estimated.Node == "Seq Scan" && observedLookupLatency(claim.queueEvidence, estimate.ObservedAt) != nil
		})
		if err != nil {
			return err
		}
		result.Outcome = "insufficient_evidence"
		if estimate == nil {
			return nil
		}
		result.Estimate = estimate
		result.Outcome = "needs_measured_evidence"
		result.ObservedLatency = observedLookupLatency(claim.queueEvidence, estimate.ObservedAt)
		switch {
		case estimate.CoveringIndex:
			result.DiagnosticReason = "matching_index_exists"
		case result.ObservedLatency == nil:
			result.DiagnosticReason = "missing_fresh_latency"
		case estimate.Estimated.Node != "Seq Scan":
			result.DiagnosticReason = "no_estimated_sequential_scan"
		case investigate != nil && claim.Attempt > 1:
			result.DiagnosticReason = "measurement_already_attempted"
		case measured == nil:
			result.DiagnosticReason = "missing_measured_plan"
		default:
			return runIndexInvestigator(ctx, claim, result, *measured, investigate)
		}
		return nil
	})
}

func dispatchInvestigation(ctx context.Context, state *pgx.Conn, project string, evaluate func(context.Context, investigationClaim, *DispatchResult) error) (*DispatchResult, error) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, investigationTimeout)
	defer cancel()
	claim, err := claimInvestigation(ctx, state, project)
	if err != nil || claim == nil {
		return nil, err
	}
	result := DispatchResult{JobID: claim.ID, Attempt: claim.Attempt}
	if err := evaluate(ctx, *claim, &result); err != nil {
		return nil, err
	}
	result.ElapsedMS = elapsedMS(started)
	if result.continuationAt != nil {
		_, err = continueInvestigation(ctx, state, *claim, result, *result.continuationAt)
	} else {
		err = finishInvestigation(ctx, state, *claim, result)
	}
	if err != nil {
		return nil, err
	}
	result.ElapsedMS = elapsedMS(started)
	return &result, nil
}
