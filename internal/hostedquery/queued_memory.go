package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func DispatchFixtureMemory(ctx context.Context, state *pgx.Conn, config *pgx.ConnConfig, route string, fixture ConsumerFixture, investigate IndexInvestigator) (*DispatchResult, error) {
	if investigate == nil || ValidateConsumerFixtureID(fixture.ID) != nil || fixture.Enrollment.Project != route+"/consumer/"+fixture.ID {
		return nil, errors.New("queued memory repair requires an exact synthetic fixture and investigator")
	}
	return dispatchInvestigation(ctx, state, fixture.Enrollment.Project, func(ctx context.Context, claim investigationClaim, result *DispatchResult) (dispatchErr error) {
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
		var report MemoryReport
		if saved == nil || json.Unmarshal(saved, &report) != nil || report.Fixture != fixture.ID || report.Function.OID == 0 {
			return nil
		}
		if status != "memory_ready" {
			if report.JobID != claim.ID {
				return nil
			}
			switch status {
			case "memory_verified", "memory_declined":
				saveMemory(result, claim, &report)
			case "memory_pending":
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
				report.Restored, err = restoreMemory(ctx, target, fixture.ID, expected, &report)
				if err != nil {
					return err
				}
				report.Outcome = "repair_interrupted"
				report.Reason = "unverified memory change reconciled; restored only unchanged Pooltune-applied setting, preserving independent changes"
				if err := writeMemoryStatus(ctx, state, "memory_declined", &report); err != nil {
					return err
				}
				saveMemory(result, claim, &report)
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
		if err := state.QueryRow(ctx, "SELECT EXISTS(SELECT FROM "+journalTable+" WHERE id=$1 AND target=$2 AND status='memory_ready')", fixture.ID, binding).Scan(&ready); err != nil || !ready {
			return err
		}
		expected, err := expectedFixture(ctx, state, fixture.ID)
		if err != nil {
			return err
		}
		if err := trialCapacity(ctx, target); err != nil {
			return err
		}
		match, err := memoryLookupMatches(ctx, target, fixture.ID, expected, report.Function, claim)
		if err != nil || !match {
			return err
		}
		report.JobID = claim.ID
		current, err := readMemoryFunction(ctx, target, trialSchema(fixture.ID))
		if err != nil {
			return err
		}
		if !sameMemoryFunction(current, report.Function) || (current.WorkMem != memoryOriginal && current.WorkMem != memoryCandidate) {
			return errors.New("memory function identity or settings changed")
		}
		start := time.Now()
		report.Before, err = sampleMemory(ctx, target, fixture.ID, expected, report.Function, current.WorkMem)
		if err != nil {
			return err
		}
		report.BaselineMS = elapsedMS(start)
		report.Evidence = IndexEvidence{At: time.Now().UTC(), Decision: Investigate, TableOID: expected.Candidate, Column: 2, Label: "lookup",
			Plan:   PlanEvidence{Node: report.Before.Plan, ExaminedRows: report.Before.SortRows, ReturnedRows: report.Before.SortRows, SharedBlocks: int64(report.Before.Blocks)},
			Memory: &MemoryEvidence{FunctionOID: report.Function.OID, TempBytes: report.Before.TempBytes, InternalTempBytes: report.Before.InternalTempBytes, SortNodes: 1, MaxConcurrentCalls: 3, CandidateBytes: 32 * 1024 * 1024, AggregateBytes: 96 * 1024 * 1024, CurrentWorkMem: current.WorkMem, CandidateWorkMem: memoryCandidate, IdentityVerified: true}}
		if !memoryCause(report.Evidence) {
			report.Outcome = "no_change"
			report.Reason = "current attributed spill does not justify function memory"
			if err := writeMemoryStatus(ctx, state, "memory_declined", &report); err != nil {
				return err
			}
			saveMemory(result, claim, &report)
			return nil
		}
		if err := runIndexInvestigator(ctx, claim, result, report.Evidence, investigate); err != nil {
			return err
		}
		report.Evidence, report.Proposal, report.GraphMS = *result.Evidence, *result.Proposal, result.GraphMS
		if !qualifyMemory(report.Evidence, report.Proposal, expected.Candidate, time.Now()) {
			report.Outcome = "no_change"
			report.Reason = "the model declined or evidence no longer supports bounded function memory"
			if err := writeMemoryStatus(ctx, state, "memory_declined", &report); err != nil {
				return err
			}
			saveMemory(result, claim, &report)
			return nil
		}
		if err := trialCapacity(ctx, target); err != nil {
			return err
		}
		start = time.Now()
		applied, err := alterMemorySetting(ctx, target, fixture.ID, expected, report.Function, memoryOriginal, memoryCandidate, func() error { return writeMemoryStatus(ctx, state, "memory_pending", &report) })
		verified := false
		defer func() {
			if applied && !verified {
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				recovery, recoveryErr := pgx.ConnectConfig(cleanup, config)
				if recoveryErr == nil {
					defer closeVacuumConnection(recovery)
					report.Restored, recoveryErr = restoreMemory(cleanup, recovery, fixture.ID, expected, &report)
				}
				dispatchErr = errors.Join(dispatchErr, recoveryErr)
			}
		}()
		if err != nil {
			return err
		}
		if !applied {
			report.Outcome = "no_change"
			report.Reason = "function identity or settings changed before execution"
			if err := writeMemoryStatus(ctx, state, "memory_declined", &report); err != nil {
				return err
			}
			saveMemory(result, claim, &report)
			return nil
		}

		report.MemoryMS = elapsedMS(start)
		start = time.Now()
		report.After, err = sampleMemory(ctx, target, fixture.ID, expected, report.Function, memoryCandidate)
		if err != nil {
			return err
		}
		report.VerifyMS = elapsedMS(start)
		report.Outcome, report.Reason = verifyMemory(report.Before, report.After)
		if report.Outcome != "improved_tested_inputs" {
			report.Restored, err = restoreMemory(ctx, target, fixture.ID, expected, &report)
			if err != nil {
				return err
			}
		}
		if err := writeMemoryStatus(ctx, state, "memory_verified", &report); err != nil {
			return err
		}
		verified = true
		saveMemory(result, claim, &report)
		return nil
	})
}
func memoryLookupMatches(ctx context.Context, target *pgx.Conn, id string, objects fixtureObjects, fn MemoryIdentity, claim investigationClaim) (bool, error) {
	tx, err := target.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL statement_timeout='750ms'"); err != nil {
		return false, err
	}
	current, err := readMemoryFunction(ctx, tx, trialSchema(id))
	if err != nil {
		return false, err
	}
	if err := validateMemoryFixture(ctx, tx, id, objects, fn, current.WorkMem); err != nil {
		return false, err
	}
	var identity bool
	if err := tx.QueryRow(ctx, "SELECT current_user=session_user AND current_user::regrole::oid=$1 AND (SELECT oid FROM pg_database WHERE datname=current_database())=$2", claim.Role, claim.Database).Scan(&identity); err != nil || !identity {
		return false, err
	}
	p, err := readLookupPlan(ctx, tx, "EXPLAIN (FORMAT JSON, VERBOSE) "+memoryQuery(trialSchema(id)))
	if err != nil || p.QueryID != claim.Query || p.Node.Type != "Result" {
		return false, err
	}
	return currentLookupCounters(ctx, tx, EnrolledLookup{Database: claim.Database, Role: claim.Role, Query: claim.Query}, [3]time.Time(claim.Generation))
}
func writeMemoryStatus(ctx context.Context, state *pgx.Conn, status string, report *MemoryReport) error {
	raw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	tag, err := state.Exec(ctx, "UPDATE "+journalTable+" SET status=$2,report=$3::jsonb WHERE id=$1", report.Fixture, status, string(raw))
	if err == nil && tag.RowsAffected() != 1 {
		return errors.New("memory journal row missing")
	}
	return err
}
func saveMemory(result *DispatchResult, claim investigationClaim, report *MemoryReport) {
	result.Outcome, result.Memory = report.Outcome, report
	result.ObservedLatency = observedLookupLatency(claim.queueEvidence, report.Evidence.At)
	result.ThreadID, result.Evidence, result.Proposal, result.GraphMS = report.Evidence.RunID, &report.Evidence, &report.Proposal, report.GraphMS
}
