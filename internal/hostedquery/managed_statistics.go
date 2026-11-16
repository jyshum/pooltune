package hostedquery

import (
	"context"
	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
)

type ManagedStatisticsReport struct{ ManagedStatisticsTrialReport }

func (m *ManagedProjectIndex) runStatistics(ctx context.Context, state, monitor *pgx.Conn, project supabaseproject.Project, epoch int64, claim investigationClaim, windows *ObservedLookupLatency, result *DispatchResult, observed supabaseproject.DiscoveredIndexEvidence, owner *pgx.Conn, before ManagedIndexMeasurement) (bool, error) {
	return m.runCommittedStatistics(ctx, state, monitor, project, epoch, claim, windows, result, observed, owner, before)
}

func verifyManagedStatistics(before, after []ManagedIndexMeasurement, controls, afterControls *managedTrafficControls) (string, string) {
	if len(before) == 0 || len(before) != len(after) || controls == nil || afterControls == nil || controls.ReadCalls <= 0 || controls.WriteCalls <= 0 || afterControls.ReadCalls <= 0 || afterControls.WriteCalls <= 0 {
		return "inconclusive", "matching sampled inputs and normal read/write traffic were not available"
	}
	if afterControls.ReadMS > controls.ReadMS*1.25 || afterControls.WriteMS > controls.WriteMS*1.25 {
		return "regressed", "protected normal traffic regressed"
	}
	improved := true
	for i, b := range before {
		a := after[i]
		for _, v := range []float64{b.Sample.ReadMS, a.Sample.ReadMS, controls.ReadMS, controls.WriteMS, afterControls.ReadMS, afterControls.WriteMS} {
			if !nonnegativeFinite(v) {
				return "inconclusive", "invalid measured latency"
			}
		}
		if a.Sample.ReadMS > b.Sample.ReadMS*1.25 {
			return "regressed", "a sampled input regressed"
		}
		if b.Sample.PlanReturned <= 0 || b.Sample.PlanReturned != a.Sample.PlanReturned || b.EstimatedRows <= 0 || a.EstimatedRows <= 0 {
			return "inconclusive", "sampled row counts changed"
		}
		gain := b.Sample.ReadMS - a.Sample.ReadMS
		improved = improved && gain >= 5 && gain >= b.Sample.ReadMS*.2 && rowEstimateError(b.EstimatedRows, b.Sample.PlanReturned) >= 10 && rowEstimateError(a.EstimatedRows, a.Sample.PlanReturned) <= 3
	}
	if improved {
		return "improved_tested_inputs", "every sampled input improved with accurate estimates and protected normal traffic"
	}
	return "inconclusive", "one or more sampled inputs did not meet the latency and estimation requirements"
}

// classifyManagedStatisticsTrial distinguishes a measured input improvement from
// a retained repair. Separate application sessions still see the old statistics.
func classifyManagedStatisticsTrial(before, after []ManagedIndexMeasurement, controls, afterControls *managedTrafficControls) (outcome, reason, sampled string) {
	outcome, reason = verifyManagedStatistics(before, after, controls, afterControls)
	sampled = outcome
	if outcome == "improved_tested_inputs" {
		outcome, reason = "inconclusive", "sampled inputs improved inside the trial; normal application sessions cannot verify the new planner statistics before commit"
	}
	return outcome, reason, sampled
}
