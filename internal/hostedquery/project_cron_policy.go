package hostedquery

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func verifyProjectCronHandoff(ctx context.Context, tx pgx.Tx, req ProjectCronRequest, now time.Time, generation CronGeneration) (time.Time, error) {
	if req.ObservedAt.IsZero() || req.ObservedAt.After(now) || now.Sub(req.ObservedAt) > 30*time.Second ||
		!req.Generation.same(generation) || generation.PID <= 0 || generation.Started.IsZero() || generation.Postmaster.IsZero() || len(req.Periods) != 3 {
		return time.Time{}, errors.New("fresh attributed Cron periods and a matching live scheduler are required")
	}
	cycleDuration := time.Duration(req.Window.EveryMinutes) * time.Minute
	cycle := now.UTC().Truncate(cycleDuration)
	next := cycle.Add(cycleDuration)
	oldOffset := time.Duration(req.Window.CurrentMinute) * time.Minute
	newOffset := time.Duration(req.Window.ProposedMinute) * time.Minute
	settledAt := cycle.Add(max(oldOffset+51*time.Second, newOffset+21*time.Second))
	deadline := next.Add(-8 * time.Second)
	if req.ownedHandoff {
		deadline = ownedCronHandoffDeadline(next)
	}
	if now.Before(settledAt) || !now.Before(deadline) {
		return time.Time{}, errors.New("Cron is outside the safe between-cycle handoff")
	}
	first := cycle.Add(-2 * cycleDuration)
	var ready bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM cron.job w JOIN cron.job t ON t.jobid=$1
		WHERE w.jobid=$2 AND w.username=t.username AND w.database=t.database AND w.active AND w.command='SELECT 1' AND w.schedule='2 seconds')
		AND NOT EXISTS(SELECT FROM cron.job_run_details WHERE jobid=$1 AND (end_time IS NULL OR status IS NULL OR status NOT IN ('succeeded','failed')
		  OR ($3::boolean AND start_time<$4::timestamptz)))`,
		req.Target.JobID, req.Target.WitnessJobID, req.ownedHandoff, first).Scan(&ready)
	if err != nil || !ready {
		return time.Time{}, errors.New("Cron job is active or its owned scheduler witness is missing")
	}
	witnessSince := now.Add(-30 * time.Second)
	if req.ownedHandoff && witnessSince.Before(cycle.Add(newOffset)) {
		witnessSince = cycle.Add(newOffset)
	}
	witness, err := projectCronRuns(ctx, tx, req.Target.WitnessJobID, witnessSince, now)
	if err != nil || validateCronWitness(req.Target.WitnessJobID, witness, witnessSince, next, now, req.Generation, generation) != nil {
		return time.Time{}, errors.New("Cron scheduler did not record two distinct recent witness runs")
	}
	history, err := projectCronRuns(ctx, tx, req.Target.JobID, first, now)
	if err != nil || len(history) != 3 {
		return time.Time{}, errors.New("Cron scheduler history did not record one run in each of three preceding cycles")
	}
	for i, p := range req.Periods {
		start := first.Add(time.Duration(i) * cycleDuration)
		if err := verifyProjectCronPeriod(req, p, start, start.Add(oldOffset), start.Add(newOffset), history[i]); err != nil {
			return time.Time{}, err
		}
	}
	return next, nil
}

func verifyProjectCronPeriod(req ProjectCronRequest, period CronPeriod, cycle, oldDue, alternateDue time.Time, run CronRun) error {
	if !period.Cycle.Equal(cycle) || run.ID <= 0 || run.JobID != req.Target.JobID || run.PID <= 0 || run.Status != "succeeded" ||
		run.Start.Before(oldDue) || !run.End.After(run.Start) || run.End.After(period.Busy.End) ||
		run.End.Sub(run.Start) > time.Duration(req.Window.MaxRuntimeSeconds)*time.Second ||
		run.End.After(cycle.Add(time.Duration(req.Window.DeadlineSeconds)*time.Second)) {
		return errors.New("Cron run is missing, late or outside its admitted duration")
	}
	ref := req.Periods[0].Busy
	for i, w := range []CronWindow{period.Busy, period.Control, period.Quiet} {
		at := []time.Time{oldDue, oldDue.Add(30 * time.Second), alternateDue}[i]
		if w.Start.Before(at) || w.Start.After(at.Add(time.Second)) || w.End.Before(at.Add(20*time.Second)) || w.End.After(at.Add(21*time.Second)) {
			return errors.New("Cron traffic windows are missing or out of phase")
		}
		for j, m := range []CronMeasure{w.Target, w.Healthy, w.Write} {
			base := []CronMeasure{ref.Target, ref.Healthy, ref.Write}[j]
			if m.Before.Database != req.Target.DatabaseOID || !m.Before.Boot.Equal(req.Generation.Postmaster) ||
				!sameCronCounter(m.Before, base.Before) || m.Errors != 0 || m.Missed != 0 {
				return errors.New("Cron traffic identity, generation or protection changed")
			}
			if i == 2 && j == 0 {
				delta, err := counterDelta(m.Before, m.After)
				if err != nil || m.Offered != 0 || delta.Calls != 0 {
					return errors.New("owner-approved quiet window contains target traffic")
				}
				continue
			}
			if m.Offered != base.Offered || m.Offered < 20 {
				return errors.New("Cron traffic volume is not comparable")
			}
			if _, err := cronMean(m); err != nil {
				return err
			}
		}
	}
	busy, _ := cronMean(period.Busy.Target)
	control, _ := cronMean(period.Control.Target)
	if busy-control < 20 || busy < 1.3*control || len(period.Blocks) == 0 {
		return errors.New("Cron evidence lacks material repeated attributable contention")
	}
	for _, b := range period.Blocks {
		if b.RunID != run.ID || b.JobID != run.JobID || b.PID != run.PID || b.WaitingPID <= 0 || b.WaitingPID == b.PID ||
			b.BackendStart.IsZero() || b.WaitingStart.IsZero() || b.BackendStart.After(b.At) || b.WaitingStart.After(b.At) ||
			b.Query != period.Busy.Target.Before.Query || b.At.Before(run.Start) || b.At.After(run.End) || b.At.Before(period.Busy.Start) || b.At.After(period.Busy.End) {
			return errors.New("Cron contention is not bound to the observed job run")
		}
	}
	return nil
}

type projectCronHistoryReader interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func projectCronRuns(ctx context.Context, db projectCronHistoryReader, job int64, since, until time.Time) ([]CronRun, error) {
	rows, err := db.Query(ctx, `SELECT runid,jobid,COALESCE(job_pid,0),start_time,COALESCE(end_time,'epoch'::timestamptz),COALESCE(status,'')
		FROM cron.job_run_details WHERE jobid=$1 AND start_time>=$2 AND start_time<$3 ORDER BY start_time,runid LIMIT 64`, job, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (r CronRun, err error) {
		err = row.Scan(&r.ID, &r.JobID, &r.PID, &r.Start, &r.End, &r.Status)
		return
	})
}
