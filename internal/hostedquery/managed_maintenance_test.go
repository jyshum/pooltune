package hostedquery

import "testing"

func TestManagedMaintenanceNeedsSameRowsAndMechanismAndProtectedTraffic(t *testing.T) {
	heapBefore, heapAfter := int64(2000), int64(0)
	before := ManagedIndexMeasurement{EstimatedRows: 20000, HeapFetches: &heapBefore, ScanRows: 2000, Sample: TrialSample{ReadMS: 20, PlanReturned: 2000, Blocks: 2000}}
	after := ManagedIndexMeasurement{EstimatedRows: 2000, HeapFetches: &heapAfter, ScanRows: 2000, Sample: TrialSample{ReadMS: 10, PlanReturned: 2000, Blocks: 20}}
	controls := &managedTrafficControls{ReadMS: 1, WriteMS: 1, ReadCalls: 20, WriteCalls: 20}
	for _, tc := range []struct {
		name      string
		operation ProjectMaintenanceOperation
		change    func(*ManagedIndexMeasurement, *managedTrafficControls)
		want      string
	}{
		{"analyze", ProjectAnalyze, func(*ManagedIndexMeasurement, *managedTrafficControls) {}, "improved_tested_inputs"},
		{"vacuum", ProjectVacuum, func(*ManagedIndexMeasurement, *managedTrafficControls) {}, "improved_tested_inputs"},
		{"changed input population", ProjectAnalyze, func(a *ManagedIndexMeasurement, _ *managedTrafficControls) { a.Sample.PlanReturned = 100 }, "inconclusive"},
		{"no planner improvement", ProjectAnalyze, func(a *ManagedIndexMeasurement, _ *managedTrafficControls) { a.EstimatedRows = 20000 }, "inconclusive"},
		{"no visibility improvement", ProjectVacuum, func(a *ManagedIndexMeasurement, _ *managedTrafficControls) { a.HeapFetches = &heapBefore }, "inconclusive"},
		{"missing writes", ProjectVacuum, func(_ *ManagedIndexMeasurement, c *managedTrafficControls) { c.WriteCalls = 0 }, "inconclusive"},
		{"write regression", ProjectAnalyze, func(_ *ManagedIndexMeasurement, c *managedTrafficControls) { c.WriteMS = 3 }, "regressed"},
		{"small gain", ProjectAnalyze, func(a *ManagedIndexMeasurement, _ *managedTrafficControls) { a.Sample.ReadMS = 19 }, "inconclusive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, c := after, *controls
			tc.change(&a, &c)
			got, _ := verifyManagedMaintenance(tc.operation, before, a, controls, &c)
			if got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}
