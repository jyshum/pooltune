package hostedquery

import (
	"encoding/json"
	"pooltune/internal/supabaseproject"
	"strings"
	"testing"
	"time"
)

func managedCleanupExample() (managedCleanupAction, investigationClaim) {
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	target := ProjectIndexTarget{DatabaseOID: 1, TableOID: 17000, Schema: "public", Relation: "orders"}
	earlier := ProjectCleanupSettingsObservation{Target: target, At: at, Boot: at.Add(-time.Hour), StatsReset: at.Add(-time.Hour), ScaleFactor: .2, EstimatedRows: 4000, BaseThreshold: 50, MaximumThreshold: -1, DeadTuples: 700, AutovacuumEnabled: true, OnlyDeadTupleTrigger: true, AutovacuumNaptimeSeconds: 1}
	before := earlier
	before.At = at.Add(2 * time.Second)
	before.DeadTuples = 750
	before.Updated = 750
	intent, _ := planProjectCleanupSettings(ProjectCleanupSettingsRequest{RunID: "managed-maintenance-1-1", Earlier: earlier, Later: before}, before)
	a := managedCleanupAction{Version: 2, RunID: intent.RunID, Intent: intent, Earlier: earlier, Before: before, Deadline: before.At.Add(22 * time.Second), InputDigest: strings.Repeat("a", 64), Observed: supabaseproject.DiscoveredIndexEvidence{DatabaseOID: 1, RoleOID: 2, QueryID: 3, TableOID: 17000, Columns: []int16{1}}, ControlMeans: map[int64]float64{4: 1, 5: 1}, ControlCounters: map[int64]managedControlCounter{4: {Query: 4, Kind: "read"}, 5: {Query: 5, Kind: "write"}}}
	a.Evidence = IndexEvidence{RunID: a.RunID, At: before.At, Decision: Investigate, TableOID: target.TableOID, Column: 1, Existing: true, Plan: PlanEvidence{Node: "Index Only Scan", ExaminedRows: 1500, ReturnedRows: 1500}, Vacuum: &VacuumEvidence{HeapFetches: 1500, ScanRows: 1500, ConditionsClear: true, CleanupTrigger: &ManagedCleanupEvidence{Earlier: earlier, Later: before, NextScaleFactor: .15}}}
	a.Proposal = IndexProposal{Outcome: "propose", Repair: "vacuum", RunID: a.RunID, TableOID: target.TableOID, Column: 1, Reason: "growing dead tuple backlog"}
	return a, investigationClaim{InvestigationJob: InvestigationJob{Database: 1, Role: 2, Query: 3}}
}
func TestManagedCleanupPreservesNativeGatesAndPrivateSampleBinding(t *testing.T) {
	a, c := managedCleanupExample()
	if err := validManagedCleanupAction(a, c); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*managedCleanupAction){func(a *managedCleanupAction) { a.Before.OnlyDeadTupleTrigger = false }, func(a *managedCleanupAction) { a.Observed.QueryID++ }, func(a *managedCleanupAction) { a.Deadline = a.Deadline.Add(time.Hour) }, func(a *managedCleanupAction) { a.Intent.AfterOption = "0.01" }} {
		bad := a
		change(&bad)
		if validManagedCleanupAction(bad, c) == nil {
			t.Fatal("unsupported cleanup intent accepted")
		}
	}
	key := "private-existing-database-password"
	a.InputDigest = managedCleanupInputDigest(key, a.Intent.Target, []int32{123456789})
	if a.InputDigest == managedCleanupInputDigest(key, a.Intent.Target, []int32{1}) || a.InputDigest == managedCleanupInputDigest("different-key", a.Intent.Target, []int32{123456789}) {
		t.Fatal("sample identity not bound to private credential")
	}
	raw, _ := json.Marshal(a)
	if strings.Contains(string(raw), key) || strings.Contains(string(raw), "123456789") {
		t.Fatal("sampled data or credential persisted")
	}
}
