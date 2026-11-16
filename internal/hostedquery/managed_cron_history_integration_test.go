//go:build integration

package hostedquery

import (
	"pooltune/internal/testpg"
	"strings"
	"testing"
)

func TestManagedCronHistoryCannotTurnCompletedRunsIntoRepairEvidence(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t)
	db := openMonitorConn(t, ctx, cfg)
	if _, err := db.Exec(ctx, `CREATE SCHEMA cron;CREATE TABLE cron.job(jobid bigint,jobname text,username text,database text,command text,schedule text,active bool);
 CREATE TABLE cron.job_run_details(runid bigint,jobid bigint,job_pid int,start_time timestamptz,end_time timestamptz,status text);
 INSERT INTO cron.job VALUES(17,'customer refresh',current_user,current_database(),'SELECT 42','*/5 * * * *',true);
 INSERT INTO cron.job_run_details SELECT i,17,42,now()-i*interval '5 minutes',now()-i*interval '5 minutes'+interval '1 second','succeeded' FROM generate_series(1,2)i;`); err != nil {
		t.Fatal(err)
	}
	e := ManagedCronEnrollment{Target: ProjectCronTarget{Project: "ordinary-project", JobID: 17, Name: "customer refresh"}, Schedule: "*/5 * * * *", Window: ProjectCronWindowPlan{EveryMinutes: 5, CurrentMinute: 0, ProposedMinute: 2, DeadlineSeconds: 200, MaxRuntimeSeconds: 20}}
	if err := db.QueryRow(ctx, `SELECT d.oid,current_user::regrole::oid,encode(sha256(convert_to(j.command,'UTF8')),'hex'),encode(sha256(convert_to(to_jsonb(j)::text,'UTF8')),'hex'),j.xmin::text FROM cron.job j CROSS JOIN pg_database d WHERE d.datname=current_database()`).Scan(&e.Target.DatabaseOID, &e.Target.OwnerOID, &e.Target.CommandDigest, &e.Fingerprint, &e.Generation); err != nil {
		t.Fatal(err)
	}
	result, err := ReadManagedCronHistory(ctx, db, e)
	if err != nil || len(result.Runs) != 2 {
		t.Fatalf("history %+v %v", result, err)
	}
	if !strings.Contains(strings.Join(result.Missing, ","), "matched_completed_database_work_windows") {
		t.Fatal("completed history claimed offered-request protection")
	}
	if _, err := db.Exec(ctx, `UPDATE cron.job SET command='SELECT 99' WHERE jobid=17`); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManagedCronHistory(ctx, db, e); err == nil {
		t.Fatal("changed enrolled command accepted")
	}
	var schedule string
	if err := db.QueryRow(ctx, `SELECT schedule FROM cron.job`).Scan(&schedule); err != nil || schedule != e.Schedule {
		t.Fatal("readiness changed a job schedule")
	}
}
