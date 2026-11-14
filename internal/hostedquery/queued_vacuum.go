package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func DispatchFixtureVacuum(ctx context.Context, state *pgx.Conn, config *pgx.ConnConfig, route string, fixture ConsumerFixture, investigate IndexInvestigator) (*DispatchResult, error) {
	if investigate == nil || ValidateConsumerFixtureID(fixture.ID) != nil || fixture.Enrollment.Project != route+"/consumer/"+fixture.ID {
		return nil, errors.New("queued VACUUM requires an exact synthetic fixture and investigator")
	}
	return dispatchInvestigation(ctx, state, fixture.Enrollment.Project, func(ctx context.Context, claim investigationClaim, result *DispatchResult) error {
		result.Outcome = "insufficient_evidence"
		var status string
		var saved []byte
		binding := journalTargetConfig(config, route)
		err := state.QueryRow(ctx, "SELECT status,report FROM "+journalTable+" WHERE id=$1 AND target=$2", fixture.ID, binding).Scan(&status, &saved)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var report VacuumReport
		if saved == nil || json.Unmarshal(saved, &report) != nil || report.Fixture != fixture.ID || report.IndexOID == 0 {
			return nil
		}
		if status != "vacuum_ready" {
			if report.JobID != claim.ID {
				return nil
			}
			switch status {
			case "vacuum_verified", "vacuum_declined":
				saveVacuum(result, claim, &report)
			case "vacuum_pending":
				target, err := pgx.ConnectConfig(ctx, config)
				if err != nil {
					return err
				}
				defer closeVacuumConnection(target)
				if err := lockTrial(ctx, target); err != nil {
					return err
				}
				expected, err := expectedFixture(ctx, state, fixture.ID)
				if err != nil {
					return err
				}
				report.After, err = sampleVacuum(ctx, target, fixture.ID, expected, report.IndexOID)
				if err != nil {
					return err
				}
				report.Outcome = "repair_interrupted"
				report.Reason = "prior VACUUM completion is uncertain; inspected current visibility without repeating cleanup"
				// Preserve pending journal state. Inspection cannot prove which process performed cleanup.
				saveVacuum(result, claim, &report)
			}
			return nil
		}
		if claim.Attempt != 1 || len(claim.Generation) != 3 || observedLookupLatency(claim.queueEvidence, time.Now()) == nil {
			return nil
		}
		target, err := pgx.ConnectConfig(ctx, config)
		if err != nil {
			return err
		}
		defer closeVacuumConnection(target)
		if err := lockTrial(ctx, target); err != nil {
			return err
		}
		var ready bool
		if err := state.QueryRow(ctx, "SELECT EXISTS(SELECT FROM "+journalTable+" WHERE id=$1 AND target=$2 AND status='vacuum_ready')", fixture.ID, binding).Scan(&ready); err != nil || !ready {
			return err
		}
		expected, err := expectedFixture(ctx, state, fixture.ID)
		if err != nil {
			return err
		}
		if err := trialCapacity(ctx, target); err != nil {
			return err
		}
		match, err := vacuumLookupMatches(ctx, target, fixture.ID, expected, report.IndexOID, claim)
		if err != nil || !match {
			return err
		}
		report.JobID = claim.ID
		clear, err := vacuumConditions(ctx, target, expected.Candidate, 0)
		if err != nil {
			return err
		}
		if !clear {
			report.Outcome = "deferred"
			report.Reason = "active maintenance or a cleanup horizon prevents this bounded action"
			saveVacuum(result, claim, &report)
			return nil
		}
		start := time.Now()
		report.Before, err = sampleVacuum(ctx, target, fixture.ID, expected, report.IndexOID)
		if err != nil {
			return err
		}
		report.BaselineMS = elapsedMS(start)
		report.Evidence = IndexEvidence{At: time.Now().UTC(), Decision: Investigate, TableOID: expected.Candidate, Column: 2, Label: "lookup", Existing: true,
			Plan:   PlanEvidence{Node: report.Before.Plan, ExaminedRows: report.Before.PlanExamined, ReturnedRows: report.Before.ScanRows, SharedBlocks: int64(report.Before.Blocks)},
			Vacuum: &VacuumEvidence{HeapFetches: report.Before.HeapFetches, ScanRows: report.Before.ScanRows, ConditionsClear: clear}}
		if !vacuumCause(report.Evidence) {
			report.Outcome = "no_change"
			report.Reason = "current measured visibility does not justify cleanup"
			if err := writeVacuumStatus(ctx, state, "vacuum_declined", &report); err != nil {
				return err
			}
			saveVacuum(result, claim, &report)
			return nil
		}
		if err := runIndexInvestigator(ctx, claim, result, report.Evidence, investigate); err != nil {
			return err
		}
		report.Evidence, report.Proposal, report.GraphMS = *result.Evidence, *result.Proposal, result.GraphMS
		if !qualifyVacuum(report.Evidence, report.Proposal, expected.Candidate, time.Now()) {
			report.Outcome = "no_change"
			report.Reason = "the model declined or the evidence no longer supports bounded cleanup"
			if err := writeVacuumStatus(ctx, state, "vacuum_declined", &report); err != nil {
				return err
			}
			saveVacuum(result, claim, &report)
			return nil
		}
		if err := trialCapacity(ctx, target); err != nil {
			return err
		}
		if err := writeVacuumStatus(ctx, state, "vacuum_pending", &report); err != nil {
			return err
		}
		start = time.Now()
		applied, err := applyFixtureVacuum(ctx, target, config, fixture.ID, expected, report.IndexOID)
		if err != nil {
			return err
		}
		if !applied {
			report.Outcome = "no_change"
			report.Reason = "visibility or current cleanup conditions changed before execution"
			if err := writeVacuumStatus(ctx, state, "vacuum_declined", &report); err != nil {
				return err
			}
			saveVacuum(result, claim, &report)
			return nil
		}
		report.VacuumMS = elapsedMS(start)
		start = time.Now()
		report.After, err = sampleVacuum(ctx, target, fixture.ID, expected, report.IndexOID)
		if err != nil {
			return err
		}
		report.VerifyMS = elapsedMS(start)
		report.Outcome, report.Reason = verifyVacuum(report.Before, report.After)
		if err := writeVacuumStatus(ctx, state, "vacuum_verified", &report); err != nil {
			return err
		}
		saveVacuum(result, claim, &report)
		return nil
	})
}
func closeVacuumConnection(conn *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn.Close(ctx)
}
func vacuumLookupMatches(ctx context.Context, target *pgx.Conn, id string, expected fixtureObjects, index uint32, claim investigationClaim) (bool, error) {
	var matches bool
	err := pgx.BeginFunc(ctx, target, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL statement_timeout='750ms'; SET LOCAL max_parallel_workers_per_gather=0; SET LOCAL random_page_cost=1"); err != nil {
			return err
		}
		schema := trialSchema(id)
		if _, err := tx.Exec(ctx, "LOCK TABLE ONLY "+qualified(schema, "candidate")+", ONLY "+qualified(schema, "healthy")+" IN ACCESS SHARE MODE NOWAIT"); err != nil {
			return err
		}
		if err := validateVacuumFixture(ctx, tx, id, expected, index); err != nil {
			return err
		}
		var identity bool
		if err := tx.QueryRow(ctx, "SELECT current_user=session_user AND current_user::regrole::oid=$1 AND (SELECT oid FROM pg_database WHERE datname=current_database())=$2", claim.Role, claim.Database).Scan(&identity); err != nil || !identity {
			return err
		}
		p, err := readLookupPlan(ctx, tx, "EXPLAIN (FORMAT JSON, VERBOSE) "+vacuumQuery(schema), int32(1), int32(50000))
		if err != nil || p.QueryID != claim.Query || p.Node.Type != "Aggregate" || len(p.Node.Plans) != 1 || p.Node.Plans[0].Schema != schema || p.Node.Plans[0].Relation != "candidate" {
			return err
		}
		matches, err = currentLookupCounters(ctx, tx, EnrolledLookup{Database: claim.Database, Role: claim.Role, Query: claim.Query}, [3]time.Time(claim.Generation))
		return err
	})
	return matches, err
}
func writeVacuumStatus(ctx context.Context, state *pgx.Conn, status string, report *VacuumReport) error {
	raw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	tag, err := state.Exec(ctx, "UPDATE "+journalTable+" SET status=$2,report=$3::jsonb WHERE id=$1", report.Fixture, status, string(raw))
	if err == nil && tag.RowsAffected() != 1 {
		return errors.New("VACUUM journal row missing")
	}
	return err
}
func saveVacuum(result *DispatchResult, claim investigationClaim, report *VacuumReport) {
	result.Outcome, result.Vacuum = report.Outcome, report
	result.ObservedLatency = observedLookupLatency(claim.queueEvidence, report.Evidence.At)
	result.ThreadID, result.Evidence, result.Proposal, result.GraphMS = report.Evidence.RunID, &report.Evidence, &report.Proposal, report.GraphMS
}
