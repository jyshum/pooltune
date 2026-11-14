package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"reflect"
	"time"
)

type CronRoutine struct {
	OID        uint32 `json:"oid"`
	Owner      uint32 `json:"owner"`
	Definition string `json:"definition"`
	Signature  string `json:"signature"`
	Private    bool   `json:"private"`
}
type CronJob struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Owner    string `json:"owner"`
	Database string `json:"database"`
	Command  string `json:"command"`
	Schedule string `json:"schedule"`
	Active   bool   `json:"active"`
}
type CronFixture struct {
	ActivationWitness         []CronRun      `json:"activation_witness,omitempty"`
	InheritedStatementTimeout string         `json:"inherited_statement_timeout"`
	InheritedLockTimeout      string         `json:"inherited_lock_timeout"`
	ExecutionMode             string         `json:"execution_mode"`
	CleanupTargetRuns         []CronRun      `json:"cleanup_target_runs,omitempty"`
	WitnessStopRequested      bool           `json:"diagnostic_witness_stop_requested,omitempty"`
	Target                    CronJob        `json:"target"`
	Witness                   CronJob        `json:"witness"`
	Read                      CronRoutine    `json:"read"`
	Procedure                 CronRoutine    `json:"procedure"`
	Generation                CronGeneration `json:"generation"`
	Created                   time.Time      `json:"created"`
	Expires                   time.Time      `json:"expires"`
	ActivationStarted         time.Time      `json:"activation_started"`
	Activated                 time.Time      `json:"activated"`
	FirstCycle                time.Time      `json:"first_cycle"`
}

func cronQuery(schema string) string {
	return "SELECT " + qualified(schema, cronReadFunction) + "() AS id"
}

type cronDB interface {
	trialReader
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func createCronRoutines(ctx context.Context, db cronDB, schema string, expires time.Time) error {
	// No procedure-level SET, SECURITY DEFINER or exception block: those prohibit COMMIT.
	// The clock guard makes incomplete work fail. Each lock acquisition is independently bounded.
	body := fmt.Sprintf(`DECLARE started timestamptz := pg_catalog.clock_timestamp(); i integer;
BEGIN
 IF holds < 1 OR holds > 200 THEN RAISE EXCEPTION 'invalid hold count'; END IF;
 IF EXISTS(SELECT FROM pg_catalog.pg_settings WHERE name='statement_timeout' AND setting::bigint BETWEEN 1 AND 19999) THEN RAISE EXCEPTION 'job session timeout cannot cover required full work'; END IF;
 IF started >= '%s'::timestamptz THEN RAISE EXCEPTION 'fixture lifetime expired'; END IF;
 FOR i IN 1..holds LOOP
  IF pg_catalog.clock_timestamp()-started >= interval '19 seconds' THEN RAISE EXCEPTION 'cron work budget exceeded'; END IF;
  PERFORM pg_catalog.set_config('lock_timeout','150ms',true);
  LOCK TABLE ONLY %s IN ACCESS EXCLUSIVE MODE;
  PERFORM pg_catalog.pg_sleep(0.080);
  COMMIT;
 END LOOP;
 IF pg_catalog.clock_timestamp()-started >= interval '19 seconds' THEN RAISE EXCEPTION 'cron work budget exceeded'; END IF;
END`, expires.UTC().Format(time.RFC3339Nano), qualified(schema, "candidate"))
	for _, sql := range []string{
		"CREATE PROCEDURE " + qualified(schema, cronProcedure) + "(holds integer) LANGUAGE plpgsql SECURITY INVOKER AS $cron$" + body + "$cron$",
		"REVOKE ALL ON PROCEDURE " + qualified(schema, cronProcedure) + "(integer) FROM PUBLIC",
		"CREATE FUNCTION " + qualified(schema, cronReadFunction) + "() RETURNS integer LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY INVOKER SET search_path=pg_catalog AS $read$ DECLARE answer integer; BEGIN SELECT id INTO STRICT answer FROM ONLY " + qualified(schema, "candidate") + " WHERE id=43; RETURN answer; END $read$",
		"REVOKE ALL ON FUNCTION " + qualified(schema, cronReadFunction) + "() FROM PUBLIC",
	} {
		if _, err := db.Exec(ctx, sql); err != nil {
			return err
		}
	}
	return nil
}
func readCronRoutine(ctx context.Context, db trialReader, schema, name string) (r CronRoutine, err error) {
	err = db.QueryRow(ctx, `SELECT p.oid,p.proowner,pg_get_functiondef(p.oid),pg_get_function_identity_arguments(p.oid),NOT p.prosecdef AND NOT EXISTS(SELECT FROM aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) a WHERE a.grantee<>p.proowner) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=$1 AND p.proname=$2`, schema, name).Scan(&r.OID, &r.Owner, &r.Definition, &r.Signature, &r.Private)
	return
}
func readCronJob(ctx context.Context, db trialReader, name string) (j CronJob, err error) {
	err = db.QueryRow(ctx, `SELECT jobid,jobname,username,database,command,schedule,active FROM cron.job WHERE jobname=$1`, name).Scan(&j.ID, &j.Name, &j.Owner, &j.Database, &j.Command, &j.Schedule, &j.Active)
	return
}
func sameCronJob(a, b CronJob, allowSchedule bool) bool {
	if a.ID <= 0 || b.ID != a.ID {
		return false
	}
	if allowSchedule {
		a.Schedule = ""
		b.Schedule = ""
	}
	a.Active = false
	b.Active = false
	return a == b
}
func readCronClock(ctx context.Context, db trialReader) (now time.Time, g CronGeneration, err error) {
	err = db.QueryRow(ctx, `SELECT clock_timestamp(),pid,backend_start,pg_postmaster_start_time() FROM pg_stat_activity WHERE backend_type='pg_cron launcher'`).Scan(&now, &g.PID, &g.Started, &g.Postmaster)
	return
}
func cronPrerequisites(ctx context.Context, db trialReader) error {
	var ok bool
	err := db.QueryRow(ctx, `SELECT e.extversion='1.6.4' AND current_setting('cron.timezone') IN ('GMT','UTC') AND current_setting('cron.log_run')='on' AND current_setting('cron.use_background_workers')='off' AND current_database()=current_setting('cron.database_name') AND current_user=session_user FROM pg_extension e WHERE e.extname='pg_cron'`).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("private Cron prerequisites unavailable")
	}
	return nil
}
func writeCronFixture(ctx context.Context, state *pgx.Conn, id string, f CronFixture) error {
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	tag, err := state.Exec(ctx, "UPDATE "+journalTable+" SET cron_fixture=$2::jsonb WHERE id=$1", id, string(raw))
	if err == nil && tag.RowsAffected() != 1 {
		return errors.New("missing Cron fixture journal")
	}
	return err
}
func readCronFixture(ctx context.Context, state *pgx.Conn, id string) (f CronFixture, err error) {
	var raw []byte
	err = state.QueryRow(ctx, "SELECT cron_fixture FROM "+journalTable+" WHERE id=$1", id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &f)
	}
	return
}
func seedCronFixture(ctx context.Context, target, state *pgx.Conn, id string) error {
	if err := cronPrerequisites(ctx, target); err != nil {
		return err
	}
	now, g, err := readCronClock(ctx, target)
	if err != nil {
		return err
	}
	f := CronFixture{Created: now, Expires: now.Add(25 * time.Minute), Generation: g}
	if err = target.QueryRow(ctx, "SELECT current_setting('statement_timeout'),current_setting('lock_timeout'),current_setting('cron.use_background_workers')").Scan(&f.InheritedStatementTimeout, &f.InheritedLockTimeout, &f.ExecutionMode); err != nil {
		return err
	}
	var owner, database string
	if err = target.QueryRow(ctx, "SELECT current_user,current_database()").Scan(&owner, &database); err != nil {
		return err
	}
	f.Target = CronJob{Name: "pooltune_target_" + id, Owner: owner, Database: database, Command: "CALL " + qualified(trialSchema(id), cronProcedure) + "(200)", Schedule: cronOriginal}
	f.Witness = CronJob{Name: "pooltune_witness_" + id, Owner: owner, Database: database, Command: "SELECT 1", Schedule: cronWitnessSchedule}
	// Creation intent survives a committed target transaction whose acknowledgement is lost.
	if err = writeCronFixture(ctx, state, id, f); err != nil {
		return err
	}
	err = pgx.BeginFunc(ctx, target, func(tx pgx.Tx) error {
		if err := createCronRoutines(ctx, tx, trialSchema(id), f.Expires); err != nil {
			return err
		}
		f.Read, err = readCronRoutine(ctx, tx, trialSchema(id), cronReadFunction)
		if err != nil {
			return err
		}
		f.Procedure, err = readCronRoutine(ctx, tx, trialSchema(id), cronProcedure)
		if err != nil {
			return err
		}
		// Name collisions are refused before schedule(name) could update an existing job.
		for _, j := range []*CronJob{&f.Target, &f.Witness} {
			var exists bool
			if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT FROM cron.job WHERE jobname=$1)", j.Name).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return errors.New("owned Cron name collision")
			}
			if err = tx.QueryRow(ctx, "SELECT cron.schedule($1,$2,$3)", j.Name, j.Schedule, j.Command).Scan(&j.ID); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, "SELECT cron.alter_job($1,active := false)", j.ID); err != nil {
				return err
			}
		}
		return writeCronFixture(ctx, state, id, f)
	})
	return err
}
func CreateCronFixture(ctx context.Context, target, state *pgx.Conn, route string) (ConsumerFixture, error) {
	if err := cronPrerequisites(ctx, target); err != nil {
		return ConsumerFixture{}, err
	}
	if _, _, err := readCronClock(ctx, target); err != nil {
		return ConsumerFixture{}, err
	}
	f, err := createConsumerFixture(ctx, target, state, route, "cron")
	if err == nil || f.ID == "" {
		return f, err
	}
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tc, ce := pgx.ConnectConfig(cleanup, target.Config())
	if ce != nil {
		return f, errors.Join(err, ce)
	}
	defer closeVacuumConnection(tc)
	sc, ce := pgx.ConnectConfig(cleanup, state.Config())
	if ce != nil {
		return f, errors.Join(err, ce)
	}
	defer closeVacuumConnection(sc)
	return f, errors.Join(err, RemoveConsumerFixture(cleanup, tc, sc, route, f.ID))
}

func validateCronFixture(ctx context.Context, db trialReader, state *pgx.Conn, id string, f CronFixture, schedule string) error {
	objects, err := expectedFixture(ctx, state, id)
	if err != nil {
		return err
	}
	actual, err := loadFixture(ctx, db, id)
	if err != nil || actual != objects {
		return errors.Join(errors.New("Cron table identity changed"), err)
	}
	for _, pair := range []struct {
		name     string
		expected CronRoutine
	}{{cronReadFunction, f.Read}, {cronProcedure, f.Procedure}} {
		r, err := readCronRoutine(ctx, db, trialSchema(id), pair.name)
		if err != nil {
			return err
		}
		if !r.Private || !reflect.DeepEqual(r, pair.expected) {
			return errors.New("Cron routine identity changed")
		}
	}
	for _, j := range []CronJob{f.Target, f.Witness} {
		current, err := readCronJob(ctx, db, j.Name)
		if err != nil {
			return err
		}
		if !sameCronJob(j, current, j.ID == f.Target.ID) {
			return errors.New("Cron command, owner, database or identity changed")
		}
		if j.ID == f.Target.ID && current.Schedule != schedule {
			return errors.New("Cron schedule changed")
		}
		if j.ID == f.Witness.ID && current.Schedule != cronWitnessSchedule {
			return errors.New("witness schedule changed")
		}
	}
	return nil
}

// ActivateCronFixture records intent and activates within one safe odd-minute
// interval. FirstCycle is fixed before the command and rechecked after its commit.
func ActivateCronFixture(ctx context.Context, target, state *pgx.Conn, route, id string) (f CronFixture, err error) {
	if ValidateConsumerFixtureID(id) != nil {
		return f, errors.New("invalid Cron fixture")
	}
	var bound bool
	if err = state.QueryRow(ctx, "SELECT EXISTS(SELECT FROM "+journalTable+" WHERE id=$1 AND target=$2 AND status='cron_ready')", id, journalTarget(target, route)).Scan(&bound); err != nil || !bound {
		return f, errors.Join(err, errors.New("Cron fixture not ready on this target"))
	}
	if err = lockTrial(ctx, target); err != nil {
		return f, err
	}
	defer releaseTrialLock(target)
	f, err = readCronFixture(ctx, state, id)
	if err != nil {
		return f, err
	}
	if !f.ActivationStarted.IsZero() {
		return f, errors.New("activation already attempted; inspect durable state")
	}
	now, g, err := readCronClock(ctx, target)
	if err != nil {
		return f, err
	}
	cycle := cronCycle(now)
	if now.Before(cycle.Add(65*time.Second)) || !now.Before(cycle.Add(100*time.Second)) || g != f.Generation {
		return f, errors.New("activation requires stable scheduler and odd minute seconds 5 through 39")
	}
	if err = validateCronFixture(ctx, target, state, id, f, cronOriginal); err != nil {
		return f, err
	}
	// Witness progress while the target is still inactive excludes catch-up of
	// the prior even minute when the launcher next refreshes its job cache.
	f.ActivationWitness, err = readCronRuns(ctx, target, f.Witness.ID)
	if err != nil {
		return f, err
	}
	if err = validateCronWitness(f.Witness.ID, f.ActivationWitness, cycle.Add(time.Minute), cycle.Add(100*time.Second), now, f.Generation, g); err != nil {
		return f, err
	}
	observedAt := now
	err = pgx.BeginFunc(ctx, target, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, "SET LOCAL statement_timeout='2s'; SET LOCAL lock_timeout='250ms'"); e != nil {
			return e
		}
		// Existing trial ownership coordinates our callers; row locks also keep
		// job metadata stable through the final recheck and activation.
		if _, e := tx.Exec(ctx, "SELECT jobid FROM cron.job WHERE jobid=ANY($1::bigint[]) ORDER BY jobid FOR UPDATE", []int64{f.Target.ID, f.Witness.ID}); e != nil {
			return e
		}
		if err = validateCronFixture(ctx, tx, state, id, f, cronOriginal); err != nil {
			return err
		}
		targetJob, e := readCronJob(ctx, tx, f.Target.Name)
		if e != nil {
			return e
		}
		if targetJob.Active {
			return errors.New("target job unexpectedly active")
		}
		witness, e := readCronJob(ctx, tx, f.Witness.Name)
		if e != nil {
			return e
		}
		if !witness.Active {
			return errors.New("activation requires an active enrolled witness")
		}
		var history bool
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT FROM cron.job_run_details WHERE jobid=$1)", f.Target.ID).Scan(&history); e != nil {
			return e
		}
		if history {
			return errors.New("inactive target already has unaccounted history")
		}
		now, g, err = readCronClock(ctx, tx)
		if err != nil {
			return err
		}
		if now.Before(observedAt) {
			return errors.New("database clock reversed before activation")
		}
		if err = validateCronWitness(f.Witness.ID, f.ActivationWitness, cycle.Add(time.Minute), cycle.Add(100*time.Second), now, f.Generation, g); err != nil {
			return err
		}
		f.ActivationStarted = now
		f.FirstCycle = cycle.Add(2 * time.Minute)
		if err = writeCronFixture(ctx, state, id, f); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "SELECT cron.alter_job($1,active := true)", f.Target.ID)
		return err
	})
	if err != nil {
		return f, err
	}
	f.Activated, g, err = readCronClock(ctx, target)
	if err != nil {
		return f, err
	}
	if g != f.Generation || f.Activated.Before(f.ActivationStarted) || !f.Activated.Before(f.FirstCycle.Add(-15*time.Second)) {
		return f, errors.New("ambiguous activation acknowledgement; recovery required")
	}
	err = writeCronFixture(ctx, state, id, f)
	return
}

func InspectCronFixture(ctx context.Context, target, state *pgx.Conn, route, id string) (CronFixture, error) {
	if ValidateConsumerFixtureID(id) != nil {
		return CronFixture{}, errors.New("invalid Cron fixture")
	}
	var bound bool
	if err := state.QueryRow(ctx, "SELECT EXISTS(SELECT FROM "+journalTable+" WHERE id=$1 AND target=$2 AND status LIKE 'cron_%')", id, journalTarget(target, route)).Scan(&bound); err != nil {
		return CronFixture{}, err
	}
	if !bound {
		return CronFixture{}, errors.New("Cron fixture target mismatch")
	}
	return readCronFixture(ctx, state, id)
}

func StartCronWitness(ctx context.Context, target, state *pgx.Conn, route, id string) error {
	f, err := InspectCronFixture(ctx, target, state, route, id)
	if err != nil {
		return err
	}
	if err = validateCronFixture(ctx, target, state, id, f, cronOriginal); err != nil {
		return err
	}
	if f.Witness.Command != "SELECT 1" || f.Witness.Schedule != cronWitnessSchedule {
		return errors.New("witness not enrolled")
	}
	_, err = target.Exec(ctx, "SELECT cron.alter_job($1,active := true)", f.Witness.ID)
	return err
}

// RecoverCronFixtures discovers durable creation intents after a driver crash or
// lost creation acknowledgement. Unknown identities remain visible for review.
func RecoverCronFixtures(ctx context.Context, target, state *pgx.Conn, route string) error {
	if err := prepareJournal(ctx, state); err != nil {
		return err
	}
	rows, err := state.Query(ctx, "SELECT id FROM "+journalTable+" WHERE target=$1 AND status LIKE 'cron_%' ORDER BY created_at", journalTarget(target, route))
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = RemoveConsumerFixture(ctx, target, state, route, id); err != nil {
			return fmt.Errorf("Cron fixture %s retained for recovery: %w", id, err)
		}
	}
	return nil
}
