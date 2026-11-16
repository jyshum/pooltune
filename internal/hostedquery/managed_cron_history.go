package hostedquery

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ManagedCronEnrollment is an exact saved job selection, not a mutation request.
// It deliberately has no caller-supplied SQL or fabricated request counts.
type ManagedCronEnrollment struct {
	Target                            ProjectCronTarget
	Fingerprint, Generation, Schedule string
	Window                            ProjectCronWindowPlan
	After                             bool // Only for read-only follow-up bound by the dispatcher to its native action receipt.
}

type ManagedCronHistory struct {
	ObservedAt       time.Time `json:"observed_at"`
	Runs             []CronRun `json:"runs"`
	SchedulerVisible bool      `json:"scheduler_visible"`
	ProviderReady    bool      `json:"provider_ready"`
	Missing          []string  `json:"missing"`
}

// ReadManagedCronHistory reads genuine provider history for the saved owner and
// command. The caller must first verify the connected project installation.
// It does not provision witnesses, call the job, or change any schedule.
func ReadManagedCronHistory(ctx context.Context, db *pgx.Conn, enrolled ManagedCronEnrollment) (out ManagedCronHistory, err error) {
	t := enrolled.Target
	if db == nil || t.Project == "" || t.DatabaseOID == 0 || t.OwnerOID == 0 || t.JobID <= 0 || t.Name == "" || len(t.CommandDigest) != 64 || len(enrolled.Fingerprint) != 64 || enrolled.Generation == "" {
		return out, errors.New("missing exact enrolled scheduling identity")
	}
	original, alternate, err := enrolled.Window.schedules()
	expected := original
	if enrolled.After {
		expected = alternate
	}
	if err != nil || expected != enrolled.Schedule {
		return out, errors.New("enrolled schedule no longer matches the approved window")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	err = pgx.BeginTxFunc(ctx, db, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL search_path=pg_catalog;SET LOCAL statement_timeout='750ms';SET LOCAL lock_timeout='100ms'`); err != nil {
			return err
		}
		var pid uint32
		var exact bool
		err := tx.QueryRow(ctx, `SELECT pg_backend_pid(),clock_timestamp(),EXISTS(SELECT FROM cron.job j JOIN pg_database d ON d.datname=current_database()
 WHERE d.oid=$1 AND current_user=session_user AND current_user::regrole::oid=$2 AND j.username::text=current_user AND j.database::text=current_database()
 AND j.jobid=$3 AND j.jobname=$4 AND j.active AND j.schedule=$5 AND j.xmin::text=$6
 AND encode(sha256(convert_to(j.command,'UTF8')),'hex')=$7 AND encode(sha256(convert_to(to_jsonb(j)::text,'UTF8')),'hex')=$8)`, t.DatabaseOID, t.OwnerOID, t.JobID, t.Name, enrolled.Schedule, enrolled.Generation, t.CommandDigest, enrolled.Fingerprint).Scan(&pid, &out.ObservedAt, &exact)
		if err != nil {
			return err
		}
		if !exact || pid != db.PgConn().PID() {
			return errors.New("enrolled scheduling identity changed")
		}
		since := out.ObservedAt.Add(-3 * time.Duration(enrolled.Window.EveryMinutes) * time.Minute)
		out.Runs, err = projectCronRuns(ctx, tx, t.JobID, since, out.ObservedAt)
		if err != nil {
			return err
		}
		if len(out.Runs) < 3 {
			out.Missing = append(out.Missing, "three_completed_cycles")
		}
		for _, run := range out.Runs {
			if run.Status != "succeeded" || !run.End.After(run.Start) || run.End.Sub(run.Start) > time.Duration(enrolled.Window.MaxRuntimeSeconds)*time.Second {
				out.Missing = append(out.Missing, "successful_on_time_runs")
				break
			}
		}
		// Settings access can be unavailable without making genuine history
		// disappear. Isolate those optional catalog probes with a savepoint.
		probe, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		out.ProviderReady = checkProjectCronProvider(ctx, probe) == nil
		_, generation, clockErr := readCronClock(ctx, probe)
		out.SchedulerVisible = clockErr == nil && generation.PID > 0 && !generation.Started.IsZero() && !generation.Postmaster.IsZero()
		if err := probe.Rollback(ctx); err != nil {
			return err
		}
		if !out.ProviderReady {
			out.Missing = append(out.Missing, "scheduler_prerequisites")
		}
		if !out.SchedulerVisible {
			out.Missing = append(out.Missing, "scheduler_identity")
		}
		out.Missing = append(out.Missing, "owned_scheduler_witness", "matched_completed_database_work_windows")
		return nil
	})
	return out, err
}
