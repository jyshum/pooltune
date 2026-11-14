//go:build integration

package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// This adapter uses real PostgreSQL ACLs, snapshots, writes and conflicts. Its
// API is a narrowly owned SQL stand-in, not pg_cron or scheduler qualification.
func cronProviderFixture(t *testing.T, activation bool) (context.Context, *pgx.ConnConfig, *pgx.Conn, *pgx.Conn, ConsumerFixture, CronFixture) {
	t.Helper()
	ctx, cfg, target, state, fixture, f := cronCatalogAdapter(t)
	now := time.Date(2026, 11, 14, 12, 5, 25, 0, time.UTC)
	if activation {
		now = time.Date(2026, 11, 14, 12, 1, 10, 0, time.UTC)
	}
	_, err := target.Exec(ctx, `CREATE TABLE cron_adapter.scheduler_clock(at timestamptz);
 CREATE FUNCTION cron_adapter.clock_timestamp() RETURNS timestamptz LANGUAGE sql AS $$SELECT at FROM cron_adapter.scheduler_clock$$;
 CREATE ROLE cron_catalog_owner;
 CREATE FUNCTION cron.alter_job(bigint,schedule text DEFAULT NULL,command text DEFAULT NULL,database text DEFAULT NULL,username text DEFAULT NULL,active boolean DEFAULT NULL) RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$BEGIN
 UPDATE cron.job SET schedule=coalesce($2,cron.job.schedule),command=coalesce($3,cron.job.command),database=coalesce($4,cron.job.database),username=coalesce($5,cron.job.username),active=coalesce($6,cron.job.active) WHERE jobid=$1 AND cron.job.username=session_user;
 IF NOT FOUND THEN RAISE EXCEPTION 'owned job missing'; END IF; END$$;
 ALTER TABLE cron.job OWNER TO cron_catalog_owner;
 ALTER TABLE cron.job_run_details OWNER TO cron_catalog_owner;
 ALTER FUNCTION cron.alter_job(bigint,text,text,text,text,boolean) OWNER TO cron_catalog_owner;
 GRANT USAGE ON SCHEMA cron TO cron_catalog_owner;
 REVOKE ALL ON FUNCTION cron.alter_job(bigint,text,text,text,text,boolean) FROM PUBLIC;
 GRANT SELECT ON cron.job,cron.job_run_details TO fixture_app;
 GRANT EXECUTE ON FUNCTION cron.alter_job(bigint,text,text,text,text,boolean) TO fixture_app;
 GRANT USAGE ON SCHEMA cron,cron_adapter TO fixture_app;
 GRANT SELECT ON cron_adapter.scheduler_clock,cron_adapter.pg_stat_activity TO fixture_app;
 UPDATE cron.job SET username='fixture_app'`)
	if err != nil {
		t.Fatal(err)
	}
	admin := target
	for _, ddl := range []string{
		"ALTER SCHEMA " + pgx.Identifier{fixture.Schema}.Sanitize() + " OWNER TO fixture_app",
		"ALTER TABLE " + qualified(fixture.Schema, "candidate") + " OWNER TO fixture_app",
		"ALTER TABLE " + qualified(fixture.Schema, "healthy") + " OWNER TO fixture_app",
		"ALTER FUNCTION " + qualified(fixture.Schema, cronReadFunction) + "() OWNER TO fixture_app",
		"ALTER PROCEDURE " + qualified(fixture.Schema, cronProcedure) + "(integer) OWNER TO fixture_app",
	} {
		if _, err = admin.Exec(ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}
	cfg = cfg.Copy()
	cfg.User = "fixture_app"
	target, err = pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { target.Close(ctx) })
	objects, err := loadFixture(ctx, target, fixture.ID)
	if err != nil {
		t.Fatal(err)
	}
	objectJSON, _ := json.Marshal(objects)
	if _, err = state.Exec(ctx, "UPDATE "+journalTable+" SET objects=$2::jsonb,target=$3 WHERE id=$1", fixture.ID, string(objectJSON), journalTargetConfig(cfg, "local")); err != nil {
		t.Fatal(err)
	}
	f.Target.Owner = "fixture_app"
	f.Witness.Owner = "fixture_app"
	f.Read, err = readCronRoutine(ctx, target, fixture.Schema, cronReadFunction)
	if err != nil {
		t.Fatal(err)
	}
	f.Procedure, err = readCronRoutine(ctx, target, fixture.Schema, cronProcedure)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, "INSERT INTO cron_adapter.scheduler_clock VALUES($1)", now); err != nil {
		t.Fatal(err)
	}
	f.Expires = now.Add(time.Hour)
	f.Created = now.Add(-10 * time.Minute)
	if activation {
		f.Activated = time.Time{}
		f.ActivationStarted = time.Time{}
		f.FirstCycle = time.Time{}
		f.Target.Active = false
		if _, err = admin.Exec(ctx, "UPDATE cron.job SET active=false WHERE jobid=1"); err != nil {
			t.Fatal(err)
		}
	} else {
		f.FirstCycle = time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
		f.Activated = f.FirstCycle.Add(-30 * time.Second)
	}
	boundary := now.Truncate(time.Minute)
	for i := 0; i < 2; i++ {
		start := boundary.Add(time.Duration(2+i*2) * time.Second)
		if _, err = admin.Exec(ctx, "INSERT INTO cron.job_run_details VALUES($1,2,42,$2,$3,'succeeded')", 10+i, start, start.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if !activation {
		seedCronProviderEvidence(t, ctx, target, admin, state, fixture, f)
	}
	if err = writeCronFixture(ctx, state, fixture.ID, f); err != nil {
		t.Fatal(err)
	}
	if err = writeTrialStatus(ctx, state, fixture.ID, "cron_ready", nil, nil); err != nil {
		t.Fatal(err)
	}
	var direct bool
	if err = target.QueryRow(ctx, "SELECT has_any_column_privilege(current_user,'cron.job','UPDATE')").Scan(&direct); err != nil || direct {
		t.Fatalf("provider role must lack direct catalog UPDATE: %v %v", direct, err)
	}
	return ctx, cfg, admin, state, fixture, f
}

// Selected synthetic periods isolate the transaction test; they are never
// represented as measured scheduler traffic. Query identity/epochs are real.
func seedCronProviderEvidence(t *testing.T, ctx context.Context, target, admin, state *pgx.Conn, fixture ConsumerFixture, f CronFixture) {
	t.Helper()
	if _, err := target.Exec(ctx, cronQuery(fixture.Schema)); err != nil {
		t.Fatal(err)
	}
	tx, err := target.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := readLookupPlan(ctx, tx, "EXPLAIN (FORMAT JSON, VERBOSE) "+cronQuery(fixture.Schema))
	tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var c Counter
	if err = target.QueryRow(ctx, `SELECT s.dbid,s.userid,s.queryid,pg_postmaster_start_time(),i.stats_reset,s.stats_since FROM public.pg_stat_statements s CROSS JOIN public.pg_stat_statements_info i WHERE s.toplevel AND s.queryid=$1`, plan.QueryID).Scan(&c.Database, &c.Role, &c.Query, &c.Boot, &c.Reset, &c.Entry); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"counter_generation": []time.Time{c.Boot, c.Reset, c.Entry}})
	if _, err = state.Exec(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence) VALUES($1,$2,$3,$4,$5::jsonb)`, fixture.Enrollment.Project, c.Database, c.Role, c.Query, string(raw)); err != nil {
		t.Fatal(err)
	}
	if err = prepareCronPeriods(ctx, state); err != nil {
		t.Fatal(err)
	}
	metric := func(mean float64, n int64) CronMeasure {
		after := c
		after.Calls = n
		after.ExecutionMS = mean * float64(n)
		return CronMeasure{Before: c, After: after, Offered: n}
	}
	for i := 0; i < 3; i++ {
		cycle := f.FirstCycle.Add(time.Duration(i) * 2 * time.Minute)
		p := CronPeriod{Cycle: cycle, Busy: CronWindow{Start: cycle, End: cycle.Add(20 * time.Second), Target: metric(42, 40), Healthy: metric(1, 80), Write: metric(1, 40)}, Control: CronWindow{Start: cycle.Add(30 * time.Second), End: cycle.Add(50 * time.Second), Target: metric(2, 40), Healthy: metric(1, 80), Write: metric(1, 40)}, Quiet: CronWindow{Start: cycle.Add(time.Minute), End: cycle.Add(80 * time.Second), Healthy: metric(1, 80), Write: metric(1, 40)}, Blocks: []CronBlock{{At: cycle.Add(time.Second), RunID: int64(i + 1), JobID: 1, PID: 42, BackendStart: cycle, WaitingPID: 43, WaitingStart: cycle, Query: c.Query}}}
		raw, _ = json.Marshal(p)
		if _, err = state.Exec(ctx, "INSERT INTO pooltune_lab_state.cron_periods(fixture,cycle,payload) VALUES($1,$2,$3::jsonb)", fixture.ID, cycle, string(raw)); err != nil {
			t.Fatal(err)
		}
		if _, err = admin.Exec(ctx, "INSERT INTO cron.job_run_details VALUES($1,1,42,$2,$3,'succeeded')", i+1, cycle, cycle.Add(17*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
}

type cronProviderTrace struct {
	beforeAPI    func(context.Context)
	afterCommit  func(context.Context, *pgx.Conn)
	beforeCommit func(context.Context, *pgx.Conn)
	afterClock   func(context.Context, *pgx.Conn)
	committed    bool
	seen         bool
	apiCalls     int
	query        string
}

func (tr *cronProviderTrace) TraceQueryStart(ctx context.Context, c *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	tr.query = d.SQL
	if strings.Contains(d.SQL, "SELECT cron.alter_job(") {
		tr.apiCalls++
		if !tr.seen {
			tr.seen = true
			if tr.beforeAPI != nil {
				tr.beforeAPI(ctx)
			}
		}
	}
	if tr.seen && d.SQL == "commit" && tr.beforeCommit != nil {
		tr.beforeCommit(ctx, c)
	}
	return ctx
}
func (tr *cronProviderTrace) TraceQueryEnd(ctx context.Context, c *pgx.Conn, d pgx.TraceQueryEndData) {
	if tr.seen && tr.query == "commit" && d.Err == nil {
		tr.committed = true
		if tr.afterCommit != nil {
			tr.afterCommit(ctx, c)
		}
	}
	if tr.committed && strings.HasPrefix(tr.query, "SELECT clock_timestamp()") && d.Err == nil {
		tr.committed = false
		if tr.afterClock != nil {
			tr.afterClock(ctx, c)
		}
	}
}

func cronProviderRun(t *testing.T, ctx context.Context, cfg *pgx.ConnConfig, state *pgx.Conn, fixture ConsumerFixture, activation bool, modelCalls *int) (*CronFixture, *DispatchResult, error) {
	t.Helper()
	if activation {
		target, err := pgx.ConnectConfig(ctx, cfg)
		if err != nil {
			return nil, nil, err
		}
		defer target.Close(ctx)
		f, err := ActivateCronFixture(ctx, target, state, "local", fixture.ID)
		return &f, nil, err
	}
	result, err := DispatchFixtureCron(ctx, state, cfg, "local", fixture, func(_ context.Context, e IndexEvidence) (IndexProposal, error) {
		*modelCalls++
		return IndexProposal{RunID: e.RunID, Outcome: "propose", Repair: "cron", TableOID: e.TableOID, Column: e.Column, Reason: "qualified test periods"}, nil
	})
	return nil, result, err
}

func TestCronProviderGuardWithoutCatalogUpdate(t *testing.T) {
	for _, activation := range []bool{true, false} {
		name := "schedule"
		if activation {
			name = "activation"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cfg, admin, state, fixture, f := cronProviderFixture(t, activation)
			trace := &cronProviderTrace{}
			if !activation {
				trace.afterClock = func(ctx context.Context, _ *pgx.Conn) {
					if _, err := admin.Exec(ctx, `INSERT INTO cron.job_run_details SELECT 20,2,42,at+interval '1 second',at+interval '2 seconds','succeeded' FROM cron_adapter.scheduler_clock; INSERT INTO cron.job_run_details SELECT 21,2,42,at+interval '3 seconds',at+interval '4 seconds','succeeded' FROM cron_adapter.scheduler_clock;UPDATE cron_adapter.scheduler_clock SET at=at+interval '5 seconds'`); err != nil {
						t.Fatal(err)
					}
				}
			}
			guarded := cfg.Copy()
			guarded.Tracer = trace
			calls := 0
			activated, result, err := cronProviderRun(t, ctx, guarded, state, fixture, activation, &calls)
			if err != nil {
				t.Fatalf("supported API mutation failed: %v", err)
			}
			target, err := readCronJob(ctx, admin, f.Target.Name)
			if err != nil {
				t.Fatal(err)
			}
			witness, err := readCronJob(ctx, admin, f.Witness.Name)
			if err != nil || witness != f.Witness {
				t.Fatalf("witness effective values changed: %+v %v", witness, err)
			}
			if activation {
				if activated.Activated.IsZero() || !target.Active || target.Schedule != "*/2 * * * *" {
					t.Fatalf("activation not acknowledged: %+v %+v", activated, target)
				}
			} else if calls != 1 || result == nil || result.Outcome != "awaiting_verification" || target.Schedule != "1-59/2 * * * *" {
				t.Fatalf("wrong schedule handoff: calls=%d result=%+v target=%+v", calls, result, target)
			}
			if trace.apiCalls != 2 {
				t.Fatalf("expected witness protection and target write, got %d calls", trace.apiCalls)
			}
		})
	}
}

func TestCronProviderGuardPreservesConcurrentEditsAndIntent(t *testing.T) {
	changes := []struct {
		name, sql string
		job       int64
	}{
		{"target_schedule", "UPDATE cron.job SET schedule='*/3 * * * *' WHERE jobid=1", 1},
		{"target_command", "UPDATE cron.job SET command='SELECT 42' WHERE jobid=1", 1},
		{"target_active", "UPDATE cron.job SET active=NOT active WHERE jobid=1", 1},
		{"target_deleted", "DELETE FROM cron.job WHERE jobid=1", 1},
		{"target_unowned", "UPDATE cron.job SET username='other_owner' WHERE jobid=1", 1},
		{"witness_command", "UPDATE cron.job SET command='SELECT 42' WHERE jobid=2", 2},
		{"witness_stopped", "UPDATE cron.job SET active=false WHERE jobid=2", 2},
	}
	for _, activation := range []bool{true, false} {
		name := "schedule"
		if activation {
			name = "activation"
		}
		for _, change := range changes {
			t.Run(name+"/"+change.name, func(t *testing.T) {
				ctx, cfg, admin, state, fixture, f := cronProviderFixture(t, activation)
				trace := &cronProviderTrace{beforeAPI: func(ctx context.Context) {
					// This callback runs after actual snapshot validation and durable intent,
					// before the first API write. The competing connection commits its edit.
					var saved bool
					sql := "SELECT (cron_fixture->>'activation_started')::timestamptz>'2000-01-01' FROM " + journalTable + " WHERE id=$1"
					if !activation {
						sql = "SELECT status='cron_pending' AND report->>'intent_at' IS NOT NULL FROM " + journalTable + " WHERE id=$1"
					}
					if err := state.QueryRow(ctx, sql, fixture.ID).Scan(&saved); err != nil || !saved {
						t.Fatalf("API write before saved intent: %v %v", saved, err)
					}
					if _, err := admin.Exec(ctx, change.sql); err != nil {
						t.Fatal(err)
					}
				}}
				guarded := cfg.Copy()
				guarded.Tracer = trace
				calls := 0
				activated, _, err := cronProviderRun(t, ctx, guarded, state, fixture, activation, &calls)
				var conflict *pgconn.PgError
				if !errors.As(err, &conflict) || conflict.Code != "40001" || !trace.seen {
					t.Fatalf("concurrent edit must abort with 40001: %v (hook=%v)", err, trace.seen)
				}
				if activation && !activated.Activated.IsZero() {
					t.Fatal("aborted activation claimed acknowledgement")
				}
				untouched := f.Witness
				if change.job == 2 {
					untouched = f.Target
				}
				actual, e := readCronJob(ctx, admin, untouched.Name)
				if e != nil || actual != untouched {
					t.Fatalf("other job did not roll back: %+v %+v %v", actual, untouched, e)
				}
				var before string
				if e = admin.QueryRow(ctx, "SELECT coalesce(jsonb_agg(to_jsonb(j) ORDER BY jobid),'[]')::text FROM cron.job j").Scan(&before); e != nil {
					t.Fatal(e)
				}
				// A fresh caller must inspect saved intent, never retry the API/model.
				if !activation {
					if _, e = state.Exec(ctx, "UPDATE pooltune_worker.investigations SET lease_until=now()-interval '1 second'"); e != nil {
						t.Fatal(e)
					}
				}
				_, retry, retryErr := cronProviderRun(t, ctx, guarded, state, fixture, activation, &calls)
				if activation && retryErr == nil {
					t.Fatal("activation intent was silently retried")
				}
				if !activation && change.name != "target_deleted" && (retryErr != nil || retry == nil || retry.Outcome != "inconclusive" || calls != 1) {
					t.Fatalf("pending repair did not reconcile: %+v %v calls=%d", retry, retryErr, calls)
				}
				if !activation && change.name == "target_deleted" && (!errors.Is(retryErr, pgx.ErrNoRows) || calls != 1) {
					t.Fatalf("deleted job must remain unresolved without repeat: %v calls=%d", retryErr, calls)
				}
				var after string
				if e = admin.QueryRow(ctx, "SELECT coalesce(jsonb_agg(to_jsonb(j) ORDER BY jobid),'[]')::text FROM cron.job j").Scan(&after); e != nil || before != after {
					t.Fatalf("recovery overwrote independent edit: %s -> %s %v", before, after, e)
				}
				wantCalls := 2
				if change.job == 2 {
					wantCalls = 1
				}
				if trace.apiCalls != wantCalls {
					t.Fatalf("blind API repeat: %d", trace.apiCalls)
				}
			})
		}
	}
}

func TestCronProviderGuardRefusesChangedSnapshotBeforeWrites(t *testing.T) {
	for _, activation := range []bool{true, false} {
		name := "schedule"
		if activation {
			name = "activation"
		}
		for _, change := range []string{"UPDATE cron.job SET active=false WHERE jobid=2", "UPDATE cron.job SET command='SELECT 42' WHERE jobid=2", "UPDATE cron.job SET username='other_owner' WHERE jobid=1", "DELETE FROM cron.job WHERE jobid=1", "UPDATE cron.job SET active=NOT active WHERE jobid=1"} {
			t.Run(name+"/"+change, func(t *testing.T) {
				ctx, cfg, admin, state, fixture, _ := cronProviderFixture(t, activation)
				if _, err := admin.Exec(ctx, change); err != nil {
					t.Fatal(err)
				}
				var before string
				if err := admin.QueryRow(ctx, "SELECT coalesce(jsonb_agg(to_jsonb(j) ORDER BY jobid),'[]')::text FROM cron.job j").Scan(&before); err != nil {
					t.Fatal(err)
				}
				trace := &cronProviderTrace{}
				guarded := cfg.Copy()
				guarded.Tracer = trace
				calls := 0
				_, result, err := cronProviderRun(t, ctx, guarded, state, fixture, activation, &calls)
				if err == nil && (result == nil || result.Outcome != "inconclusive") {
					t.Fatalf("changed snapshot admitted: %+v %v", result, err)
				}
				if trace.apiCalls != 0 {
					t.Fatal("changed or stopped job was written")
				}
				var after string
				if err := admin.QueryRow(ctx, "SELECT coalesce(jsonb_agg(to_jsonb(j) ORDER BY jobid),'[]')::text FROM cron.job j").Scan(&after); err != nil || before != after {
					t.Fatalf("changed snapshot mutated: %s -> %s %v", before, after, err)
				}
			})
		}
	}
}

func TestCronProviderGuardLostWriteOrCommitAcknowledgementNeverRepeats(t *testing.T) {
	for _, activation := range []bool{true, false} {
		name := "schedule"
		if activation {
			name = "activation"
		}
		for _, committed := range []bool{false, true} {
			mode := "connection_lost_before_commit"
			if committed {
				mode = "commit_acknowledgement_lost"
			}
			t.Run(name+"/"+mode, func(t *testing.T) {
				ctx, cfg, admin, state, fixture, f := cronProviderFixture(t, activation)
				trace := &cronProviderTrace{}
				disconnect := func(ctx context.Context, c *pgx.Conn) {
					if err := c.Close(ctx); err != nil {
						t.Fatal(err)
					}
				}
				if committed {
					trace.afterCommit = disconnect
				} else {
					trace.beforeCommit = disconnect
				}
				guarded := cfg.Copy()
				guarded.Tracer = trace
				calls := 0
				_, _, err := cronProviderRun(t, ctx, guarded, state, fixture, activation, &calls)
				if err == nil || trace.apiCalls != 2 {
					t.Fatalf("missing injected acknowledgement failure: %v calls=%d", err, trace.apiCalls)
				}
				target, e := readCronJob(ctx, admin, f.Target.Name)
				if e != nil {
					t.Fatal(e)
				}
				if activation {
					if target.Active != committed {
						t.Fatalf("wrong transactional activation: %+v", target)
					}
				} else {
					want := "*/2 * * * *"
					if committed {
						want = "1-59/2 * * * *"
					}
					if target.Schedule != want {
						t.Fatalf("wrong transactional schedule: %+v", target)
					}
				}
				witness, e := readCronJob(ctx, admin, f.Witness.Name)
				if e != nil || witness != f.Witness {
					t.Fatalf("witness changed: %+v %v", witness, e)
				}
				if activation {
					saved, e := readCronFixture(ctx, state, fixture.ID)
					if e != nil || saved.ActivationStarted.IsZero() || !saved.Activated.IsZero() {
						t.Fatalf("lost activation acknowledgement was not retained: %+v %v", saved, e)
					}
				} else {
					var saved CronReport
					var raw []byte
					if e = state.QueryRow(ctx, "SELECT report FROM "+journalTable+" WHERE id=$1 AND status='cron_pending'", fixture.ID).Scan(&raw); e != nil {
						t.Fatal(e)
					}
					if e = json.Unmarshal(raw, &saved); e != nil || saved.IntentAt.IsZero() || !saved.CommitAt.IsZero() {
						t.Fatalf("lost repair acknowledgement was not retained: %+v %v", saved, e)
					}
					if _, e = state.Exec(ctx, "UPDATE pooltune_worker.investigations SET lease_until=now()-interval '1 second'"); e != nil {
						t.Fatal(e)
					}
				}
				_, result, retryErr := cronProviderRun(t, ctx, guarded, state, fixture, activation, &calls)
				if activation && retryErr == nil {
					t.Fatal("uncertain activation blindly repeated")
				}
				if !activation && (retryErr != nil || result == nil || result.Outcome != "inconclusive" || calls != 1) {
					t.Fatalf("uncertain repair was not inspected: %+v %v calls=%d", result, retryErr, calls)
				}
				after, e := readCronJob(ctx, admin, f.Target.Name)
				if e != nil || after != target || trace.apiCalls != 2 {
					t.Fatalf("retry mutated uncertain action: %+v -> %+v calls=%d error=%v", target, after, trace.apiCalls, e)
				}
			})
		}
	}
}
