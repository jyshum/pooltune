package hostedquery

import (
	"context"
	"errors"
	"testing"
	"time"

	"pooltune/internal/supabaseproject"
)

func TestOwnedIndexRestartProbeOnlyStopsTheExplicitStagingAction(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var fixture OwnedProjectIndex
	if err := fixture.waitForRestartProbe(ctx, 1, 2); err != nil {
		t.Fatalf("ordinary action was interrupted by the test gate: %v", err)
	}
	fixture.RestartProbe = true
	if err := fixture.waitForRestartProbe(ctx, 1, 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("the test gate did not hand off the pending action on shutdown: %v", err)
	}
}

func TestOwnedIndexBindsOnlyTheDiscoveredFixtureClaim(t *testing.T) {
	now := time.Now().UTC()
	fixture := OwnedProjectIndex{Discover: true, OwnerRoleOID: 22, Columns: []int16{2, 4},
		Target: ProjectIndexTarget{DatabaseOID: 11, TableOID: 33, Schema: "pt_hosted_ix_0123456789abcdef", Relation: "workload"}}
	claim := investigationClaim{InvestigationJob: InvestigationJob{Database: 11, Role: 22, Query: 44, Generation: []time.Time{now, now, now}}}
	observed := supabaseproject.DiscoveredIndexEvidence{
		DatabaseOID: 11, RoleOID: 22, QueryID: 44, TableOID: 33, Schema: fixture.Target.Schema, Relation: "workload",
		Only: true, Columns: []int16{2, 4}, ObjectiveMS: 2, Boot: now, ResetAt: now, EntrySince: now,
	}
	if !fixture.matchesDiscoveredEvidence(claim, 2, observed) {
		t.Fatal("exact discovered evidence was rejected")
	}
	for name, mutate := range map[string]func(*supabaseproject.DiscoveredIndexEvidence){
		"wrong role":       func(e *supabaseproject.DiscoveredIndexEvidence) { e.RoleOID++ },
		"wrong query":      func(e *supabaseproject.DiscoveredIndexEvidence) { e.QueryID++ },
		"wrong table":      func(e *supabaseproject.DiscoveredIndexEvidence) { e.TableOID++ },
		"reordered keys":   func(e *supabaseproject.DiscoveredIndexEvidence) { e.Columns = []int16{4, 2} },
		"changed shape":    func(e *supabaseproject.DiscoveredIndexEvidence) { e.Only = false },
		"stale statistics": func(e *supabaseproject.DiscoveredIndexEvidence) { e.ResetAt = now.Add(-time.Second) },
		"wrong objective":  func(e *supabaseproject.DiscoveredIndexEvidence) { e.ObjectiveMS = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := observed
			mutate(&changed)
			if fixture.matchesDiscoveredEvidence(claim, 2, changed) {
				t.Fatal("mismatched evidence admitted")
			}
		})
	}
}
