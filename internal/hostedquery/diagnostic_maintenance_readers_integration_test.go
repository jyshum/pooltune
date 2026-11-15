//go:build integration

package hostedquery

import (
	"context"
	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
	"testing"
	"time"
)

// A reader must measure current visibility without executing VACUUM or changing data.
func TestVacuumDiagnosticReadsFreshVisibilityAndRejectsChangedEnrollment(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t, "-c", "compute_query_id=on")
	db, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	f, err := CreateVacuumFixture(ctx, db, db, "local")
	if err != nil {
		t.Fatal(err)
	}
	defer RemoveConsumerFixture(ctx, db, db, "local", f.ID)
	objects, err := expectedFixture(ctx, db, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	base := IndexEvidence{RunID: "vacuum-reader", At: time.Now().UTC(), Decision: Investigate, TableOID: objects.Candidate, Column: 2, Label: "lookup"}
	read := VacuumDiagnosticEvidence(db, cfg, "local", f.ID)
	healthy, ok, err := read(ctx, "vacuum", base)
	if err != nil || !ok || healthy.Vacuum == nil || healthy.Vacuum.HeapFetches != 0 {
		t.Fatalf("healthy: %+v %v %v", healthy, ok, err)
	}
	if _, err = db.Exec(ctx, "UPDATE ONLY "+qualified(f.Schema, "candidate")+" SET padding=repeat('q',64)"); err != nil {
		t.Fatal(err)
	}
	dirty, ok, err := read(ctx, "vacuum", base)
	if err != nil || !ok || !vacuumCause(dirty) {
		t.Fatalf("dirty: %+v %v %v", dirty, ok, err)
	}
	if dirty.At.Before(base.At) || dirty.Plan.ReturnedRows != 50000 {
		t.Fatal("missing fresh measured plan")
	}
	for _, tool := range []string{"plan", "indexes"} {
		out, ok, err := read(ctx, tool, base)
		if err != nil || !ok || out.TableOID != base.TableOID || !out.Existing {
			t.Fatalf("%s: %+v %v %v", tool, out, ok, err)
		}
	}
	assertMaintenanceReaderRefusals(t, ctx, read, base, "vacuum")
	if _, err = db.Exec(ctx, "UPDATE "+journalTable+" SET status='vacuum_declined' WHERE id=$1", f.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := read(ctx, "vacuum", base); err == nil || ok {
		t.Fatal("retired fixture admitted")
	}
}

func TestMemoryDiagnosticReadsAttributedSpillAndRefusesFunctionDrift(t *testing.T) {
	ctx, cfg, target, state, f, objects, _ := memoryTestFixture(t)
	base := IndexEvidence{RunID: "memory-reader", At: time.Now().UTC(), Decision: Investigate, TableOID: objects.Candidate, Column: 2, Label: "lookup"}
	read := MemoryDiagnosticEvidence(state, cfg, "local", f.ID)
	out, ok, err := read(ctx, "memory", base)
	if err != nil || !ok || !memoryCause(out) {
		t.Fatalf("memory: %+v %v %v", out, ok, err)
	}
	if out.At.Before(base.At) || out.Memory.TempBytes != out.Memory.InternalTempBytes {
		t.Fatalf("missing actual and internal spill: %+v", out)
	}
	assertMaintenanceReaderRefusals(t, ctx, read, base, "memory")
	if _, err = target.Exec(ctx, "ALTER FUNCTION "+qualified(f.Schema, memoryFunction)+"() SET jit=on"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := read(ctx, "memory", base); err == nil || ok {
		t.Fatal("changed function admitted")
	}
	if _, err = target.Exec(ctx, "ALTER FUNCTION "+qualified(f.Schema, memoryFunction)+"() SET jit=off"); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryDiagnosticHonorsSharedConcurrencyLimit(t *testing.T) {
	ctx, cfg, _, state, f, objects, _ := memoryTestFixture(t)
	for slot := 1; slot <= 3; slot++ {
		c, err := pgx.ConnectConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close(ctx)
		if _, err = c.Exec(ctx, "SELECT pg_advisory_lock($1::integer,$2::integer)", int32(objects.Schema), slot); err != nil {
			t.Fatal(err)
		}
	}
	base := IndexEvidence{RunID: "memory-full", At: time.Now().UTC(), Decision: Investigate, TableOID: objects.Candidate, Column: 2, Label: "lookup"}
	if _, ok, err := MemoryDiagnosticEvidence(state, cfg, "local", f.ID)(ctx, "memory", base); err == nil || ok {
		t.Fatal("memory budget exceeded")
	}
}

// Fake catalogs/periods test reader binding, never real pg_cron scheduling.
func TestCronDiagnosticRechecksCurrentIdentityAndPreservesPeriodTimes(t *testing.T) {
	ctx, cfg, admin, state, f, cf := cronProviderFixture(t, false)
	objects, err := expectedFixture(ctx, state, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	base := IndexEvidence{RunID: "cron-reader", At: time.Now().UTC(), Decision: Investigate, TableOID: objects.Candidate, Column: 1, Label: "lookup"}
	read := CronDiagnosticEvidence(state, cfg, "local", f.ID)
	measured, measuredOK, measuredErr := read(ctx, "plan", base)
	if measuredErr != nil || !measuredOK || measured.Plan.Node != "Result" || measured.Plan.ReturnedRows != 1 || measured.Plan.ExaminedRows != 1 || measured.Plan.SharedBlocks <= 0 {
		t.Fatalf("Cron plan must report actual one-row function execution and observed buffers: %+v %v %v", measured.Plan, measuredOK, measuredErr)
	}
	out, ok, err := read(ctx, "cron", base)
	if err != nil || !ok || !cronCause(out) {
		t.Fatalf("cron: %+v %v %v", out, ok, err)
	}
	if !out.Cron.Periods[0].Cycle.Equal(cf.FirstCycle) || out.Cron.JobID != cf.Target.ID {
		t.Fatal("period history rebound")
	}
	assertMaintenanceReaderRefusals(t, ctx, read, base, "cron")
	if _, err = admin.Exec(ctx, "UPDATE cron.job SET active=false WHERE jobid=1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := read(ctx, "cron", base); err == nil || ok {
		t.Fatal("inactive job admitted")
	}
	if _, err = admin.Exec(ctx, "UPDATE cron.job SET active=true WHERE jobid=1"); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, "CREATE OR REPLACE VIEW cron_adapter.pg_stat_activity AS SELECT 998::integer AS pid,pg_postmaster_start_time() AS backend_start,'pg_cron launcher'::text AS backend_type"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := read(ctx, "cron", base); err == nil || ok {
		t.Fatal("changed scheduler generation admitted")
	}
	if _, err = admin.Exec(ctx, "CREATE OR REPLACE VIEW cron_adapter.pg_stat_activity AS SELECT 999::integer AS pid,pg_postmaster_start_time() AS backend_start,'pg_cron launcher'::text AS backend_type"); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, "REVOKE SELECT ON cron.job FROM fixture_app"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := read(ctx, "cron", base); err == nil || ok {
		t.Fatal("revoked catalog access admitted")
	}
	if _, err = admin.Exec(ctx, "GRANT SELECT ON cron.job TO fixture_app"); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, "UPDATE cron.job SET command='SELECT 2' WHERE jobid=1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := read(ctx, "cron", base); err == nil || ok {
		t.Fatal("changed job admitted")
	}
	if _, err = admin.Exec(ctx, "UPDATE cron.job SET command=$1 WHERE jobid=1", cf.Target.Command); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, "UPDATE cron_adapter.scheduler_clock SET at=at+interval '25 seconds'"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := read(ctx, "cron", base); err == nil || ok {
		t.Fatal("expired handoff admitted")
	}
}

func assertMaintenanceReaderRefusals(t *testing.T, ctx context.Context, read DiagnosticEvidenceReader, base IndexEvidence, tool string) {
	t.Helper()
	for _, edit := range []func(*IndexEvidence){func(e *IndexEvidence) { e.TableOID++ }, func(e *IndexEvidence) { e.Column = 99 }, func(e *IndexEvidence) { e.Columns = []int16{2, 3} }, func(e *IndexEvidence) { e.At = e.At.Add(-time.Minute) }, func(e *IndexEvidence) { e.Decision = NoChange }} {
		bad := base
		edit(&bad)
		if _, ok, err := read(ctx, tool, bad); err == nil || ok {
			t.Fatalf("invalid enrollment admitted: %+v", bad)
		}
	}
	if _, ok, err := read(ctx, "SELECT 1", base); err == nil || ok {
		t.Fatal("arbitrary tool accepted")
	}
	if _, ok, err := read(ctx, "statistics", base); err != nil || ok {
		t.Fatal("unsupported tool should be unavailable")
	}
}
