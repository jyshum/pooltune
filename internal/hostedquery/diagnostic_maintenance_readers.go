package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// These readers inspect only already-enrolled synthetic fixtures. They never
// invoke repair commands or the baseline batches that contain guard writes.
func VacuumDiagnosticEvidence(state *pgx.Conn, config *pgx.ConnConfig, route, fixture string) DiagnosticEvidenceReader {
	return maintenanceDiagnosticEvidence(state, config, route, fixture, "vacuum")
}
func MemoryDiagnosticEvidence(state *pgx.Conn, config *pgx.ConnConfig, route, fixture string) DiagnosticEvidenceReader {
	return maintenanceDiagnosticEvidence(state, config, route, fixture, "memory")
}
func CronDiagnosticEvidence(state *pgx.Conn, config *pgx.ConnConfig, route, fixture string) DiagnosticEvidenceReader {
	return maintenanceDiagnosticEvidence(state, config, route, fixture, "cron")
}

func maintenanceDiagnosticEvidence(state *pgx.Conn, config *pgx.ConnConfig, route, fixture, family string) DiagnosticEvidenceReader {
	return func(ctx context.Context, tool string, base IndexEvidence) (out IndexEvidence, available bool, err error) {
		switch tool {
		case "plan", "indexes", "analyze", "statistics", "vacuum", "memory", "cron":
		default:
			return out, false, errors.New("unknown diagnostic tool")
		}
		if tool != "plan" && tool != "indexes" && tool != family {
			return out, false, nil
		}
		column := int16(2)
		if family == "cron" {
			column = 1
		}
		if state == nil || config == nil || ValidateConsumerFixtureID(fixture) != nil || (route != "local" && route != "rookery") || base.RunID == "" || base.TableOID == 0 || base.Column != column || len(base.Columns) != 0 || base.Decision != Investigate || base.At.IsZero() || time.Since(base.At) < 0 || time.Since(base.At) > 30*time.Second {
			return out, false, errors.New("invalid maintenance diagnostic enrollment")
		}
		ctx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
		defer cancel()
		var status string
		var raw []byte
		binding := journalTargetConfig(config, route)
		if err = state.QueryRow(ctx, "SELECT status,report FROM "+journalTable+" WHERE id=$1 AND target=$2", fixture, binding).Scan(&status, &raw); err != nil {
			return out, false, err
		}
		if status != family+"_ready" && !(family == "cron" && status == "cron_waiting") {
			return out, false, errors.New("maintenance fixture is not enrolled for inspection")
		}
		objects, err := expectedFixture(ctx, state, fixture)
		if err != nil {
			return out, false, err
		}
		if objects.Candidate != base.TableOID {
			return out, false, errors.New("diagnostic target changed")
		}
		target, err := pgx.ConnectConfig(ctx, config)
		if err != nil {
			return out, false, err
		}
		defer closeVacuumConnection(target)
		// Capture the actual scheduler clock/generation using the same provider
		// connection as the consumer. Period timestamps below remain unchanged.
		var cronNow time.Time
		var generation CronGeneration
		if family == "cron" {
			cronNow, generation, err = readCronClock(ctx, target)
			if err != nil {
				return out, false, err
			}
		}
		out = IndexEvidence{RunID: base.RunID, Decision: base.Decision, TableOID: base.TableOID, Column: base.Column, Label: "lookup"}
		err = pgx.BeginTxFunc(ctx, target, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL statement_timeout='750ms'; SET LOCAL lock_timeout='250ms'; SET LOCAL max_parallel_workers_per_gather=0; SET LOCAL jit=off"); err != nil {
				return err
			}
			// ACCESS SHARE pins the tables against replacement without taking a repair
			// lock or blocking ordinary foreground writes during evidence collection.
			if _, err := tx.Exec(ctx, "LOCK TABLE ONLY "+qualified(trialSchema(fixture), "candidate")+", ONLY "+qualified(trialSchema(fixture), "healthy")+" IN ACCESS SHARE MODE NOWAIT"); err != nil {
				return err
			}
			switch family {
			case "vacuum":
				return readVacuumDiagnostic(ctx, tx, fixture, objects, raw, tool, &out)
			case "memory":
				return readMemoryDiagnostic(ctx, tx, state, fixture, objects, tool, &out)
			case "cron":
				return readCronDiagnostic(ctx, tx, state, fixture, objects, tool, cronNow, generation, &out)
			}
			return errors.New("unknown maintenance diagnostic family")
		})
		if err != nil {
			return out, false, err
		}
		var stillEnrolled bool
		if err = state.QueryRow(ctx, "SELECT EXISTS(SELECT FROM "+journalTable+" WHERE id=$1 AND target=$2 AND status=$3)", fixture, binding, status).Scan(&stillEnrolled); err != nil {
			return out, false, err
		}
		if !stillEnrolled {
			return out, false, errors.New("diagnostic enrollment changed during inspection")
		}
		if family == "cron" {
			now, current, e := readCronClock(ctx, target)
			if e != nil {
				return out, false, e
			}
			f, e := readCronFixture(ctx, state, fixture)
			if e != nil {
				return out, false, e
			}
			if !current.same(generation) || !now.Before(f.FirstCycle.Add(4*time.Minute+105*time.Second)) {
				return out, false, errors.New("scheduler changed or diagnostic handoff expired")
			}
		}
		out.At = time.Now().UTC()
		return out, true, nil
	}
}

func readVacuumDiagnostic(ctx context.Context, tx pgx.Tx, fixture string, objects fixtureObjects, raw []byte, tool string, out *IndexEvidence) error {
	var report VacuumReport
	if json.Unmarshal(raw, &report) != nil || report.Fixture != fixture || report.IndexOID == 0 {
		return errors.New("missing enrolled vacuum index identity")
	}
	if err := validateVacuumFixture(ctx, tx, fixture, objects, report.IndexOID); err != nil {
		return err
	}
	out.Existing = true
	if tool == "indexes" {
		return nil
	}
	if _, err := tx.Exec(ctx, "SET LOCAL random_page_cost=1"); err != nil {
		return err
	}
	p, err := scanTrialPlan(tx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, VERBOSE, TIMING OFF) "+vacuumQuery(trialSchema(fixture)), int32(1), int32(50000)))
	if err != nil {
		return err
	}
	n, err := vacuumScan(p, trialSchema(fixture))
	if err != nil {
		return err
	}
	out.Plan = PlanEvidence{Node: n.Type, ExaminedRows: n.Rows + n.Filtered, ReturnedRows: n.Rows, SharedBlocks: p.Node.Hit + p.Node.Read}
	if tool == "vacuum" {
		clear, err := vacuumConditions(ctx, tx, objects.Candidate, 0)
		if err != nil {
			return err
		}
		out.Vacuum = &VacuumEvidence{HeapFetches: float64(*n.HeapFetches), ScanRows: n.Rows, ConditionsClear: clear}
	}
	return nil
}

func readMemoryDiagnostic(ctx context.Context, tx pgx.Tx, state *pgx.Conn, fixture string, objects fixtureObjects, tool string, out *IndexEvidence) error {
	fn, err := expectedMemoryFunction(ctx, state, fixture)
	if err != nil {
		return err
	}
	current, err := readMemoryFunction(ctx, tx, trialSchema(fixture))
	if err != nil {
		return err
	}
	if current.WorkMem != memoryOriginal && current.WorkMem != memoryCandidate {
		return errors.New("unsupported current memory setting")
	}
	if err = validateMemoryFixture(ctx, tx, fixture, objects, fn, current.WorkMem); err != nil {
		return err
	}
	catalog, err := collectIndexCatalog(ctx, tx, fixture)
	if err != nil {
		return err
	}
	out.Existing = catalog.Existing
	if tool == "indexes" {
		return nil
	}
	if err = admitMemory(ctx, tx, objects.Schema); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "SET LOCAL work_mem='"+current.WorkMem+"'"); err != nil {
		return err
	}
	prefix := "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, TIMING OFF) "
	p, err := scanTrialPlan(tx.QueryRow(ctx, prefix+memoryInternalQuery(trialSchema(fixture))))
	if err != nil {
		return err
	}
	n, err := memorySort(p)
	if err != nil {
		return err
	}
	out.Plan = PlanEvidence{Node: n.Type, ExaminedRows: n.Rows, ReturnedRows: n.Rows, SharedBlocks: p.Node.Hit + p.Node.Read}
	if tool == "memory" {
		actual, err := scanTrialPlan(tx.QueryRow(ctx, prefix+memoryQuery(trialSchema(fixture))))
		if err != nil {
			return err
		}
		if actual.Node.Type != "Result" || actual.Node.Rows != 1 || actual.Node.Loops != 1 || actual.Node.TempWritten < 0 {
			return errors.New("invalid measured memory function call")
		}
		out.Memory = &MemoryEvidence{FunctionOID: fn.OID, TempBytes: float64(actual.Node.TempWritten) * 8192, InternalTempBytes: float64(p.Node.TempWritten) * 8192, SortNodes: 1, MaxConcurrentCalls: 3, CandidateBytes: 32 * 1024 * 1024, AggregateBytes: 96 * 1024 * 1024, CurrentWorkMem: current.WorkMem, CandidateWorkMem: memoryCandidate, IdentityVerified: true}
	}
	return validateMemoryFixture(ctx, tx, fixture, objects, fn, current.WorkMem)
}

func readCronDiagnostic(ctx context.Context, tx pgx.Tx, state *pgx.Conn, fixture string, objects fixtureObjects, tool string, now time.Time, generation CronGeneration, out *IndexEvidence) error {
	f, err := readCronFixture(ctx, state, fixture)
	if err != nil {
		return err
	}
	last := f.FirstCycle.Add(4 * time.Minute)
	deadline := last.Add(105 * time.Second)
	if f.Activated.IsZero() || f.FirstCycle.IsZero() || !f.Generation.same(generation) || !now.Before(f.Expires) || now.Before(last.Add(82*time.Second)) || !now.Before(deadline) {
		return errors.New("unready, expired or changed Cron diagnostic enrollment")
	}
	if err = validateCronFixture(ctx, tx, state, fixture, f, cronOriginal); err != nil {
		return err
	}
	for _, j := range []CronJob{f.Target, f.Witness} {
		current, e := readCronJob(ctx, tx, j.Name)
		if e != nil {
			return e
		}
		if !current.Active {
			return errors.New("enrolled Cron job is inactive")
		}
	}
	out.Existing = true // The enrolled function selects the table's existing primary key.
	if tool == "indexes" {
		return nil
	}
	plan, err := scanTrialPlan(tx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, VERBOSE, TIMING OFF) "+cronQuery(trialSchema(fixture))))
	if err != nil {
		return err
	}
	if plan.Node.Type != "Result" || plan.Node.Rows != 1 || plan.Node.Loops != 1 || len(plan.Node.Plans) != 0 || plan.Node.Filtered != 0 || plan.Node.Hit < 0 || plan.Node.Read < 0 {
		return errors.New("Cron diagnostic requires one measured enrolled function result")
	}
	out.Plan = PlanEvidence{Node: plan.Node.Type, ExaminedRows: plan.Node.Rows, ReturnedRows: plan.Node.Rows, SharedBlocks: plan.Node.Hit + plan.Node.Read}
	if tool == "plan" {
		return nil
	}
	periods, err := loadCronPeriods(ctx, state, fixture, f.FirstCycle)
	if err != nil {
		return err
	}
	if err = validateCronPeriods(periods, true); err != nil {
		return err
	}
	if !periods[0].Cycle.Equal(f.FirstCycle) || now.Before(periods[2].Quiet.End) {
		return errors.New("Cron diagnostic periods do not match enrolled lifetime")
	}
	c := periods[0].Busy.Target.Before
	var bound bool
	if err = tx.QueryRow(ctx, "SELECT current_user::regrole::oid=$1 AND (SELECT oid FROM pg_database WHERE datname=current_database())=$2", c.Role, c.Database).Scan(&bound); err != nil {
		return err
	}
	if !bound || plan.QueryID != c.Query {
		return errors.New("Cron diagnostic query identity changed")
	}
	if bound, err = currentLookupCounters(ctx, tx, EnrolledLookup{Database: c.Database, Role: c.Role, Query: c.Query}, [3]time.Time{c.Boot, c.Reset, c.Entry}); err != nil {
		return err
	}
	if !bound {
		return errors.New("Cron diagnostic counters changed")
	}
	runs, err := readMaintenanceCronRuns(ctx, tx, f.Target.ID)
	if err != nil {
		return err
	}
	if err = validateCronCycles(f.Target.ID, runs, f.FirstCycle, 3, false); err != nil {
		return err
	}
	for _, p := range periods {
		for _, b := range p.Blocks {
			matched := false
			for _, r := range runs {
				if b.JobID == r.JobID && b.RunID == r.ID && b.PID == r.PID && !b.At.Before(r.Start) && !b.At.After(r.End) && !b.BackendStart.Before(r.Start.Add(-5*time.Second)) && !b.BackendStart.After(b.At) {
					matched = true
				}
			}
			if !matched {
				return errors.New("Cron blocking observation no longer binds owned run")
			}
		}
	}
	witness, err := readMaintenanceCronRuns(ctx, tx, f.Witness.ID)
	if err != nil {
		return err
	}
	if err = validateCronWitness(f.Witness.ID, witness, last.Add(time.Minute), deadline, now, f.Generation, generation); err != nil {
		return err
	}
	out.Cron = &CronEvidence{JobID: f.Target.ID, Original: cronOriginal, Alternate: cronAlternate, Periods: periods, IdentityVerified: true, RequiredRuns: 1, CycleSeconds: 120, DeadlineSeconds: 120}
	return nil
}

func readMaintenanceCronRuns(ctx context.Context, tx pgx.Tx, job int64) ([]CronRun, error) {
	rows, err := tx.Query(ctx, `SELECT runid,jobid,COALESCE(job_pid,0),COALESCE(start_time,'epoch'::timestamptz),COALESCE(end_time,'epoch'::timestamptz),status FROM cron.job_run_details WHERE jobid=$1 ORDER BY runid LIMIT 1001`, job)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []CronRun{}
	for rows.Next() {
		var r CronRun
		if err = rows.Scan(&r.ID, &r.JobID, &r.PID, &r.Start, &r.End, &r.Status); err != nil {
			return nil, err
		}
		runs = append(runs, r)
		if len(runs) > 1000 {
			return nil, errors.New("Cron diagnostic history exceeds bounded fixture")
		}
	}
	return runs, rows.Err()
}
