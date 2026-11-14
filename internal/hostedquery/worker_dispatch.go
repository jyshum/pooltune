package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const maxInvestigationAttempts = 2
const investigationLease = 30 * time.Second
const investigationTimeout = 25 * time.Second

var errClaimLost = errors.New("investigation claim expired or was replaced")

type investigationClaim struct {
	InvestigationJob
	Attempt int
	Project string
}

type DispatchResult struct {
	JobID        int64          `json:"job_id"`
	Attempt      int            `json:"attempt"`
	Outcome      string         `json:"outcome"`
	ThreadID     string         `json:"thread_id,omitempty"`
	Evidence     *IndexEvidence `json:"evidence,omitempty"`
	Proposal     *IndexProposal `json:"proposal,omitempty"`
	ElapsedMS    float64        `json:"elapsed_ms"`
	TargetPlanMS float64        `json:"target_plan_ms,omitempty"`
	GraphMS      float64        `json:"graph_ms,omitempty"`
}

// InvestigationEvidence supplies fresh evidence for this exact enrolled job.
type InvestigationEvidence func(context.Context, InvestigationJob) (IndexEvidence, error)

func claimInvestigation(ctx context.Context, state *pgx.Conn, project string) (*investigationClaim, error) {
	claim := investigationClaim{Project: project}
	err := pgx.BeginTxFunc(ctx, state, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		var locked string
		if err := tx.QueryRow(ctx, "SELECT project_id FROM pooltune_worker.projects WHERE project_id=$1 FOR UPDATE", project).Scan(&locked); err != nil {
			return err
		}
		// A new statement after the project lock sees a competing consumer's committed claim.
		err := tx.QueryRow(ctx, `
			WITH exhausted AS (
				UPDATE pooltune_worker.investigations
				SET finished_at=statement_timestamp(), lease_until=NULL,
				    result=jsonb_build_object('outcome','retry_exhausted')
				WHERE project_id=$1 AND finished_at IS NULL AND attempts >= $2
				  AND lease_until <= statement_timestamp()
			), available AS (
				SELECT id FROM pooltune_worker.investigations
				WHERE project_id=$1 AND finished_at IS NULL AND attempts < $2
				  AND (lease_until IS NULL OR lease_until <= statement_timestamp())
				  AND NOT EXISTS (
					SELECT FROM pooltune_worker.investigations
					WHERE project_id=$1 AND finished_at IS NULL AND lease_until>statement_timestamp()
				  )
				ORDER BY id LIMIT 1
			)
			UPDATE pooltune_worker.investigations AS jobs
			SET attempts=jobs.attempts+1, lease_until=clock_timestamp()+make_interval(secs=>$3)
			FROM available WHERE jobs.id=available.id
			RETURNING jobs.id,jobs.database_oid,jobs.role_oid,jobs.query_id,jobs.attempts,
			          jobs.evidence->'counter_generation'
		`, project, maxInvestigationAttempts, investigationLease.Seconds()).
			Scan(&claim.ID, &claim.Database, &claim.Role, &claim.Query, &claim.Attempt, &claim.Generation)
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

func finishInvestigation(ctx context.Context, state *pgx.Conn, claim investigationClaim, result DispatchResult) error {
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	tag, err := state.Exec(ctx, `
		WITH locked_project AS MATERIALIZED (
			SELECT project_id FROM pooltune_worker.projects WHERE project_id=$4 FOR UPDATE
		)
		UPDATE pooltune_worker.investigations SET finished_at=clock_timestamp(), lease_until=NULL, result=$3::jsonb
		WHERE project_id=(SELECT project_id FROM locked_project) AND id=$1 AND attempts=$2
		  AND finished_at IS NULL AND lease_until>clock_timestamp()
	`, claim.ID, claim.Attempt, string(payload), claim.Project)
	if err == nil && tag.RowsAffected() != 1 {
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
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, investigationTimeout)
	defer cancel()
	claim, err := claimInvestigation(ctx, state, project)
	if err != nil || claim == nil {
		return nil, err
	}
	result := DispatchResult{JobID: claim.ID, Attempt: claim.Attempt}
	evidence, err := collect(ctx, claim.InvestigationJob)
	if err != nil {
		return nil, fmt.Errorf("collect investigation evidence: %w", err)
	}
	now := time.Now()
	if evidence.Decision != Investigate || evidence.At.IsZero() || evidence.At.After(now) ||
		now.Sub(evidence.At) > 30*time.Second || evidence.TableOID == 0 || evidence.Column <= 0 ||
		evidence.Label != "lookup" || evidence.Plan.Node == "" {
		result.Outcome = "insufficient_evidence"
	} else {
		evidence.RunID = fmt.Sprintf("worker-%d-%d", claim.ID, claim.Attempt)
		beforeGraph := time.Now()
		proposal, err := investigate(ctx, evidence)
		result.GraphMS = elapsedMS(beforeGraph)
		if err != nil {
			return nil, fmt.Errorf("invoke investigator: %w", err)
		}
		if (proposal.Outcome != "propose" && proposal.Outcome != "no_change") ||
			proposal.RunID != evidence.RunID || proposal.TableOID != evidence.TableOID ||
			proposal.Column != evidence.Column || len(proposal.Reason) == 0 || len(proposal.Reason) > 1000 {
			return nil, errors.New("graph response does not match the claimed investigation")
		}
		result.TargetPlanMS = evidence.ExecutionMS
		result.Outcome, result.ThreadID, result.Evidence, result.Proposal = proposal.Outcome, evidence.RunID, &evidence, &proposal
	}
	result.ElapsedMS = elapsedMS(started)
	if err := finishInvestigation(ctx, state, *claim, result); err != nil {
		return nil, err
	}
	result.ElapsedMS = elapsedMS(started)
	return &result, nil
}
