package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func DispatchFixtureStatistics(ctx context.Context, state *pgx.Conn, config *pgx.ConnConfig, route string, fixture ConsumerFixture, investigate IndexInvestigator) (*DispatchResult, error) {
	if investigate == nil || ValidateConsumerFixtureID(fixture.ID) != nil || fixture.Enrollment.Project != route+"/consumer/"+fixture.ID {
		return nil, errors.New("queued statistics needs an exact synthetic fixture and investigator")
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
		report := StatisticsReport{JobID: claim.ID, Fixture: fixture.ID}
		if status != "statistics_ready" {
			if saved == nil || json.Unmarshal(saved, &report) != nil || report.JobID != claim.ID || report.Fixture != fixture.ID {
				return nil
			}
			switch status {
			case "statistics_verified", "statistics_declined":
				saveStatistics(result, claim, &report)
			case "statistics_pending":
				report.Outcome = "repair_interrupted"
				report.RecoveredObject, err = inspectPendingStatistics(ctx, state, config, fixture.ID)
				if err != nil {
					return err
				}
				report.Reason = "the prior action did not record a verified outcome; no refresh was repeated"
				saveStatistics(result, claim, &report)
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
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			target.Close(cleanup)
		}()
		if err := lockTrial(ctx, target); err != nil {
			return err
		}
		var ready bool
		err = state.QueryRow(ctx, "SELECT EXISTS(SELECT FROM "+journalTable+" WHERE id=$1 AND target=$2 AND status='statistics_ready')", fixture.ID, binding).Scan(&ready)
		if err != nil || !ready {
			return err
		}
		expected, err := expectedFixture(ctx, state, fixture.ID)
		if err != nil {
			return err
		}
		if err := trialCapacity(ctx, target); err != nil {
			return err
		}
		match, err := statisticsLookupMatches(ctx, target, fixture.ID, expected, claim)
		if err != nil || !match {
			return err
		}

		baseline := time.Now()
		report.Before, err = statisticsBaseline(ctx, target, fixture.ID, expected)
		if err != nil {
			return err
		}
		report.BaselineMS = elapsedMS(baseline)
		if !report.Before.Correlated || len(report.Before.Samples) != 2 {
			return nil
		}
		first := report.Before.Samples[0]
		report.Evidence = IndexEvidence{At: time.Now().UTC(), Decision: Investigate, TableOID: expected.Candidate, Column: 2, Label: "lookup", Existing: true,
			Plan: PlanEvidence{Node: first.Plan, ExaminedRows: first.PlanExamined, ReturnedRows: first.PlanReturned, SharedBlocks: int64(first.Blocks)},
			Statistics: &StatisticsEvidence{Correlated: true, Cases: []AnalyzeEvidence{
				{EstimatedRows: first.EstimatedRows, ActualRows: first.PlanReturned},
				{EstimatedRows: report.Before.Samples[1].EstimatedRows, ActualRows: report.Before.Samples[1].PlanReturned},
			}},
		}
		if err := runIndexInvestigator(ctx, claim, result, report.Evidence, investigate); err != nil {
			return err
		}
		report.Evidence, report.Proposal, report.GraphMS = *result.Evidence, *result.Proposal, result.GraphMS
		if report.Proposal.Outcome != "propose" || report.Proposal.Repair != "statistics" ||
			report.Before.Samples[0].EstimateError < 10 || report.Before.Samples[1].EstimateError < 10 {
			report.Outcome, report.Reason = "no_change", "the measured evidence does not support joint statistics"
			if err := writeStatisticsStatus(ctx, state, "statistics_declined", &report); err != nil {
				return err
			}
			saveStatistics(result, claim, &report)
			return nil
		}
		if err := trialCapacity(ctx, target); err != nil {
			return err
		}
		if err := writeStatisticsStatus(ctx, state, "statistics_pending", &report); err != nil {
			return err
		}
		start := time.Now()
		report.After, err = refreshExtendedStatistics(ctx, target, fixture.ID, expected, report.Before)
		if err != nil {
			return err
		}
		report.ApplyMS = elapsedMS(start)
		report.Outcome, report.Reason = verifyStatistics(report.Before, report.After)
		if !report.After.Applied {
			report.Outcome, report.Reason = "no_change", "the plan or statistics changed before the action"
		}
		if err := writeStatisticsStatus(ctx, state, "statistics_verified", &report); err != nil {
			return err
		}
		saveStatistics(result, claim, &report)
		return nil
	})
}

func statisticsLookupMatches(ctx context.Context, target *pgx.Conn, fixture string, expected fixtureObjects, claim investigationClaim) (bool, error) {
	var matches bool
	err := pgx.BeginFunc(ctx, target, func(tx pgx.Tx) error {
		if err := lockStatisticsFixture(ctx, tx, fixture, expected, false); err != nil {
			return err
		}
		var identity bool
		if err := tx.QueryRow(ctx, "SELECT current_user=session_user AND current_user::regrole::oid=$1 AND (SELECT oid FROM pg_database WHERE datname=current_database())=$2", claim.Role, claim.Database).Scan(&identity); err != nil || !identity {
			return err
		}
		schema := trialSchema(fixture)
		plan, err := readStatisticsPlan(ctx, tx, schema, statisticsCases[0])
		if err != nil || plan.QueryID != claim.Query || plan.Node.Schema != schema || plan.Node.Relation != "candidate" {
			return err
		}
		matches, err = currentLookupCounters(ctx, tx, EnrolledLookup{Database: claim.Database, Role: claim.Role, Query: claim.Query}, [3]time.Time(claim.Generation))
		return err
	})
	return matches, err
}

func writeStatisticsStatus(ctx context.Context, state *pgx.Conn, status string, report *StatisticsReport) error {
	raw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	tag, err := state.Exec(ctx, "UPDATE "+journalTable+" SET status=$2,report=$3::jsonb WHERE id=$1", report.Fixture, status, string(raw))
	if err == nil && tag.RowsAffected() != 1 {
		return errors.New("statistics journal row missing")
	}
	return err
}

func saveStatistics(result *DispatchResult, claim investigationClaim, report *StatisticsReport) {
	result.Outcome, result.Statistics = report.Outcome, report
	result.ObservedLatency = observedLookupLatency(claim.queueEvidence, report.Evidence.At)
	result.ThreadID, result.Evidence, result.Proposal, result.GraphMS = report.Evidence.RunID, &report.Evidence, &report.Proposal, report.GraphMS
}

func inspectPendingStatistics(ctx context.Context, state *pgx.Conn, config *pgx.ConnConfig, fixture string) (string, error) {
	expected, err := expectedFixture(ctx, state, fixture)
	if err != nil {
		return "", err
	}
	target, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return "", err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		target.Close(cleanup)
	}()
	if err := lockTrial(ctx, target); err != nil {
		return "", err
	}
	var observed string
	err = pgx.BeginFunc(ctx, target, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL statement_timeout='750ms'"); err != nil {
			return err
		}
		schema := trialSchema(fixture)
		if _, err := tx.Exec(ctx, "LOCK TABLE ONLY "+qualified(schema, "candidate")+", ONLY "+qualified(schema, "healthy")+" IN ACCESS SHARE MODE NOWAIT"); err != nil {
			return err
		}
		var present bool
		err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT FROM pg_statistic_ext WHERE stxrelid=ANY($1) OR stxnamespace=$2)",
			[]uint32{expected.Candidate, expected.Healthy}, expected.Schema).Scan(&present)
		if err != nil {
			return err
		}
		if err := validateStatisticsFixture(ctx, tx, fixture, expected, present); err != nil {
			return err
		}
		observed = "absent"
		if present {
			observed = "present"
		}
		return nil
	})
	return observed, err
}
