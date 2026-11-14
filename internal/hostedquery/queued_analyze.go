package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

func DispatchFixtureAnalyze(ctx context.Context, state *pgx.Conn, config *pgx.ConnConfig, route string, fixture ConsumerFixture, investigate IndexInvestigator) (*DispatchResult, error) {
	if investigate == nil || ValidateConsumerFixtureID(fixture.ID) != nil ||
		fixture.Enrollment.Project != route+"/consumer/"+fixture.ID {
		return nil, errors.New("queued ANALYZE requires an exact synthetic fixture and investigator")
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
		if status != "analyze_ready" {
			var report AnalyzeReport
			if saved == nil || json.Unmarshal(saved, &report) != nil || report.JobID != claim.ID || report.Fixture != fixture.ID {
				return nil
			}
			switch status {
			case "analyze_verified", "analyze_declined":
				saveAnalyze(result, claim, &report)
			case "analyze_pending":
				// A committed statistics refresh cannot be undone. Do not reissue it
				// when the prior process stopped before recording the result.
				report.Outcome = "repair_interrupted"
				report.Reason = "statistics may have been refreshed; fresh measurements are needed before any new decision"
				saveAnalyze(result, claim, &report)
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
			cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			target.Close(cleanup)
		}()
		if err := lockTrial(ctx, target); err != nil {
			return err
		}
		var ready bool
		err = state.QueryRow(ctx, "SELECT EXISTS(SELECT FROM "+journalTable+" WHERE id=$1 AND target=$2 AND status='analyze_ready')", fixture.ID, binding).Scan(&ready)
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
		match, err := analyzeLookupMatches(ctx, target, fixture.ID, expected, claim)
		if err != nil || !match {
			return err
		}

		report := AnalyzeReport{JobID: claim.ID, Fixture: fixture.ID}
		baseline := time.Now()
		report.Before, err = sampleAnalyze(ctx, target, fixture.ID, expected)
		if err != nil {
			return fmt.Errorf("measure ANALYZE baseline: %w", err)
		}
		report.BaselineMS = elapsedMS(baseline)
		catalog, err := collectIndexCatalog(ctx, target, fixture.ID)
		if err != nil {
			return err
		}
		if catalog.TableOID != expected.Candidate || !catalog.Existing {
			return errors.New("targeted ANALYZE fixture no longer has its lookup index")
		}
		report.Evidence = catalog
		report.Evidence.At = time.Now().UTC()
		report.Evidence.Decision = Investigate
		report.Evidence.Analyze = &AnalyzeEvidence{EstimatedRows: report.Before.EstimatedRows, ActualRows: report.Before.PlanReturned}
		// The singleton MCV entry refers to the exact enrolled synthetic lookup value.
		// Compare its recorded frequency with the measured plan instead of inferring staleness from latency.
		err = target.QueryRow(ctx, `SELECT (SELECT most_common_freqs[1]::float8 FROM pg_catalog.pg_stats
    WHERE schemaname=$1 AND tablename='candidate' AND attname='lookup' AND NOT inherited
      AND array_length(most_common_freqs,1)=1 AND most_common_vals::text=$2)`,
			trialSchema(fixture.ID), fmt.Sprintf("{%d}", analyzeInput)).Scan(&report.Evidence.Analyze.SampledValueFraction)
		if err != nil {
			return err
		}
		report.Evidence.Plan = PlanEvidence{Node: report.Before.Plan, ExaminedRows: report.Before.PlanExamined, ReturnedRows: report.Before.PlanReturned, SharedBlocks: int64(report.Before.Blocks)}
		if err := runIndexInvestigator(ctx, claim, result, report.Evidence, investigate); err != nil {
			return err
		}
		report.Evidence, report.Proposal, report.GraphMS = *result.Evidence, *result.Proposal, result.GraphMS
		if !qualifyAnalyze(report, expected.Candidate, time.Now()) {
			report.Outcome, report.Reason = "no_change", "the measured evidence or recommendation does not support targeted ANALYZE"
			if err := writeAnalyzeStatus(ctx, state, "analyze_declined", &report); err != nil {
				return err
			}
			saveAnalyze(result, claim, &report)
			return nil
		}
		if err := trialCapacity(ctx, target); err != nil {
			return err
		}
		if err := writeAnalyzeStatus(ctx, state, "analyze_pending", &report); err != nil {
			return err
		}
		action := time.Now()
		refreshed := false
		err = pgx.BeginFunc(ctx, target, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL statement_timeout='2s'; SET LOCAL lock_timeout='250ms'; SET LOCAL max_parallel_workers_per_gather=0"); err != nil {
				return err
			}
			if err := lockAnalyzeFixture(ctx, tx, fixture.ID, expected); err != nil {
				return err
			}
			if !qualifyAnalyze(report, expected.Candidate, time.Now()) {
				return errors.New("ANALYZE evidence expired")
			}
			schema := trialSchema(fixture.ID)
			current, err := readLookupPlan(ctx, tx, "EXPLAIN (FORMAT JSON, VERBOSE) "+analyzeCandidate(schema), analyzeInput)
			if err != nil {
				return err
			}
			if current.QueryID != claim.Query || current.Node.Schema != schema || current.Node.Relation != "candidate" ||
				current.Node.Type != report.Before.Plan || current.Node.EstRows != report.Before.EstimatedRows {
				return nil
			}
			_, err = tx.Exec(ctx, "ANALYZE "+qualified(schema, "candidate")+" (lookup)")
			refreshed = err == nil
			return err
		})
		if err != nil {
			return fmt.Errorf("targeted ANALYZE is pending: %w", err)
		}
		if !refreshed {
			report.Outcome, report.Reason = "no_change", "the plan changed before the statistics refresh"
			if err := writeAnalyzeStatus(ctx, state, "analyze_declined", &report); err != nil {
				return err
			}
			saveAnalyze(result, claim, &report)
			return nil
		}
		report.AnalyzeMS = elapsedMS(action)
		verify := time.Now()
		report.After, err = sampleAnalyze(ctx, target, fixture.ID, expected)
		if err != nil {
			return fmt.Errorf("verify targeted ANALYZE: %w", err)
		}
		report.VerifyMS = elapsedMS(verify)
		report.Outcome, report.Reason = verifyAnalyze(report.Before, report.After)
		if err := writeAnalyzeStatus(ctx, state, "analyze_verified", &report); err != nil {
			return err
		}
		saveAnalyze(result, claim, &report)
		return nil
	})
}

func qualifyAnalyze(report AnalyzeReport, table uint32, now time.Time) bool {
	e, p := report.Evidence, report.Proposal
	return e.Decision == Investigate && e.Analyze != nil && e.Analyze.ActualRows > 0 &&
		rowEstimateError(e.Analyze.EstimatedRows, e.Analyze.ActualRows) >= 10 &&
		e.Existing && e.TableOID == table && e.Column == 2 && e.RunID != "" &&
		p.Outcome == "propose" && p.Repair == "analyze" && p.RunID == e.RunID && p.TableOID == table && p.Column == e.Column &&
		!e.At.IsZero() && !e.At.After(now) && now.Sub(e.At) <= 30*time.Second
}

func analyzeLookupMatches(ctx context.Context, target *pgx.Conn, fixture string, expected fixtureObjects, claim investigationClaim) (bool, error) {
	var matches bool
	err := pgx.BeginTxFunc(ctx, target, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL statement_timeout='750ms'"); err != nil {
			return err
		}
		schema := trialSchema(fixture)
		if _, err := tx.Exec(ctx, "LOCK TABLE ONLY "+qualified(schema, "candidate")+", ONLY "+qualified(schema, "healthy")+" IN ACCESS SHARE MODE NOWAIT"); err != nil {
			return err
		}
		if err := validateSampleFixture(ctx, tx, fixture, expected); err != nil {
			return err
		}
		var role bool
		if err := tx.QueryRow(ctx, "SELECT current_user=session_user AND current_user::regrole::oid=$1 AND (SELECT oid FROM pg_database WHERE datname=current_database())=$2", claim.Role, claim.Database).Scan(&role); err != nil || !role {
			return err
		}
		plan, err := readLookupPlan(ctx, tx, "EXPLAIN (FORMAT JSON, VERBOSE) "+analyzeCandidate(schema), analyzeInput)
		if err != nil || plan.QueryID != claim.Query || plan.Node.Schema != schema || plan.Node.Relation != "candidate" {
			return err
		}
		matches, err = currentLookupCounters(ctx, tx, EnrolledLookup{Database: claim.Database, Role: claim.Role, Query: claim.Query}, [3]time.Time(claim.Generation))
		return err
	})
	return matches, err
}

func writeAnalyzeStatus(ctx context.Context, state *pgx.Conn, status string, report *AnalyzeReport) error {
	raw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	tag, err := state.Exec(ctx, "UPDATE "+journalTable+" SET status=$2,report=$3::jsonb WHERE id=$1", report.Fixture, status, string(raw))
	if err == nil && tag.RowsAffected() != 1 {
		return errors.New("ANALYZE journal row missing")
	}
	return err
}

func saveAnalyze(result *DispatchResult, claim investigationClaim, report *AnalyzeReport) {
	result.Outcome, result.Analyze = report.Outcome, report
	result.ObservedLatency = observedLookupLatency(claim.queueEvidence, report.Evidence.At)
	result.ThreadID, result.Evidence, result.Proposal, result.GraphMS = report.Evidence.RunID, &report.Evidence, &report.Proposal, report.GraphMS
}
