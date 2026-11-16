//go:build integration

package hostedquery

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"pooltune/internal/testpg"
)

func TestProjectCronChangesOnlyApprovedSchedule(t *testing.T) {
	t.Run("legacy_application_telemetry", func(t *testing.T) { testProjectCronChangesOnlyApprovedSchedule(t, false) })
	t.Run("managed_database_observed", func(t *testing.T) { testProjectCronChangesOnlyApprovedSchedule(t, true) })
}
func testProjectCronChangesOnlyApprovedSchedule(t *testing.T, managed bool) {
	ctx, cfg, _ := testpg.Start(t)
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE SCHEMA cron;
		CREATE TABLE cron.job(jobid bigint PRIMARY KEY,jobname text,username text,database text,command text,schedule text,active boolean);
		CREATE TABLE cron.job_run_details(runid bigint,jobid bigint,job_pid integer,start_time timestamptz,end_time timestamptz,status text);
		CREATE FUNCTION cron.alter_job(id bigint,schedule text DEFAULT NULL,active boolean DEFAULT NULL) RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
		BEGIN UPDATE cron.job j SET schedule=coalesce($2,j.schedule),active=coalesce($3,j.active) WHERE j.jobid=$1 AND j.username=session_user;
		IF NOT FOUND THEN RAISE EXCEPTION 'foreign cron job'; END IF; END $$;
		REVOKE ALL ON FUNCTION cron.alter_job(bigint,text,boolean) FROM PUBLIC;
		GRANT USAGE ON SCHEMA cron TO fixture_app;
		GRANT SELECT ON cron.job,cron.job_run_details TO fixture_app;
		GRANT EXECUTE ON FUNCTION cron.alter_job(bigint,text,boolean) TO fixture_app;
		CREATE TABLE cron_intent (name text, original text, alternate text);
		INSERT INTO cron.job VALUES (17,'refresh_shipments','fixture_app',current_database(),'SELECT 101','2-59/10 * * * *',true),
		(18,'owner_scheduler_witness','fixture_app',current_database(),'SELECT 1','2 seconds',true),
		(19,'another_job','fixture_app',current_database(),'SELECT 102','2-59/10 * * * *',true)`)
	job := ProjectCronTarget{Project: "owned-local", JobID: 17, Name: "refresh_shipments", CommandDigest: fmt.Sprintf("%x", sha256.Sum256([]byte("SELECT 101"))), WitnessJobID: 18}
	if err := admin.QueryRow(ctx, `SELECT d.oid,r.oid FROM pg_database d CROSS JOIN pg_roles r WHERE d.datname=current_database() AND r.rolname='fixture_app'`).Scan(&job.DatabaseOID, &job.OwnerOID); err != nil {
		t.Fatal(err)
	}
	cfg = cfg.Copy()
	cfg.User = "fixture_app"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	baseline, err := ReadProjectCron(ctx, conn, job)
	if err != nil {
		t.Fatal(err)
	}
	cycle := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	now := cycle.Add(8 * time.Minute)
	g := CronGeneration{PID: 109, Started: cycle.Add(-time.Hour), Postmaster: cycle.Add(-2 * time.Hour)}
	plan := ProjectCronWindowPlan{EveryMinutes: 10, CurrentMinute: 2, ProposedMinute: 7, DeadlineSeconds: 500, MaxRuntimeSeconds: 20}
	req := ProjectCronRequest{Target: job, Window: plan, Baseline: baseline, Generation: g, RunID: "rotate-fixture-17", ObservedAt: now.Add(-time.Second)}
	for i := range 3 {
		start := cycle.Add(time.Duration(i-2) * 10 * time.Minute)
		due := start.Add(2 * time.Minute)
		makeWindow := func(at time.Time, busy, quiet bool) CronWindow {
			metric := func(query int64, calls int64, mean float64) CronMeasure {
				before := Counter{Database: job.DatabaseOID, Role: 4, Query: query, Boot: g.Postmaster, Reset: g.Started, Entry: g.Started}
				after := before
				after.Calls = calls
				after.ExecutionMS = mean * float64(calls)
				return CronMeasure{Before: before, After: after, Offered: calls}
			}
			targetCalls, targetMS := int64(20), 2.
			if busy {
				targetMS = 42
			}
			if quiet {
				targetCalls = 0
			}
			return CronWindow{Start: at, End: at.Add(20 * time.Second), Target: metric(42, targetCalls, targetMS), Healthy: metric(43, 20, 1), Write: metric(44, 20, 1)}
		}
		id := int64(101 + i)
		req.Periods = append(req.Periods, CronPeriod{Cycle: start, Busy: makeWindow(due, true, false), Control: makeWindow(due.Add(30*time.Second), false, false), Quiet: makeWindow(start.Add(7*time.Minute), false, true), Blocks: []CronBlock{{At: due.Add(5 * time.Second), RunID: id, JobID: 17, PID: 23, BackendStart: due, WaitingPID: 24, WaitingStart: due, Query: 42}}})
		exec(`INSERT INTO cron.job_run_details VALUES ($1,17,23,$2,$3,'succeeded')`, id, due, due.Add(19*time.Second))
	}
	if managed {
		for i, p := range req.Periods {
			convert := func(w CronWindow) ManagedCronWindow {
				m := func(old CronMeasure) ManagedCronMeasure {
					b, a := old.Before, old.After
					base := int64(w.Start.Sub(g.Postmaster)/time.Second) * 100
					b.Calls += base
					a.Calls += base
					b.ExecutionMS += float64(base) * 100
					a.ExecutionMS += float64(base) * 100
					return ManagedCronMeasure{Before: b, After: a}
				}
				return ManagedCronWindow{Start: w.Start, End: w.End, Target: m(w.Target), Read: m(w.Healthy), Write: m(w.Write)}
			}
			for j := range p.Blocks {
				p.Blocks[j].WaitingRole = p.Busy.Target.Before.Role
			}
			req.ManagedPeriods = append(req.ManagedPeriods, ManagedCronPeriod{Cycle: p.Cycle, Run: CronRun{ID: int64(101 + i), JobID: 17, PID: 23, Start: p.Busy.Start, End: p.Busy.Start.Add(19 * time.Second), Status: "succeeded"}, Busy: convert(p.Busy), Control: convert(p.Control), Quiet: convert(p.Quiet), Blocks: p.Blocks})
		}
		req.Periods = nil
	}
	for i := range 2 {
		at := now.Add(time.Duration(i-5) * time.Second)
		exec(`INSERT INTO cron.job_run_details VALUES ($1,18,32,$2,$3,'succeeded')`, int64(200+i), at, at.Add(time.Second))
	}
	denied := errors.New("denied test Cron action")
	guard := ProjectCronGuards{
		CheckPermission: func(_ context.Context, i ProjectCronIntent) error {
			if i.Target != job || i.Before.Schedule != "2-59/10 * * * *" || i.ProposedSchedule != "7-59/10 * * * *" {
				return denied
			}
			return nil
		},
		CheckCapacity:      func(context.Context, ProjectCronIntent) error { return nil },
		RecordWitnessFence: func(context.Context, string) error { return nil },
		RecordIntent: func(ctx context.Context, i ProjectCronIntent) error {
			_, err := admin.Exec(ctx, `INSERT INTO cron_intent VALUES ($1,$2,$3)`, i.Target.Name, i.Before.Schedule, i.ProposedSchedule)
			return err
		},
	}
	postCommitClockReads := 0
	provider := projectCronProvider{check: func(context.Context, trialReader) error { return nil }, clock: func(_ context.Context, reader trialReader) (time.Time, CronGeneration, error) {
		if _, committed := reader.(*pgx.Conn); committed {
			postCommitClockReads++
			if postCommitClockReads == 2 {
				for i := range 2 {
					at := now.Add(time.Duration(i+1) * time.Second)
					exec(`INSERT INTO cron.job_run_details VALUES ($1,18,32,$2,$3,'succeeded')`, int64(300+i), at, at.Add(time.Second))
				}
				return now.Add(5 * time.Second), g, nil
			}
		}
		return now, g, nil
	}}
	bad := guard
	bad.RecordIntent = func(context.Context, ProjectCronIntent) error { return denied }
	if result, err := applyProjectCron(ctx, cfg, req, bad, provider); !errors.Is(err, denied) || result.WriteAttempted {
		t.Fatalf("failed durable intent reached mutation: %+v %v", result, err)
	}
	for _, mutate := range []func(*ProjectCronRequest){func(r *ProjectCronRequest) { r.Window.DeadlineSeconds = 431 }, func(r *ProjectCronRequest) { r.Window.EveryMinutes = 11 }, func(r *ProjectCronRequest) { r.Target.OwnerOID++ }, func(r *ProjectCronRequest) { r.Target.CommandDigest = "changed" }, func(r *ProjectCronRequest) { r.Periods = nil; r.ManagedPeriods = nil }} {
		bad := req
		mutate(&bad)
		if r, e := applyProjectCron(ctx, cfg, bad, guard, provider); e == nil || r.WriteAttempted {
			t.Fatalf("unsafe shape admitted: %+v %v", r, e)
		}
	}
	exec(`INSERT INTO cron.job_run_details VALUES (999,17,12,$1,NULL,'running')`, now)
	if r, e := applyProjectCron(ctx, cfg, req, guard, provider); e == nil || r.WriteAttempted {
		t.Fatalf("active job was moved: %+v %v", r, e)
	}
	exec(`DELETE FROM cron.job_run_details WHERE runid=999`)
	racing := guard
	racing.RecordIntent = func(ctx context.Context, i ProjectCronIntent) error {
		if err := guard.RecordIntent(ctx, i); err != nil {
			return err
		}
		_, err := admin.Exec(ctx, `UPDATE cron.job SET command='SELECT 403' WHERE jobid=17`)
		return err
	}
	if r, e := applyProjectCron(ctx, cfg, req, racing, provider); e == nil || !r.WriteAttempted {
		t.Fatalf("independent job edit did not abort the apply: %+v %v", r, e)
	}
	var command, schedule string
	if err := admin.QueryRow(ctx, `SELECT command,schedule FROM cron.job WHERE jobid=17`).Scan(&command, &schedule); err != nil || command != "SELECT 403" || schedule != "2-59/10 * * * *" {
		t.Fatalf("independent edit was overwritten: %q %q %v", command, schedule, err)
	}
	exec(`UPDATE cron.job SET command='SELECT 101' WHERE jobid=17; DELETE FROM cron_intent`)
	req.Baseline, err = ReadProjectCron(ctx, conn, job)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		sql      string
		command  string
		isActive bool
	}{
		{"UPDATE cron.job SET command='SELECT 9' WHERE jobid=18", "SELECT 9", true},
		{"UPDATE cron.job SET active=false WHERE jobid=18", "SELECT 1", false},
	} {
		racingWitness := guard
		racingWitness.RecordIntent = func(ctx context.Context, i ProjectCronIntent) error {
			if err := guard.RecordIntent(ctx, i); err != nil {
				return err
			}
			_, err := admin.Exec(ctx, change.sql)
			return err
		}
		got, err := applyProjectCron(ctx, cfg, req, racingWitness, provider)
		var conflict *pgconn.PgError
		if !errors.As(err, &conflict) || conflict.Code != "40001" || !got.WriteAttempted || !got.CommitAt.IsZero() {
			t.Fatalf("independent witness edit reached schedule change: %+v %v", got, err)
		}
		var active bool
		var saved int
		err = admin.QueryRow(ctx, `SELECT command,active,(SELECT schedule FROM cron.job WHERE jobid=17),
			(SELECT count(*) FROM cron_intent) FROM cron.job WHERE jobid=18`).Scan(&command, &active, &schedule, &saved)
		if err != nil || command != change.command || active != change.isActive || schedule != req.Baseline.Schedule || saved != 1 {
			t.Fatalf("independent witness edit or committed intent was overwritten: %q %t %q %d %v", command, active, schedule, saved, err)
		}
		exec(`UPDATE cron.job SET command='SELECT 1', active=true WHERE jobid=18; DELETE FROM cron_intent`)
	}
	result, err := applyProjectCron(ctx, cfg, req, guard, provider)
	if err != nil || !result.WriteAttempted || result.After.Schedule != "7-59/10 * * * *" || !result.FirstFutureCycle.Equal(cycle.Add(10*time.Minute)) || len(result.PostCommitWitnesses) != 2 {
		t.Fatalf("exact change missing: %+v %v", result, err)
	}
	var other string
	var recorded int
	if err = admin.QueryRow(ctx, `SELECT (SELECT command FROM cron.job WHERE jobid=17),(SELECT schedule FROM cron.job WHERE jobid=19),(SELECT count(*) FROM cron_intent)`).Scan(&command, &other, &recorded); err != nil || command != "SELECT 101" || other != "2-59/10 * * * *" || recorded != 1 {
		t.Fatalf("changed unrelated state: %q %q %d %v", command, other, recorded, err)
	}
	if r, e := ApplyProjectCron(ctx, cfg, req, guard); e == nil || r.WriteAttempted {
		t.Fatalf("public action accepted non-extension provider: %+v %v", r, e)
	}
	exec(`UPDATE cron.job SET schedule='2-59/10 * * * *' WHERE jobid=17;
		DELETE FROM cron.job_run_details WHERE runid>=300`)
	req.Baseline, err = ReadProjectCron(ctx, conn, job)
	if err != nil {
		t.Fatal(err)
	}
	missingWitness := provider
	postCommitClockReads = 0
	missingWitness.clock = func(_ context.Context, reader trialReader) (time.Time, CronGeneration, error) {
		if _, committed := reader.(*pgx.Conn); committed {
			postCommitClockReads++
			if postCommitClockReads > 1 {
				return now.Add(8 * time.Second), g, nil
			}
		}
		return now, g, nil
	}
	if r, e := applyProjectCron(ctx, cfg, req, guard, missingWitness); e == nil || !r.WriteAttempted || r.After.Schedule != "" || len(r.PostCommitWitnesses) != 0 {
		t.Fatalf("schedule with missing scheduler acknowledgement was accepted: %+v %v", r, e)
	}
	if err := admin.QueryRow(ctx, `SELECT schedule FROM cron.job WHERE jobid=17`).Scan(&schedule); err != nil || schedule != "7-59/10 * * * *" {
		t.Fatalf("ambiguous action result lost the committed schedule: %q %v", schedule, err)
	}
	if r, e := applyProjectCron(ctx, cfg, req, guard, missingWitness); e == nil || r.WriteAttempted {
		t.Fatalf("lost acknowledgement allowed a repeated write: %+v %v", r, e)
	}
	exec(`UPDATE cron.job SET schedule='2-59/10 * * * *' WHERE jobid=17;
		DELETE FROM cron.job_run_details WHERE runid>=300`)
	req.Baseline, err = ReadProjectCron(ctx, conn, job)
	if err != nil {
		t.Fatal(err)
	}
	trace := &projectCronReadbackTrace{}
	lateReadback := cfg.Copy()
	lateReadback.Tracer = trace
	postCommitClockReads = 0
	accurateAcknowledgement := provider
	for i := range 2 {
		at := now.Add(time.Duration(1+i*2) * time.Second)
		exec(`INSERT INTO cron.job_run_details VALUES ($1,18,32,$2,$3,'succeeded')`, int64(400+i), at, at.Add(time.Second))
	}
	accurateAcknowledgement.clock = func(_ context.Context, reader trialReader) (time.Time, CronGeneration, error) {
		if _, committed := reader.(*pgx.Conn); !committed {
			return now, g, nil
		}
		postCommitClockReads++
		switch postCommitClockReads {
		case 1:
			if trace.readbackBeforeClock {
				return now.Add(2 * time.Second), g, nil
			}
			return now, g, nil
		case 2:
			return now.Add(5 * time.Second), g, nil
		default:
			return now.Add(10 * time.Second), g, nil
		}
	}
	if r, e := applyProjectCron(ctx, lateReadback, req, guard, accurateAcknowledgement); e != nil || len(r.PostCommitWitnesses) != 2 || !r.CommitAt.Equal(now) {
		t.Fatalf("catalog round trip obscured a real later witness: %+v %v", r, e)
	}
	if trace.separateWrites != 0 || trace.guardedWriteBatches != 1 {
		t.Fatalf("Cron writes and exact in-transaction readbacks required separate round trips: writes=%d batches=%d",
			trace.separateWrites, trace.guardedWriteBatches)
	}
}

type projectCronReadbackTrace struct {
	committed, readbackBeforeClock      bool
	separateWrites, guardedWriteBatches int
}

func (t *projectCronReadbackTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, query pgx.TraceQueryStartData) context.Context {
	if query.SQL == "commit" {
		t.committed = true
	}
	if t.committed && strings.Contains(query.SQL, "SELECT j.schedule,") {
		t.readbackBeforeClock = true
	}
	if strings.Contains(query.SQL, "SELECT cron.alter_job(") {
		t.separateWrites++
	}
	return ctx
}

func (*projectCronReadbackTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (t *projectCronReadbackTrace) TraceBatchStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchStartData) context.Context {
	queries := data.Batch.QueuedQueries
	if len(queries) == 4 && strings.Contains(queries[0].SQL, "active := true") && strings.Contains(queries[1].SQL, "schedule := ") &&
		strings.Contains(queries[2].SQL, "SELECT j.schedule,") && strings.Contains(queries[3].SQL, "pg_catalog.to_jsonb(w)") {
		t.guardedWriteBatches++
	}
	return ctx
}

func (*projectCronReadbackTrace) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {
}
func (*projectCronReadbackTrace) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData) {}
