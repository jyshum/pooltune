package hostedquery

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
)

// Target work is never canceled. The only stop exception is the separately
// enrolled SELECT 1 diagnostic witness, which has no application work/deadline.
func cleanCronJobs(ctx context.Context, target, state *pgx.Conn, id string) error {
	f, err := readCronFixture(ctx, state, id)
	if err != nil {
		var empty bool
		e := target.QueryRow(ctx, "SELECT NOT EXISTS(SELECT FROM cron.job WHERE jobname=ANY($1::text[])) AND NOT EXISTS(SELECT FROM pg_proc WHERE pronamespace=to_regnamespace($2))", []string{"pooltune_target_" + id, "pooltune_witness_" + id}, trialSchema(id)).Scan(&empty)
		if e == nil && empty {
			return nil
		}
		return errors.Join(err, e)
	}
	targetJob, te := readCronJob(ctx, target, f.Target.Name)
	witness, we := readCronJob(ctx, target, f.Witness.Name)
	if errors.Is(te, pgx.ErrNoRows) && errors.Is(we, pgx.ErrNoRows) {
		return waitCronTerminal(ctx, target, []int64{f.Target.ID, f.Witness.ID})
	}
	if te != nil || we != nil {
		return errors.Join(te, we, errors.New("partial Cron job identity requires review"))
	}
	for _, pair := range []struct {
		expected *CronJob
		actual   CronJob
	}{{&f.Target, targetJob}, {&f.Witness, witness}} {
		expected := *pair.expected
		if expected.ID == 0 {
			expected.ID = pair.actual.ID
		}
		if !sameCronJob(expected, pair.actual, pair.expected == &f.Target) {
			return errors.New("preserving changed or foreign Cron job")
		}
		pair.expected.ID = pair.actual.ID
	}
	if targetJob.Schedule != cronOriginal && targetJob.Schedule != cronAlternate {
		return errors.New("unknown target schedule preserved")
	}
	if witness.Command != "SELECT 1" || witness.Schedule != cronWitnessSchedule {
		return errors.New("unknown witness preserved")
	}
	if err = writeCronFixture(ctx, state, id, f); err != nil {
		return err
	}
	runs, err := readCronRuns(ctx, target, targetJob.ID)
	if err != nil {
		return err
	}
	now, g, err := readCronClock(ctx, target)
	if err != nil {
		return err
	}
	next := cronCycle(now).Add(2 * time.Minute)
	due := cronCycle(now)
	if targetJob.Schedule == cronAlternate {
		due = due.Add(time.Minute)
		if now.Before(due) {
			next = due
			due = due.Add(-2 * time.Minute)
		} else {
			next = due.Add(2 * time.Minute)
		}
	}
	if targetJob.Active {
		if g != f.Generation || now.Before(due.Add(25*time.Second)) || !now.Before(next.Add(-15*time.Second)) {
			return errors.New("target cleanup waits for terminal execution and boundary margin")
		}
		ws, err := readCronRuns(ctx, target, witness.ID)
		if err != nil {
			return err
		}
		if err = validateCronWitness(witness.ID, ws, due.Add(20*time.Second), next.Add(-15*time.Second), now, f.Generation, g); err != nil {
			return err
		}
		if f.FirstCycle.IsZero() {
			return errors.New("unknown activation lifetime preserved")
		}
		// Complete lifetime accounting excludes invisible pending original work.
		var firstFuture *time.Time
		var rawTime *time.Time
		if err = state.QueryRow(ctx, "SELECT (report->>'first_future_cycle')::timestamptz FROM "+journalTable+" WHERE id=$1", id).Scan(&rawTime); err != nil {
			return err
		}
		firstFuture = rawTime
		expected := 0
		for c := f.FirstCycle; !c.After(cronCycle(now)); c = c.Add(2 * time.Minute) {
			d := c
			if firstFuture != nil && !firstFuture.IsZero() && !c.Before(*firstFuture) {
				d = d.Add(time.Minute)
			}
			if !d.After(now) {
				expected++
			}
		}
		if len(runs) != expected {
			return errors.New("target lifetime history incomplete or duplicated; cleanup preserved")
		}
	}
	for _, r := range runs {
		if r.End.IsZero() || r.End.Unix() <= 0 || (r.Status != "succeeded" && r.Status != "failed") {
			return errors.New("target run still active; cleanup deferred")
		}
	}
	// Archive all selected history before removing metadata. History is not deleted.
	f.CleanupTargetRuns = runs
	f.WitnessStopRequested = true
	if err = writeCronFixture(ctx, state, id, f); err != nil {
		return err
	}
	err = pgx.BeginFunc(ctx, target, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout='2s'; SET LOCAL lock_timeout='250ms'"); err != nil {
			return err
		}
		currentNow, currentGeneration, err := readCronClock(ctx, tx)
		if err != nil {
			return err
		}
		if targetJob.Active && (currentGeneration != g || !currentNow.Before(next.Add(-15*time.Second))) {
			return errors.New("cleanup boundary changed")
		}
		for _, j := range []CronJob{targetJob, witness} {
			current, err := readCronJob(ctx, tx, j.Name)
			if err != nil {
				return err
			}
			if current != j {
				return errors.New("job identity changed before cleanup")
			}
		}
		var active bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM cron.job_run_details WHERE jobid=$1 AND (end_time IS NULL OR status NOT IN ('succeeded','failed')))`, targetJob.ID).Scan(&active); err != nil {
			return err
		}
		if active {
			return errors.New("target work active before unschedule")
		}
		// Refuse changed routines or table ownership before removing either job.
		// cleanTrial repeats these checks before dropping the private objects.
		if err := validateCronFixture(ctx, tx, state, id, f, targetJob.Schedule); err != nil {
			return err
		}
		for _, j := range []CronJob{targetJob, witness} {
			var removed bool
			if err := tx.QueryRow(ctx, "SELECT cron.unschedule($1::bigint)", j.ID).Scan(&removed); err != nil {
				return err
			}
			if !removed {
				return errors.New("owned Cron job not removed")
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return waitCronTerminal(ctx, target, []int64{targetJob.ID, f.Witness.ID})
}

// A lost unschedule acknowledgement must re-enter this check even when both
// catalog rows are gone. Removing metadata is not proof the backend terminated.
func waitCronTerminal(ctx context.Context, target *pgx.Conn, ids []int64) error {
	for {
		var active bool
		err := target.QueryRow(ctx, `SELECT EXISTS(SELECT FROM cron.job_run_details r WHERE r.jobid=ANY($1::bigint[]) AND (r.end_time IS NULL OR r.status NOT IN ('succeeded','failed'))) OR EXISTS(SELECT FROM cron.job_run_details r JOIN pg_stat_activity a ON a.pid=r.job_pid WHERE r.jobid=ANY($1::bigint[]) AND a.backend_start BETWEEN r.start_time-interval '5 seconds' AND COALESCE(r.end_time,clock_timestamp()))`, ids).Scan(&active)
		if err != nil {
			return err
		}
		if !active {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}
func cleanCronRoutines(ctx context.Context, tx pgx.Tx, state *pgx.Conn, id, status string) error {
	var n int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM pg_proc WHERE pronamespace=to_regnamespace($1)", trialSchema(id)).Scan(&n); err != nil {
		return err
	}
	if n == 0 && status == "cron_preparing" {
		return nil
	}
	if n != 2 {
		return errors.New("unexpected Cron fixture routines")
	}
	f, err := readCronFixture(ctx, state, id)
	if err != nil {
		return err
	}
	for _, p := range []struct {
		name     string
		expected CronRoutine
		kind     string
		args     string
	}{{cronReadFunction, f.Read, "FUNCTION", ""}, {cronProcedure, f.Procedure, "PROCEDURE", "integer"}} {
		actual, err := readCronRoutine(ctx, tx, trialSchema(id), p.name)
		if err != nil {
			return err
		}
		if actual != p.expected || !actual.Private {
			return errors.New("changed Cron routine preserved")
		}
		if _, err = tx.Exec(ctx, "DROP "+p.kind+" "+qualified(trialSchema(id), p.name)+"("+p.args+") RESTRICT"); err != nil {
			return err
		}
	}
	return nil
}
func isCronStatus(s string) bool { return strings.HasPrefix(s, "cron_") }
