package hostedquery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

type ProjectCronTarget struct {
	Project, Name, CommandDigest string
	DatabaseOID, OwnerOID        uint32
	JobID, WitnessJobID          int64
}

// ProjectCronWindowPlan preserves one execution per fixed UTC cycle. The
// offsets are minute positions from the start of each cycle; the deadline is
// measured from that same cycle boundary.
type ProjectCronWindowPlan struct {
	EveryMinutes, CurrentMinute, ProposedMinute int
	DeadlineSeconds, MaxRuntimeSeconds          int
}

type ProjectCronSnapshot struct {
	Schedule, Fingerprint, Generation string
}

type ProjectCronRequest struct {
	Target       ProjectCronTarget
	Window       ProjectCronWindowPlan
	Baseline     ProjectCronSnapshot
	Generation   CronGeneration
	RunID        string
	ObservedAt   time.Time
	Periods      []CronPeriod
	ownedHandoff bool
}

type ProjectCronIntent struct {
	Target           ProjectCronTarget
	Window           ProjectCronWindowPlan
	Before           ProjectCronSnapshot
	Generation       CronGeneration
	RunID            string
	ProposedSchedule string
	FirstFutureCycle time.Time
}

type ProjectCronGuards struct {
	CheckPermission func(context.Context, ProjectCronIntent) error
	CheckCapacity   func(context.Context, ProjectCronIntent) error
	// RecordIntent must commit the exact action in independent durable state.
	RecordIntent func(context.Context, ProjectCronIntent) error
}

type ProjectCronResult struct {
	WriteAttempted      bool
	After               ProjectCronSnapshot
	CommitAt            time.Time
	FirstFutureCycle    time.Time
	PostCommitWitnesses []CronRun
}

type projectCronProvider struct {
	check func(context.Context, trialReader) error
	clock func(context.Context, trialReader) (time.Time, CronGeneration, error)
}

func (p ProjectCronWindowPlan) schedules() (string, string, error) {
	if p.EveryMinutes < 2 || p.EveryMinutes > 60 || 60%p.EveryMinutes != 0 || p.CurrentMinute < 0 || p.ProposedMinute < 0 ||
		p.CurrentMinute == p.ProposedMinute || p.CurrentMinute >= p.EveryMinutes || p.ProposedMinute >= p.EveryMinutes ||
		p.MaxRuntimeSeconds < 1 || p.MaxRuntimeSeconds > 20 || p.DeadlineSeconds > p.EveryMinutes*60 ||
		p.DeadlineSeconds < max(p.CurrentMinute, p.ProposedMinute)*60+p.MaxRuntimeSeconds {
		return "", "", errors.New("Cron change does not preserve an approved periodic window and deadline")
	}
	cron := func(offset int) string {
		if p.EveryMinutes == 60 {
			return fmt.Sprintf("%d * * * *", offset)
		}
		if offset == 0 {
			return fmt.Sprintf("*/%d * * * *", p.EveryMinutes)
		}
		return fmt.Sprintf("%d-59/%d * * * *", offset, p.EveryMinutes)
	}
	return cron(p.CurrentMinute), cron(p.ProposedMinute), nil
}

// ReadProjectCron reads an enrolled job on the verified owner route. The
// command is compared by digest and stays in PostgreSQL.
func ReadProjectCron(ctx context.Context, db *pgx.Conn, target ProjectCronTarget) (ProjectCronSnapshot, error) {
	if db == nil {
		return ProjectCronSnapshot{}, errors.New("missing Cron catalog connection")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return readProjectCronJob(ctx, db, target)
}

const projectCronJobSQL = `
		SELECT j.schedule,pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to((pg_catalog.to_jsonb(j)-'schedule')::text,'UTF8')),'hex'),j.xmin::text
		FROM cron.job j JOIN pg_catalog.pg_roles r ON r.rolname::text=j.username::text
		JOIN pg_catalog.pg_database d ON d.datname=pg_catalog.current_database()
		WHERE d.oid=$1 AND r.oid=$2 AND j.jobid=$3 AND j.jobname::text=$4 AND j.active
		  AND current_user=session_user AND r.rolname=current_user AND j.database::text=d.datname::text
		  AND pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to(j.command,'UTF8')),'hex')=$5
	`

func validProjectCronTarget(t ProjectCronTarget) bool {
	return t.Project != "" && t.DatabaseOID != 0 && t.OwnerOID != 0 && t.JobID > 0 && t.WitnessJobID > 0 &&
		t.WitnessJobID != t.JobID && t.Name != "" && len(t.CommandDigest) == 64
}

func readProjectCronJob(ctx context.Context, db trialReader, t ProjectCronTarget) (ProjectCronSnapshot, error) {
	if !validProjectCronTarget(t) {
		return ProjectCronSnapshot{}, errors.New("missing enrolled Cron target identity")
	}
	return scanProjectCronJob(db.QueryRow(ctx, projectCronJobSQL, t.DatabaseOID, t.OwnerOID, t.JobID, t.Name, t.CommandDigest))
}

func scanProjectCronJob(row pgx.Row) (ProjectCronSnapshot, error) {
	var out ProjectCronSnapshot
	err := row.Scan(&out.Schedule, &out.Fingerprint, &out.Generation)
	if err != nil {
		return out, fmt.Errorf("read exact active Cron job as owner: %w", err)
	}
	return out, nil
}

const projectCronWitnessSQL = `
		SELECT pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to(pg_catalog.to_jsonb(w)::text,'UTF8')),'hex')
		FROM cron.job w JOIN cron.job t ON t.jobid=$1
		WHERE w.jobid=$2 AND t.username::text=current_user AND t.database::text=current_database()
		  AND w.username=t.username AND w.database=t.database AND w.active
		  AND w.command='SELECT 1' AND w.schedule='2 seconds'
	`

func queueProjectCronPair(batch *pgx.Batch, target ProjectCronTarget) {
	batch.Queue(projectCronJobSQL, target.DatabaseOID, target.OwnerOID, target.JobID, target.Name, target.CommandDigest)
	batch.Queue(projectCronWitnessSQL, target.JobID, target.WitnessJobID)
}

func readProjectCronPair(ctx context.Context, db cronCatalogReader, target ProjectCronTarget) (ProjectCronSnapshot, string, error) {
	if !validProjectCronTarget(target) {
		return ProjectCronSnapshot{}, "", errors.New("missing enrolled Cron target identity")
	}
	batch := &pgx.Batch{}
	queueProjectCronPair(batch, target)
	return scanProjectCronPair(db.SendBatch(ctx, batch))
}

func scanProjectCronPair(results pgx.BatchResults) (ProjectCronSnapshot, string, error) {
	job, err := scanProjectCronJob(results.QueryRow())
	if err != nil {
		return job, "", errors.Join(err, results.Close())
	}
	var witness string
	err = results.QueryRow().Scan(&witness)
	return job, witness, errors.Join(err, results.Close())
}

func changeProjectCronPair(ctx context.Context, tx pgx.Tx, target ProjectCronTarget, proposed string) (ProjectCronSnapshot, string, error) {
	batch := &pgx.Batch{}
	// PostgreSQL runs these in order in the already-open Repeatable Read
	// transaction. A row conflict aborts the transaction, including both writes.
	batch.Queue(`SELECT cron.alter_job($1::bigint,active := true)`, target.WitnessJobID)
	batch.Queue(`SELECT cron.alter_job($1::bigint,schedule := $2::text)`, target.JobID, proposed)
	queueProjectCronPair(batch, target)
	results := tx.SendBatch(ctx, batch)
	for range 2 {
		if _, err := results.Exec(); err != nil {
			return ProjectCronSnapshot{}, "", errors.Join(err, results.Close())
		}
	}
	return scanProjectCronPair(results)
}

const projectCronProviderCatalogSQL = `EXISTS (
		SELECT FROM pg_catalog.pg_extension e JOIN pg_catalog.pg_depend dep ON dep.refobjid=e.oid
		WHERE e.extname='pg_cron' AND e.extversion='1.6.4' AND dep.deptype='e' AND dep.refclassid='pg_catalog.pg_extension'::regclass
		  AND dep.classid='pg_catalog.pg_proc'::regclass AND dep.objid=pg_catalog.to_regprocedure('cron.alter_job(bigint,text,text,text,text,boolean)')
	) AND NOT pg_catalog.pg_is_in_recovery()
	  AND current_user=session_user AND NOT pg_catalog.has_any_column_privilege(current_user,'cron.job','UPDATE')
	  AND pg_catalog.has_function_privilege(current_user,pg_catalog.to_regprocedure('cron.alter_job(bigint,text,text,text,text,boolean)'),'EXECUTE')`

func checkProjectCronProvider(ctx context.Context, db trialReader) error {
	const directSettings = ` AND COALESCE(pg_catalog.current_setting('cron.timezone',true) IN ('UTC','GMT'),false)
		AND pg_catalog.current_setting('cron.log_run',true)='on' AND pg_catalog.current_setting('cron.use_background_workers',true)='off'
		AND pg_catalog.current_setting('cron.database_name',true)=pg_catalog.current_database()`
	var valid bool
	err := db.QueryRow(ctx, "SELECT "+projectCronProviderCatalogSQL+directSettings).Scan(&valid)
	if err != nil || !valid {
		return errors.New("registered pg_cron schedule-only provider or runtime prerequisites unavailable")
	}
	return nil
}

// ApplyProjectCron changes only one already-authorized future schedule. A
// successful readback awaits actual future-cycle and workload verification.
// An error after WriteAttempted requires reconciliation before another action.
func ApplyProjectCron(ctx context.Context, cfg *pgx.ConnConfig, req ProjectCronRequest, guards ProjectCronGuards) (ProjectCronResult, error) {
	return applyProjectCron(ctx, cfg, req, guards, projectCronProvider{checkProjectCronProvider, readCronClock})
}

func applyProjectCron(ctx context.Context, cfg *pgx.ConnConfig, req ProjectCronRequest, guards ProjectCronGuards, provider projectCronProvider) (out ProjectCronResult, err error) {
	return applyProjectCronOn(ctx, cfg, nil, req, guards, provider)
}

func applyProjectCronOn(ctx context.Context, cfg *pgx.ConnConfig, db *pgx.Conn, req ProjectCronRequest, guards ProjectCronGuards, provider projectCronProvider) (out ProjectCronResult, err error) {
	if (cfg == nil && db == nil) || guards.CheckPermission == nil || guards.CheckCapacity == nil || guards.RecordIntent == nil || req.RunID == "" || len(req.RunID) > 64 {
		return out, errors.New("Cron change requires native route, permissions, capacity and durable intent")
	}
	original, proposed, err := req.Window.schedules()
	if err != nil || original != req.Baseline.Schedule {
		return out, errors.New("Cron job does not match the approved schedule pair")
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	phase := "writer_connection"
	var commitConfirmed bool
	var observedClock time.Time
	var futureCycle time.Time
	defer func() {
		if !req.ownedHandoff || err == nil {
			return
		}
		cause := err
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			cause = ctx.Err()
		}
		fields := []any{"phase", phase, "run", req.RunID, "job", req.Target.JobID, "elapsed_ms", elapsedMS(started),
			"write_attempted", out.WriteAttempted, "commit_confirmed", commitConfirmed, "category", ownedRepairErrorCategory(cause)}
		if !observedClock.IsZero() && !futureCycle.IsZero() {
			fields = append(fields, "observation_margin_ms", ownedCronHandoffDeadline(futureCycle).Sub(observedClock).Milliseconds())
		}
		slog.Info("owned Cron native action stopped", fields...)
	}()
	if db == nil {
		db, err = pgx.ConnectConfig(ctx, cfg.Copy())
		if err != nil {
			return out, err
		}
		defer closeVacuumConnection(db)
	}
	phase = "provider_preflight"
	var pid int32
	if err := db.QueryRow(ctx, "SELECT pg_catalog.pg_backend_pid()").Scan(&pid); err != nil || pid != int32(db.PgConn().PID()) {
		return out, errors.New("Cron change requires a native PostgreSQL session")
	}
	if _, err := db.Exec(ctx, "SET search_path=pg_catalog; SET statement_timeout='1s'; SET lock_timeout='250ms'"); err != nil {
		return out, err
	}
	if err := provider.check(ctx, db); err != nil {
		return out, err
	}
	var expected ProjectCronSnapshot
	var witnessBefore string
	var lastClock time.Time
	phase = "handoff_locks"
	err = pgx.BeginTxFunc(ctx, db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL idle_in_transaction_session_timeout='5s'"); err != nil {
			return err
		}
		var locked bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('pooltune-project-cron/'||$1::text,0))`, req.Target.JobID).Scan(&locked); err != nil || !locked {
			return errors.New("another Cron action holds this exact job")
		}
		phase = "handoff_history"
		now, generation, err := provider.clock(ctx, tx)
		if err != nil {
			return err
		}
		observedClock = now
		firstFuture, err := verifyProjectCronHandoff(ctx, tx, req, now, generation)
		if err != nil {
			return err
		}
		futureCycle = firstFuture
		phase = "handoff_identity"
		before, witness, err := readProjectCronPair(ctx, tx, req.Target)
		if err != nil || before != req.Baseline {
			return errors.New("Cron job identity or schedule changed before its handoff")
		}
		witnessBefore = witness
		intent := ProjectCronIntent{Target: req.Target, Window: req.Window, Before: before, Generation: generation,
			RunID: req.RunID, ProposedSchedule: proposed, FirstFutureCycle: firstFuture}
		for _, check := range []struct {
			phase string
			run   func(context.Context, ProjectCronIntent) error
		}{{"permission", guards.CheckPermission}, {"capacity", guards.CheckCapacity}, {"intent", guards.RecordIntent}, {"permission_recheck", guards.CheckPermission}} {
			phase = check.phase
			if err := check.run(ctx, intent); err != nil {
				return err
			}
		}
		phase = "pre_write_clock"
		lastClock, generation, err = provider.clock(ctx, tx)
		if !lastClock.IsZero() {
			observedClock = lastClock
		}
		latest := firstFuture.Add(-8 * time.Second)
		if req.ownedHandoff {
			latest = ownedCronHandoffDeadline(firstFuture)
		}
		if err != nil || lastClock.Before(now) || !req.Generation.same(generation) || !lastClock.Before(latest) || lastClock.Sub(req.ObservedAt) > 30*time.Second {
			return errors.New("Cron scheduler changed or passed its safe handoff window")
		}
		out.WriteAttempted = true
		phase = "schedule_write"
		// The witness is already active in this snapshot. Its same-value API
		// update detects concurrent stops or edits and holds its row until commit.
		expected, witness, err = changeProjectCronPair(ctx, tx, req.Target, proposed)
		if err != nil {
			return err
		}
		phase = "transaction_identity"
		if expected.Schedule != proposed || expected.Fingerprint != before.Fingerprint || expected.Generation == before.Generation {
			return errors.New("Cron provider changed fields outside the approved schedule")
		}
		if witness != witnessBefore {
			return errors.New("Cron provider changed the owned scheduler witness")
		}
		out.FirstFutureCycle = firstFuture
		phase = "commit"
		return nil
	})
	if err != nil {
		return out, err
	}
	commitConfirmed = true
	phase = "commit_clock"
	witnessLimit := out.FirstFutureCycle
	if req.ownedHandoff {
		witnessLimit = ownedCronHandoffDeadline(witnessLimit)
	}
	// A successful COMMIT reply is the barrier. Read the provider clock before
	// catalog readback so that it does not consume a genuine later witness start.
	committed, generation, err := provider.clock(ctx, db)
	if !committed.IsZero() {
		observedClock = committed
	}
	if err != nil || committed.Before(lastClock) || !generation.same(req.Generation) || !committed.Before(witnessLimit) {
		return out, errors.New("Cron scheduler changed around the schedule commit")
	}
	phase = "post_commit_identity"
	actual, err := readProjectCronJob(ctx, db, req.Target)
	if err != nil || actual != expected {
		return out, errors.New("Cron change acknowledgement requires reconciliation")
	}
	phase = "post_commit_witnesses"
	out.PostCommitWitnesses, err = waitProjectCronWitnesses(ctx, db, req, provider, committed, witnessLimit)
	if err != nil {
		return out, err
	}
	phase = "acknowledgement_identity"
	actual, witness, err := readProjectCronPair(ctx, db, req.Target)
	if err != nil || actual != expected {
		return out, errors.New("Cron job changed before scheduler acknowledgement")
	}
	if witness != witnessBefore {
		return out, errors.New("owned Cron witness changed before scheduler acknowledgement")
	}
	out.After = actual
	out.CommitAt = committed
	if req.ownedHandoff {
		slog.Info("owned Cron native action acknowledged", "run", req.RunID, "job", req.Target.JobID,
			"elapsed_ms", elapsedMS(started), "commit_margin_ms", witnessLimit.Sub(committed).Milliseconds(),
			"witnesses", len(out.PostCommitWitnesses))
	}
	return out, nil
}

func waitProjectCronWitnesses(ctx context.Context, db *pgx.Conn, req ProjectCronRequest, provider projectCronProvider, committed, firstFuture time.Time) ([]CronRun, error) {
	deadline := committed.Add(7 * time.Second)
	if firstFuture.Before(deadline) {
		deadline = firstFuture
	}
	lastClock := committed
	for {
		now, generation, err := provider.clock(ctx, db)
		if err != nil || now.Before(lastClock) || !now.Before(deadline) || !generation.same(req.Generation) {
			return nil, errors.New("Cron post-commit scheduler progress is missing or changed; reconciliation required")
		}
		lastClock = now
		runs, err := projectCronRuns(ctx, db, req.Target.WitnessJobID, committed, now)
		if err != nil {
			return nil, err
		}
		if validateCronWitness(req.Target.WitnessJobID, runs, committed, deadline, now, req.Generation, generation) == nil {
			return runs, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}
