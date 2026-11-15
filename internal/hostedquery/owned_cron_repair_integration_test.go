//go:build integration

package hostedquery

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
)

func TestVerifyOwnedCronFollowupChecksCatalogHistoryAndProtectedTraffic(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t)
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE SCHEMA cron;
		CREATE TABLE cron.job (jobid bigint PRIMARY KEY, jobname text, username text, database text, command text, schedule text, active boolean);
		CREATE TABLE cron.job_run_details (runid bigint PRIMARY KEY, jobid bigint, job_pid integer,
			start_time timestamptz, end_time timestamptz, status text);
		INSERT INTO cron.job VALUES (17,'owned_target',current_user,current_database(),'CALL demo()', '1-59/2 * * * *',true),
			(18,'owned_witness',current_user,current_database(),'SELECT 1','2 seconds',true),
			(19,'foreign_job',current_user,current_database(),'SELECT 99','*/2 * * * *',true)`)
	first := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	future := first.Add(6 * time.Minute)
	generation := CronGeneration{PID: 13, Started: first.Add(-time.Hour), Postmaster: first.Add(-2 * time.Hour)}
	req := OwnedCronRequest{RunID: "owned-cron-42-1", FirstCycle: first,
		Target: ProjectCronTarget{Project: "private", Name: "owned_target", JobID: 17, WitnessJobID: 18, CommandDigest: fmt.Sprintf("%x", sha256.Sum256([]byte("CALL demo()")))},
	}
	if err := conn.QueryRow(ctx, `SELECT (SELECT oid FROM pg_database WHERE datname=current_database()),current_user::regrole::oid`).Scan(&req.Target.DatabaseOID, &req.Target.OwnerOID); err != nil {
		t.Fatal(err)
	}
	req.Observed = Counter{Database: req.Target.DatabaseOID, Role: req.Target.OwnerOID, Query: 51, Boot: generation.Postmaster, Reset: generation.Started, Entry: generation.Started}
	before := ownedCronTestPeriods(req.Observed, first, true)
	after := ownedCronTestPeriods(req.Observed, future, false)
	current, err := ReadProjectCron(ctx, conn, req.Target)
	if err != nil {
		t.Fatal(err)
	}
	committed := future.Add(-20 * time.Second)
	witnesses := []CronRun{
		{ID: 30, JobID: 18, PID: 50, Start: committed.Add(time.Second), End: committed.Add(2 * time.Second), Status: "succeeded"},
		{ID: 31, JobID: 18, PID: 51, Start: committed.Add(3 * time.Second), End: committed.Add(4 * time.Second), Status: "succeeded"},
	}
	baseline := ProjectCronSnapshot{Schedule: cronOriginal, Fingerprint: current.Fingerprint, Generation: "before"}
	prior := OwnedCronAction{
		Intent:   ProjectCronIntent{Target: req.Target, Window: ownedCronWindow(), Before: baseline, Generation: generation, RunID: req.RunID, ProposedSchedule: cronAlternate, FirstFutureCycle: future},
		Observed: req.Observed, After: current,
		Report: CronReport{Before: before, CommitAt: committed, PostWitness: witnesses, FirstFutureCycle: future, VerifyAfter: future.Add(6*time.Minute + 2*time.Second)},
	}
	for i := range 3 {
		due := future.Add(time.Duration(i)*2*time.Minute + time.Minute)
		exec(`INSERT INTO cron.job_run_details VALUES ($1,17,61,$2,$3,'succeeded')`, int64(100+i), due.Add(time.Second), due.Add(16*time.Second))
	}
	for _, witness := range witnesses {
		exec(`INSERT INTO cron.job_run_details VALUES ($1,$2,$3,$4,$5,$6)`, witness.ID, witness.JobID, witness.PID, witness.Start, witness.End, witness.Status)
	}

	verify := func(req OwnedCronRequest, action OwnedCronAction, periods []CronPeriod, now time.Time, generation CronGeneration) OwnedCronDecision {
		t.Helper()
		r, err := verifyOwnedCronFollowup(ctx, conn, req, action, func(context.Context, OwnedCronRequest, time.Time) ([]CronPeriod, error) {
			return periods, nil
		}, now, generation)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := verify(req, prior, nil, future, generation); r.Report.Outcome != "awaiting_verification" || !r.NotBefore.Equal(prior.Report.VerifyAfter) {
		t.Fatalf("future periods were credited early: %+v", r)
	}
	now := prior.Report.VerifyAfter
	if r := verify(req, prior, after, now, generation); r.Report.Outcome != "improved_tested_inputs" || len(r.Report.History) != 3 {
		t.Fatalf("complete catalog and protected traffic proof failed: %+v", r)
	}
	drift := generation
	drift.PID++
	if r := verify(req, prior, after, now, drift); r.Report.Outcome != "inconclusive" {
		t.Fatalf("restarted database was credited: %+v", r)
	}
	badIdentity := req
	badIdentity.Observed.Query++
	if r := verify(badIdentity, prior, after, now, generation); r.Report.Outcome != "inconclusive" {
		t.Fatalf("another query replayed the report: %+v", r)
	}
	protectedRegression := ownedCronTestPeriods(req.Observed, future, false)
	for i := range protectedRegression {
		protectedRegression[i].Quiet.Write.After.ExecutionMS = 600
	}
	if r := verify(req, prior, protectedRegression, now, generation); r.Report.Outcome != "regressed" {
		t.Fatalf("lost quiet-window write protection was credited: %+v", r)
	}
	exec(`UPDATE cron.job_run_details SET status='failed' WHERE runid=102`)
	if r := verify(req, prior, after, now, generation); r.Report.Outcome != "regressed" {
		t.Fatalf("failed future run was credited: %+v", r)
	}
	exec(`UPDATE cron.job_run_details SET status='succeeded' WHERE runid=102;
		UPDATE cron.job SET command='CALL changed()' WHERE jobid=17`)
	if r := verify(req, prior, after, now, generation); r.Report.Outcome != "inconclusive" {
		t.Fatalf("edited command was credited: %+v", r)
	}
	var schedule string
	if err := conn.QueryRow(ctx, `SELECT schedule FROM cron.job WHERE jobid=19`).Scan(&schedule); err != nil || schedule != cronOriginal {
		t.Fatalf("verification changed unrelated schedule: %q %v", schedule, err)
	}
}

func ownedCronTestPeriods(base Counter, first time.Time, original bool) []CronPeriod {
	periods := make([]CronPeriod, 0, 3)
	for i := range 3 {
		cycle := first.Add(time.Duration(i) * 2 * time.Minute)
		window := func(delta time.Duration, busy, quiet bool) CronWindow {
			at := cycle.Add(delta)
			measure := func(offset, calls int64, mean float64) CronMeasure {
				identity := base
				identity.Query += offset
				after := identity
				after.Calls = calls
				after.ExecutionMS = mean * float64(calls)
				return CronMeasure{Before: identity, After: after, Offered: calls}
			}
			calls, mean := int64(40), 1.
			if original && busy {
				mean = 44
			}
			if quiet {
				calls = 0
			}
			return CronWindow{Start: at, End: at.Add(20 * time.Second), Target: measure(0, calls, mean), Healthy: measure(1, 80, 1), Write: measure(2, 40, 1)}
		}
		p := CronPeriod{Cycle: cycle, Busy: window(0, true, false), Control: window(30*time.Second, false, false), Quiet: window(time.Minute, false, true)}
		if original {
			p.Blocks = []CronBlock{{At: cycle.Add(5 * time.Second), RunID: int64(i + 1), JobID: 17, PID: 33, BackendStart: cycle, WaitingPID: 34, WaitingStart: cycle, Query: base.Query}}
		}
		periods = append(periods, p)
	}
	return periods
}

func TestReadOwnedCronPeriodsPinsProjectAndJob(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t)
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS pooltune_worker"); err != nil {
		t.Fatal(err)
	}
	if err := PrepareOwnedCronPeriodArchive(ctx, conn); err != nil {
		t.Fatal(err)
	}
	req := OwnedCronRequest{
		Target:     ProjectCronTarget{Project: "exact test project", DatabaseOID: 33, JobID: 18},
		Observed:   Counter{Role: 20, Query: -177},
		FirstCycle: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC),
	}
	secondGroup := req.FirstCycle.Add(6 * time.Minute)
	for i := range 6 {
		cycle := req.FirstCycle.Add(time.Duration(i) * 2 * time.Minute)
		period := CronPeriod{Cycle: cycle}
		raw, err := json.Marshal(period)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeOwnedCronPeriod(ctx, conn, req, period, raw); err != nil {
			t.Fatal(err)
		}
		if err := writeOwnedCronPeriod(ctx, conn, req, period, raw); err != nil {
			t.Fatalf("identical period retry refused: %v", err)
		}
		if err := writeOwnedCronPeriod(ctx, conn, req, period, []byte("{}")); err == nil {
			t.Fatal("different evidence replaced an admitted period")
		}
	}
	periods, err := ReadOwnedCronPeriods(ctx, conn, req, secondGroup)
	if err != nil || len(periods) != 3 || !periods[0].Cycle.Equal(secondGroup) {
		t.Fatalf("future periods were not independently bound: %+v %v", periods, err)
	}
	cancelled := req
	cancelled.Target.JobID++
	periods, err = ReadOwnedCronPeriods(ctx, conn, cancelled, secondGroup)
	if err != nil || len(periods) != 0 {
		t.Fatalf("a foreign job read prior observations: %+v %v", periods, err)
	}
}

func TestReadOwnedCronScopeMatchesRealQueryAndControlsWithoutReplay(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	exec := func(db *pgx.Conn, sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(admin, `CREATE EXTENSION pg_stat_statements;
		CREATE SCHEMA cron;
		CREATE TABLE cron.job(jobid bigint PRIMARY KEY,jobname text,username text,database text,command text,schedule text,active boolean);
		GRANT USAGE ON SCHEMA cron TO fixture_app;
		GRANT SELECT ON cron.job TO fixture_app;
		INSERT INTO cron.job VALUES(18,'fixture_app_witness','fixture_app',current_database(),'SELECT 1','2 seconds',true);
		CREATE SCHEMA fixture_app AUTHORIZATION fixture_app;
		COMMENT ON SCHEMA fixture_app IS 'Pooltune hosted cron fixture local-owned'`)
	ownerConfig := cfg.Copy()
	ownerConfig.User = "fixture_app"
	owner, err := pgx.ConnectConfig(ctx, ownerConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close(ctx)
	exec(owner, `CREATE TABLE fixture_app.candidate(id integer PRIMARY KEY);
		CREATE TABLE fixture_app.healthy(id integer PRIMARY KEY, padding text);
		INSERT INTO fixture_app.candidate VALUES (43);
		INSERT INTO fixture_app.healthy VALUES (43,'a'),(90001,'a');
		CREATE FUNCTION fixture_app.pooltune_cron_read() RETURNS integer LANGUAGE plpgsql VOLATILE AS
			$$ DECLARE x integer; BEGIN SELECT id INTO STRICT x FROM fixture_app.candidate WHERE id=43; RETURN x; END $$;
		REVOKE ALL ON FUNCTION fixture_app.pooltune_cron_read() FROM public;
		CREATE PROCEDURE fixture_app.pooltune_cron_hold(holds integer) LANGUAGE plpgsql AS $$ BEGIN NULL; END $$;
		REVOKE ALL ON PROCEDURE fixture_app.pooltune_cron_hold(integer) FROM public`)
	statements := []string{cronQuery("fixture_app"),
		"SELECT id FROM ONLY " + qualified("fixture_app", "healthy") + " WHERE id=$1",
		"UPDATE ONLY " + qualified("fixture_app", "healthy") + " SET padding=CASE padding WHEN 'a' THEN 'b' ELSE 'a' END WHERE id=$1 RETURNING id"}
	exec(owner, statements[0])
	exec(owner, statements[1], int32(43))
	exec(owner, statements[2], int32(90001))
	req := OwnedCronRequest{Target: ProjectCronTarget{Project: "local-owned", WitnessJobID: 18},
		Candidate: ProjectIndexTarget{Schema: "fixture_app", Relation: "candidate"}}
	counters := make([]Counter, len(statements))
	for i, statement := range statements {
		c := &counters[i]
		var args []any
		if i == 1 {
			args = append(args, int32(43))
		} else if i == 2 {
			args = append(args, int32(90001))
		}
		tx, err := owner.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		if err != nil {
			t.Fatal(err)
		}
		plan, err := readLookupPlan(ctx, tx, "EXPLAIN (FORMAT JSON, VERBOSE) "+statement, args...)
		_ = tx.Rollback(ctx)
		if err != nil || plan.QueryID == 0 {
			t.Fatalf("observe query %d: %v", i, err)
		}
		err = owner.QueryRow(ctx, `SELECT s.dbid,s.userid,s.queryid,pg_postmaster_start_time(),info.stats_reset,s.stats_since,s.calls
			FROM pg_stat_statements s CROSS JOIN pg_stat_statements_info info
			WHERE s.toplevel AND s.userid=current_user::regrole::oid AND s.queryid=$1`, plan.QueryID).Scan(&c.Database,
			&c.Role, &c.Query, &c.Boot, &c.Reset, &c.Entry, &c.Calls)
		if err != nil {
			t.Fatalf("find observed query %d: %v", i, err)
		}
	}
	req.Observed = counters[0]
	req.Candidate.DatabaseOID, req.Target.DatabaseOID, req.Target.OwnerOID = req.Observed.Database, req.Observed.Database, req.Observed.Role
	if err := owner.QueryRow(ctx, `SELECT 'fixture_app.candidate'::regclass::oid,
		'fixture_app.pooltune_cron_read()'::regprocedure::oid, 'fixture_app.pooltune_cron_hold(int)'::regprocedure::oid,
		pg_get_functiondef('fixture_app.pooltune_cron_read()'::regprocedure),
		pg_get_functiondef('fixture_app.pooltune_cron_hold(int)'::regprocedure)`).Scan(&req.Candidate.TableOID,
		&req.ReadOID, &req.ProcedureOID, &req.ReadDigest, &req.ProcedureDigest); err != nil {
		t.Fatal(err)
	}
	req.ReadDigest, req.ProcedureDigest = ownedCronDigest(req.ReadDigest), ownedCronDigest(req.ProcedureDigest)
	protected := CronWindow{Healthy: CronMeasure{Before: counters[1]}, Write: CronMeasure{Before: counters[2]}}
	if node, err := readOwnedCronScope(ctx, owner, req, &protected); err != nil || node == "" {
		t.Fatalf("exact observed and protected statements did not bind: %q %v", node, err)
	}
	wrong := protected
	wrong.Write.Before = wrong.Healthy.Before
	if _, err := readOwnedCronScope(ctx, owner, req, &wrong); err == nil {
		t.Fatal("a different protected statement was admitted")
	}
	wrongQuery := req
	wrongQuery.Observed = counters[1]
	if _, err := readOwnedCronScope(ctx, owner, wrongQuery, nil); err == nil {
		t.Fatal("a different candidate statement was admitted")
	}
	for i, c := range counters {
		var calls int64
		if err := owner.QueryRow(ctx, `SELECT calls FROM pg_stat_statements WHERE toplevel AND userid=$1 AND queryid=$2`, c.Role, c.Query).Scan(&calls); err != nil || calls != c.Calls {
			t.Fatalf("catalog plan validation replayed query %d: %d %v", i, calls, err)
		}
	}
	exec(admin, `UPDATE cron.job SET command='SELECT 2' WHERE jobid=18`)
	if _, err := readOwnedCronScope(ctx, owner, req, nil); err == nil {
		t.Fatal("a changed scheduler witness was admitted")
	}
}
