//go:build integration

package hostedquery

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
	"testing"
	"time"
)

// A deliberately fake pg_cron catalog and launcher row test our catalog binding
// and journal recovery. They do NOT run or qualify pg_cron scheduling.
func cronCatalogAdapter(t *testing.T) (context.Context, *pgx.ConnConfig, *pgx.Conn, *pgx.Conn, ConsumerFixture, CronFixture) {
	t.Helper()
	ctx, cfg, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	target, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { target.Close(ctx) })
	if _, err = target.Exec(ctx, "CREATE EXTENSION pg_stat_statements"); err != nil {
		t.Fatal(err)
	}
	if _, err = target.Exec(ctx, "CREATE DATABASE cron_state"); err != nil {
		t.Fatal(err)
	}
	sc := cfg.Copy()
	sc.Database = "cron_state"
	state, err := pgx.ConnectConfig(ctx, sc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close(ctx) })
	f, err := CreateConsumerFixture(ctx, target, state, "local")
	if err != nil {
		t.Fatal(err)
	}
	if err = createCronRoutines(ctx, target, f.Schema, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = target.Exec(ctx, `CREATE SCHEMA cron; CREATE TABLE cron.job(jobid bigint PRIMARY KEY,jobname text UNIQUE,username text,database text,command text,schedule text,active boolean); CREATE TABLE cron.job_run_details(runid bigint,jobid bigint,job_pid integer,start_time timestamptz,end_time timestamptz,status text); CREATE SCHEMA cron_adapter; CREATE VIEW cron_adapter.pg_stat_activity AS SELECT 999::integer AS pid,pg_postmaster_start_time() AS backend_start,'pg_cron launcher'::text AS backend_type; SET search_path=cron_adapter,pg_catalog`); err != nil {
		t.Fatal(err)
	}
	cfg = cfg.Copy()
	cfg.RuntimeParams["search_path"] = "cron_adapter,pg_catalog"
	now, g, err := readCronClock(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	cf := CronFixture{Generation: g, Created: now.Add(-20 * time.Minute), Expires: now.Add(time.Hour), FirstCycle: cronCycle(now).Add(-12 * time.Minute), Activated: now.Add(-13 * time.Minute)}
	cf.Read, err = readCronRoutine(ctx, target, f.Schema, cronReadFunction)
	if err != nil {
		t.Fatal(err)
	}
	cf.Procedure, err = readCronRoutine(ctx, target, f.Schema, cronProcedure)
	if err != nil {
		t.Fatal(err)
	}
	var owner, database string
	if err = target.QueryRow(ctx, "SELECT current_user,current_database()").Scan(&owner, &database); err != nil {
		t.Fatal(err)
	}
	cf.Target = CronJob{ID: 1, Name: "pooltune_target_" + f.ID, Owner: owner, Database: database, Command: "CALL " + qualified(f.Schema, cronProcedure) + "(200)", Schedule: cronOriginal, Active: true}
	cf.Witness = CronJob{ID: 2, Name: "pooltune_witness_" + f.ID, Owner: owner, Database: database, Command: "SELECT 1", Schedule: cronWitnessSchedule, Active: true}
	for _, j := range []CronJob{cf.Target, cf.Witness} {
		if _, err = target.Exec(ctx, "INSERT INTO cron.job VALUES($1,$2,$3,$4,$5,$6,$7)", j.ID, j.Name, j.Owner, j.Database, j.Command, j.Schedule, j.Active); err != nil {
			t.Fatal(err)
		}
	}
	if err = writeCronFixture(ctx, state, f.ID, cf); err != nil {
		t.Fatal(err)
	}
	if err = PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	if _, err = state.Exec(ctx, "INSERT INTO pooltune_worker.projects(project_id) VALUES($1)", f.Enrollment.Project); err != nil {
		t.Fatal(err)
	}
	return ctx, cfg, target, state, f, cf
}
func TestCronCatalogRejectsChangedIdentity(t *testing.T) {
	ctx, _, target, state, f, cf := cronCatalogAdapter(t)
	if err := validateCronFixture(ctx, target, state, f.ID, cf, cronOriginal); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"command", "username", "database", "schedule"} {
		t.Run(column, func(t *testing.T) {
			tx, err := target.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err = tx.Exec(ctx, "UPDATE cron.job SET "+column+"='changed' WHERE jobid=1"); err != nil {
				t.Fatal(err)
			}
			if err = validateCronFixture(ctx, tx, state, f.ID, cf, cronOriginal); err == nil {
				t.Fatal("identity drift admitted")
			}
		})
	}
	tx, err := target.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "ALTER FUNCTION "+qualified(f.Schema, cronReadFunction)+"() SET work_mem='8MB'"); err != nil {
		t.Fatal(err)
	}
	if err = validateCronFixture(ctx, tx, state, f.ID, cf, cronOriginal); err == nil {
		t.Fatal("changed read definition admitted")
	}
}
func TestCronRecoveryNeverRepeatsMutationOrModel(t *testing.T) {
	for _, mode := range []string{"before_mutation", "acknowledgement_lost", "future_wait", "missing_traffic", "final_write_replay"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cfg, target, state, f, cf := cronCatalogAdapter(t)
			var job int64
			if err := state.QueryRow(ctx, "INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence) VALUES($1,1,2,3,'{}') RETURNING id", f.Enrollment.Project).Scan(&job); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			r := CronReport{Fixture: f.ID, JobID: job, Outcome: "awaiting_verification", IntentAt: now.Add(-time.Second), FirstFutureCycle: cronCycle(now).Add(-6 * time.Minute), VerifyAfter: now.Add(-time.Second)}
			status := "cron_pending"
			if mode != "before_mutation" {
				if _, err := target.Exec(ctx, "UPDATE cron.job SET schedule=$1 WHERE jobid=1", cronAlternate); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "future_wait" || mode == "missing_traffic" {
				r.CommitAt = now.Add(-10 * time.Minute)
				r.PostWitness = []CronRun{{ID: 1}, {ID: 2}}
				status = "cron_verifying"
				if mode == "future_wait" {
					r.VerifyAfter = now.Add(time.Hour)
				} else {
					for i := 0; i < 6; i++ {
						s := cf.FirstCycle.Add(time.Duration(i) * 2 * time.Minute)
						if i >= 3 {
							s = s.Add(time.Minute)
						}
						if _, err := target.Exec(ctx, "INSERT INTO cron.job_run_details VALUES($1,1,42,$2,$3,'succeeded')", i+1, s, s.Add(17*time.Second)); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			if mode == "final_write_replay" {
				status = "cron_verified"
				r.Outcome = "improved_tested_inputs"
			}
			if err := writeCronReport(ctx, state, status, &r); err != nil {
				t.Fatal(err)
			}
			result, err := DispatchFixtureCron(ctx, state, cfg, "local", f, func(context.Context, IndexEvidence) (IndexProposal, error) {
				t.Fatal("recovery called model")
				return IndexProposal{}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			want := "inconclusive"
			if mode == "future_wait" {
				want = "awaiting_verification"
			}
			if mode == "final_write_replay" {
				want = "improved_tested_inputs"
			}
			if result == nil || result.Outcome != want {
				raw, _ := json.Marshal(result)
				t.Fatalf("got %s want %s", raw, want)
			}
			current, err := readCronJob(ctx, target, cf.Target.Name)
			if err != nil {
				t.Fatal(err)
			}
			wantSchedule := cronAlternate
			if mode == "before_mutation" {
				wantSchedule = cronOriginal
			}
			if current.Schedule != wantSchedule {
				t.Fatal("recovery mutated schedule")
			}
		})
	}
}

func TestCronFinalReportCannotReplayForAnotherQuery(t *testing.T) {
	ctx, cfg, _, state, f, _ := cronCatalogAdapter(t)
	var root int64
	if err := state.QueryRow(ctx, "INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence,finished_at) VALUES($1,1,2,3,'{}',now()) RETURNING id", f.Enrollment.Project).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Exec(ctx, "INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence) VALUES($1,1,2,4,'{}')", f.Enrollment.Project); err != nil {
		t.Fatal(err)
	}
	if err := writeCronReport(ctx, state, "cron_verified", &CronReport{Fixture: f.ID, JobID: root, Outcome: "improved_tested_inputs"}); err != nil {
		t.Fatal(err)
	}
	r, err := DispatchFixtureCron(ctx, state, cfg, "local", f, func(context.Context, IndexEvidence) (IndexProposal, error) {
		t.Fatal("foreign query reached model")
		return IndexProposal{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if r == nil || r.Outcome != "insufficient_evidence" {
		t.Fatalf("replayed another query's result: %+v", r)
	}
}

func TestCronCreationAcknowledgementLossDiscoversOnlyOwnedJobs(t *testing.T) {
	ctx, _, target, state, f, cf := cronCatalogAdapter(t)
	// An isolated catalog adapter implements only job deletion. No scheduler exists.
	if _, err := target.Exec(ctx, `CREATE FUNCTION cron.unschedule(bigint) RETURNS boolean LANGUAGE sql AS $$WITH gone AS (DELETE FROM cron.job WHERE jobid=$1 RETURNING jobid) SELECT EXISTS(SELECT FROM gone)$$; UPDATE cron.job SET active=false; INSERT INTO cron.job VALUES(3,'foreign','other','other','SELECT 9','* * * * *',true)`); err != nil {
		t.Fatal(err)
	}
	cf.Target.ID = 0
	cf.Witness.ID = 0
	if err := writeCronFixture(ctx, state, f.ID, cf); err != nil {
		t.Fatal(err)
	}
	if err := writeTrialStatus(ctx, state, f.ID, "cron_preparing", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := RemoveConsumerFixture(ctx, target, state, "local", f.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := target.QueryRow(ctx, "SELECT count(*) FROM cron.job WHERE jobid=3 AND command='SELECT 9'").Scan(&n); err != nil || n != 1 {
		t.Fatal("foreign job was touched", err)
	}
	var gone bool
	if err := target.QueryRow(ctx, "SELECT to_regnamespace($1) IS NULL", f.Schema).Scan(&gone); err != nil || !gone {
		t.Fatal("owned fixture was not removed", err)
	}
}
func TestCronCleanupPreservesMutatedWitness(t *testing.T) {
	ctx, _, target, state, f, _ := cronCatalogAdapter(t)
	if _, err := target.Exec(ctx, "UPDATE cron.job SET command='SELECT 2' WHERE jobid=2"); err != nil {
		t.Fatal(err)
	}
	if err := writeTrialStatus(ctx, state, f.ID, "cron_ready", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := RemoveConsumerFixture(ctx, target, state, "local", f.ID); err == nil {
		t.Fatal("changed witness deleted")
	}
	var n int
	if err := target.QueryRow(ctx, "SELECT count(*) FROM cron.job").Scan(&n); err != nil || n != 2 {
		t.Fatal("job cleanup did not preserve identity conflict", err)
	}
}

func TestCronLostUnscheduleAckStillWaitsForWitnessTermination(t *testing.T) {
	ctx, _, target, state, f, _ := cronCatalogAdapter(t)
	if _, err := target.Exec(ctx, "DELETE FROM cron.job;INSERT INTO cron.job_run_details VALUES(1,2,42,now(),NULL,'running')"); err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
	defer cancel()
	if err := cleanCronJobs(bounded, target, state, f.ID); err == nil {
		t.Fatal("absent catalog rows bypassed terminal history check after lost acknowledgement")
	}
}
