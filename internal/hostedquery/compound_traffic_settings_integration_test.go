//go:build integration

package hostedquery

import (
	"testing"
)

// This is a harness regression, not a latency threshold or a planner hint for
// customer execution. The original traffic settings disabled the access path
// PostgreSQL selected for the valid compound index.
func TestCompoundHealthyTrafficAllowsNormalBitmapPlan(t *testing.T) {
	ctx, target, state, f, _ := compoundFixtureForTest(t)
	if _, err := state.Exec(ctx, "UPDATE "+journalTable+" SET status='compound_building' WHERE id=$1", f.ID); err != nil {
		t.Fatal(err)
	}
	if err := buildCompoundIndex(ctx, target, f.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec(ctx, "SET lock_timeout='250ms'; SET statement_timeout='1500ms'; SET max_parallel_workers_per_gather=0; SET random_page_cost=5; SET enable_bitmapscan=off"); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []struct {
		name, settings string
		wantIndexed    bool
	}{
		{"inherited traffic settings", "SET enable_bitmapscan=off", false},
		{"only bitmap scan restored", "SET enable_bitmapscan=on", true},
		{"PostgreSQL normal costing", "RESET enable_bitmapscan; RESET random_page_cost", true},
	} {
		if _, err := target.Exec(ctx, phase.settings); err != nil {
			t.Fatal(err)
		}
		for _, input := range inputIDs {
			plan, err := scanTrialPlan(target.QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON,VERBOSE,TIMING OFF) "+compoundQuery(f.Schema), indexReadArgs(input, true)...))
			if err != nil {
				t.Fatal(err)
			}
			p := plan.Node
			t.Logf("%s input=%d plan=%s estimated=%d actual=%d examined=%d blocks=%d ms=%.6f", phase.name, input, p.Type, p.EstRows, p.Rows, p.Rows+p.Filtered, p.Hit+p.Read, *plan.ExecutionMS)
			if p.Rows != 1 || p.Loops != 1 {
				t.Fatal("query result changed")
			}
			if phase.wantIndexed {
				if p.Type != "Bitmap Heap Scan" && p.Type != "Index Scan" {
					t.Fatalf("normal plan ignored existing compound index: %s", p.Type)
				}
				if p.Rows+p.Filtered != 1 || p.Hit+p.Read > 10 {
					t.Fatal("healthy indexed access touched excessive data")
				}
			} else if p.Type != "Seq Scan" || p.Rows+p.Filtered != fixtureRows {
				t.Fatalf("old settings did not reproduce scan: %+v", p)
			}
		}
	}
}
