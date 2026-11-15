package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// DispatchFixtureRepair is an explicit lab mode for an owned synthetic fixture.
// The queue, proposal, action journal and measured workload refer to that fixture.
func DispatchFixtureRepair(ctx context.Context, state *pgx.Conn, config *pgx.ConnConfig, route string, fixture ConsumerFixture, investigate IndexInvestigator) (*DispatchResult, error) {
	return dispatchFixtureIndex(ctx, state, config, route, fixture, investigate, false)
}

// DispatchFixtureCompound uses the same queue and guards for the enrolled two-column fixture.
func DispatchFixtureCompound(ctx context.Context, state *pgx.Conn, config *pgx.ConnConfig, route string, fixture ConsumerFixture, investigate IndexInvestigator) (*DispatchResult, error) {
	return dispatchFixtureIndex(ctx, state, config, route, fixture, investigate, true)
}
func dispatchFixtureIndex(ctx context.Context, state *pgx.Conn, config *pgx.ConnConfig, route string, fixture ConsumerFixture, investigate IndexInvestigator, compound bool) (*DispatchResult, error) {
	readyStatus, verifiedStatus, declinedStatus, buildingStatus, recoveredStatus := "consumer_ready", "queued_verified", "queued_declined", "queued_building", "queued_recovered"
	definition := trialDefinition
	sample, collect, evidence, recover := sampleTrial, collectIndexCatalog, trialEvidence, recoverQueuedFixture
	if compound {
		readyStatus, verifiedStatus, declinedStatus, buildingStatus, recoveredStatus = "compound_ready", "compound_verified", "compound_declined", "compound_building", "compound_recovered"
		definition = compoundDefinition
		sample, collect, evidence, recover = sampleCompoundTrial, collectCompoundCatalog, compoundTrialEvidence, recoverQueuedCompound
	}

	if investigate == nil || ValidateConsumerFixtureID(fixture.ID) != nil ||
		fixture.Enrollment.Project != route+"/consumer/"+fixture.ID {
		return nil, errors.New("queued repair requires an exact synthetic fixture and investigator")
	}
	return dispatchInvestigation(ctx, state, fixture.Enrollment.Project, func(ctx context.Context, claim investigationClaim, result *DispatchResult) error {
		result.Outcome = "insufficient_evidence"
		binding := journalTargetConfig(config, route)
		var status string
		var recoveredIndex *string
		var previous []byte
		err := state.QueryRow(ctx, "SELECT status,report,recovered_index FROM "+journalTable+" WHERE id=$1 AND target=$2", fixture.ID, binding).Scan(&status, &previous, &recoveredIndex)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if status != readyStatus {
			var report IndexTrialReport
			if previous == nil || json.Unmarshal(previous, &report) != nil || report.JobID != claim.ID {
				return nil
			}
			switch status {
			case verifiedStatus, declinedStatus:
				result.ObservedLatency = observedLookupLatency(claim.queueEvidence, report.Evidence.At)
				saveFixtureRepair(result, report)
			case buildingStatus:
				target, err := pgx.ConnectConfig(ctx, config)
				if err != nil {
					return err
				}
				defer target.Close(ctx)
				observed, err := recover(ctx, target, state, fixture.ID)
				if err != nil {
					return err
				}
				result.RecoveredIndex = observed
				result.Outcome = "repair_interrupted"
				result.Repair = &report
			case recoveredStatus:
				if recoveredIndex != nil {
					result.RecoveredIndex = *recoveredIndex
				}
				result.Outcome = "repair_interrupted"
				result.Repair = &report
			}
			return nil
		}
		if claim.Attempt != 1 || len(claim.Generation) != 3 {
			return nil
		}
		fresh := observedLookupLatency(claim.queueEvidence, time.Now())
		if fresh == nil {
			return nil
		}
		result.ObservedLatency = fresh
		target, err := pgx.ConnectConfig(ctx, config)
		if err != nil {
			return err
		}
		defer func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			target.Close(closeCtx)
		}()
		if err := lockTrial(ctx, target); err != nil {
			return err
		}
		// Recheck under the same target lock that serializes builds and cleanup.
		var ready bool
		err = state.QueryRow(ctx, "SELECT EXISTS (SELECT FROM "+journalTable+" WHERE id=$1 AND target=$2 AND status=$3 AND index_definition=$4)",
			fixture.ID, binding, readyStatus, definition).Scan(&ready)
		if err != nil || !ready {
			return err
		}
		expected, err := expectedFixture(ctx, state, fixture.ID)
		if err != nil {
			return err
		}
		schema := trialSchema(fixture.ID)
		candidate := "SELECT id FROM " + qualified(schema, "candidate") + " WHERE lookup=$1"
		if compound {
			candidate = compoundQuery(schema)
		}
		var plan explain
		err = pgx.BeginTxFunc(ctx, target, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL statement_timeout='750ms'"); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "LOCK TABLE ONLY "+qualified(schema, "candidate")+", ONLY "+qualified(schema, "healthy")+" IN ACCESS SHARE MODE NOWAIT"); err != nil {
				return err
			}
			if err := validateSampleFixture(ctx, tx, fixture.ID, expected); err != nil {
				return err
			}
			var err error
			plan, err = readLookupPlan(ctx, tx, "EXPLAIN (FORMAT JSON, VERBOSE) "+candidate, indexReadArgs(inputIDs[0], compound)...)
			if err != nil || plan.QueryID != claim.Query {
				return err
			}
			catalog, err := collect(ctx, tx, fixture.ID)
			if err != nil {
				return err
			}
			if catalog.Existing || plan.Node.Type != "Seq Scan" {
				result.Outcome = "no_change"
				result.DiagnosticReason = "lookup_no_longer_needs_index_measurement"
				plan = explain{}
				return nil
			}
			matches, err := currentLookupCounters(ctx, tx, EnrolledLookup{Database: claim.Database, Role: claim.Role, Query: claim.Query},
				[3]time.Time(claim.Generation))
			if !matches {
				plan = explain{}
			}
			return err
		})
		if err != nil || plan.QueryID != claim.Query || plan.Node.Schema != schema || plan.Node.Relation != "candidate" {
			return err
		}
		var sameRole bool
		if err := target.QueryRow(ctx, "SELECT current_user=session_user AND current_user::regrole::oid=$1 AND (SELECT oid FROM pg_database WHERE datname=current_database())=$2", claim.Role, claim.Database).Scan(&sameRole); err != nil || !sameRole {
			return err
		}
		if err := trialCapacity(ctx, target); err != nil {
			return err
		}
		report := IndexTrialReport{RunID: fixture.ID, JobID: claim.ID}
		started := time.Now()
		report.Before, err = sample(ctx, target, fixture.ID, expected)
		if err != nil {
			return fmt.Errorf("measure queued repair baseline: %w", err)
		}
		report.DetectWallMS = elapsedMS(started)
		report.Evidence, err = evidence(ctx, target, fixture.ID, report.Before)
		if err != nil {
			return err
		}
		if report.Before.HealthyBreaches >= 3 {
			report.Evidence.Decision = InsufficientEvidence
		}
		if err := runIndexInvestigator(ctx, claim, result, report.Evidence, investigate); err != nil {
			return err
		}
		if result.Proposal != nil {
			report.Evidence = *result.Evidence
			report.Proposal = *result.Proposal
		}
		report.ModelWallMS = result.GraphMS
		report, err = applyIndexTrial(ctx, target, state, report, nil)
		if err != nil {
			return err
		}
		report.TotalWallMS = elapsedMS(started)
		status = declinedStatus
		if report.IndexValid {
			status = verifiedStatus
		}
		if err := writeTrialStatus(ctx, state, fixture.ID, status, nil, &report); err != nil {
			return err
		}
		saveFixtureRepair(result, report)
		return nil
	})
}

func saveFixtureRepair(result *DispatchResult, report IndexTrialReport) {
	result.Outcome = report.Outcome
	result.Repair = &report
	if report.Proposal.RunID != "" {
		result.ThreadID, result.Evidence, result.Proposal = report.Proposal.RunID, &report.Evidence, &report.Proposal
		result.GraphMS = report.ModelWallMS
	}
}

func recoverQueuedFixture(ctx context.Context, target, state *pgx.Conn, fixture string) (string, error) {
	return recoverQueuedIndex(ctx, target, state, fixture, false)
}
func recoverQueuedCompound(ctx context.Context, target, state *pgx.Conn, fixture string) (string, error) {
	return recoverQueuedIndex(ctx, target, state, fixture, true)
}
func recoverQueuedIndex(ctx context.Context, target, state *pgx.Conn, fixture string, compound bool) (observed string, err error) {
	building, recovered := "queued_building", "queued_recovered"
	if compound {
		building, recovered = "compound_building", "compound_recovered"
	}
	if err := lockTrial(ctx, target); err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, releaseTrialLock(target)) }()
	var status, recovery string
	if err := state.QueryRow(ctx, "SELECT status,coalesce(recovered_index,'') FROM "+journalTable+" WHERE id=$1", fixture).Scan(&status, &recovery); err != nil {
		return "", err
	}
	if status == recovered {
		return recovery, nil
	}
	if status != building {
		return "", errors.New("queued fixture no longer has an interrupted build")
	}
	observed, err = cleanTrial(ctx, target, state, fixture)
	if err != nil {
		return "", err
	}
	return observed, writeTrialStatus(ctx, state, fixture, recovered, &observed, nil)
}
