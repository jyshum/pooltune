//go:build integration

package hostedquery

import "testing"

// PostgreSQL counters catch duplicate diagnostics or a reduced actual-call cohort.
// Each phase must still measure ten real calls and ten protected reads/writes.
func TestMemoryReusesOneDiagnosticPerSideWithoutReducingActualSamples(t *testing.T) {
	ctx, _, target, _, f, objects, fn := memoryTestFixture(t)
	const measured = "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, TIMING OFF) "
	counts := func() [2]int64 {
		t.Helper()
		var c [2]int64
		err := target.QueryRow(ctx, `SELECT
    COALESCE(sum(calls) FILTER(WHERE query=$1),0)::bigint,
    COALESCE(sum(calls) FILTER(WHERE query=$2),0)::bigint
    FROM public.pg_stat_statements WHERE dbid=(SELECT oid FROM pg_database WHERE datname=current_database()) AND userid=current_user::regrole::oid`,
			measured+"SELECT sum(lookup) FROM (SELECT lookup FROM ONLY "+qualified(f.Schema, "candidate")+" ORDER BY padding,lookup OFFSET $1) AS sorted", measured+memoryQuery(f.Schema)).Scan(&c[0], &c[1])
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	for _, setting := range []string{"4MB", "32MB"} {
		if setting == "32MB" {
			if applied, err := changeMemorySetting(ctx, target, f.ID, objects, fn, "4MB", "32MB"); err != nil || !applied {
				t.Fatalf("apply: %v %v", applied, err)
			}
		}
		before := counts()
		sample, err := sampleMemory(ctx, target, f.ID, objects, fn, setting)
		if err != nil {
			t.Fatal(err)
		}
		after := counts()
		diagnosticCalls, actualCalls := after[0]-before[0], after[1]-before[1]
		if diagnosticCalls != 1 || actualCalls != 10 {
			t.Fatalf("%s executed diagnostics=%d actual measured calls=%d; want 1 and 10", setting, diagnosticCalls, actualCalls)
		}
		if sample.Reads != 10 || sample.HealthyReads != 10 || sample.Writes != 10 || !sample.ResultsCorrect || sample.SortRows != 100000 {
			t.Fatalf("%s lost matched coverage: %+v", setting, sample)
		}
		if setting == "4MB" && (sample.TempBytes < 8388608 || sample.InternalTempBytes < 8388608) {
			t.Fatalf("missing 4MB attribution: %+v", sample)
		}
		if setting == "32MB" && (sample.TempBytes != 0 || sample.InternalTempBytes != 0) {
			t.Fatalf("unexpected 32MB spill: %+v", sample)
		}
		t.Logf("%s diagnostics=%d actual measured calls=%d actual temp=%.0f internal temp=%.0f", setting, diagnosticCalls, actualCalls, sample.TempBytes, sample.InternalTempBytes)
	}
}
