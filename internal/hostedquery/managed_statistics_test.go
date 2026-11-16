package hostedquery

import "testing"

func TestManagedStatisticsRetentionRequiresEverySampleAndTraffic(t *testing.T) {
	b := ManagedIndexMeasurement{EstimatedRows: 20000, Sample: TrialSample{PlanReturned: 100, ReadMS: 20}}
	a := ManagedIndexMeasurement{EstimatedRows: 100, Sample: TrialSample{PlanReturned: 100, ReadMS: 10}}
	c := &managedTrafficControls{ReadCalls: 10, WriteCalls: 10, ReadMS: 1, WriteMS: 1}
	for _, tc := range []struct {
		name    string
		after   []ManagedIndexMeasurement
		traffic *managedTrafficControls
		want    string
	}{
		{"both improve", []ManagedIndexMeasurement{a, a}, c, "improved_tested_inputs"},
		{"one unchanged", []ManagedIndexMeasurement{a, b}, c, "inconclusive"},
		{"missing sample", []ManagedIndexMeasurement{a}, c, "inconclusive"},
		{"missing traffic", []ManagedIndexMeasurement{a, a}, nil, "inconclusive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := verifyManagedStatistics([]ManagedIndexMeasurement{b, b}, tc.after, c, tc.traffic)
			if got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestUncommittedStatisticsCannotQualifyRetentionFromExternalTraffic(t *testing.T) {
	before := []ManagedIndexMeasurement{{EstimatedRows: 20000, Sample: TrialSample{PlanReturned: 100, ReadMS: 20}}}
	after := []ManagedIndexMeasurement{{EstimatedRows: 100, Sample: TrialSample{PlanReturned: 100, ReadMS: 1}}}
	traffic := &managedTrafficControls{ReadCalls: 50, WriteCalls: 50, ReadMS: 1, WriteMS: 1}
	outcome, _, sampled := classifyManagedStatisticsTrial(before, after, traffic, traffic)
	if outcome != "inconclusive" || sampled != "improved_tested_inputs" {
		t.Fatalf("uncommitted sample was treated as a retained repair: overall=%s sample=%s", outcome, sampled)
	}
}
