package hostedquery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type OwnedMemoryRequest struct {
	Workload OwnedRepairWorkload
	Function ProjectFunctionMemoryTarget
}

// OwnedMemoryFunctionBody is the exact private invoker fixture definition. The
// sampler checks it before calling the function. Its three slots cap app calls
// and its query has one serial sort.
func OwnedMemoryFunctionBody(schema string, lockKey uint32) string {
	return fmt.Sprintf(`DECLARE slot integer; admitted boolean := false; answer bigint;
BEGIN
 FOR slot IN 1..3 LOOP
  IF pg_catalog.pg_try_advisory_xact_lock(%d,slot) THEN admitted := true; EXIT; END IF;
 END LOOP;
 IF NOT admitted THEN RAISE EXCEPTION 'memory concurrency limit' USING ERRCODE='53300'; END IF;
 %s INTO answer;
 RETURN answer;
END`, int32(lockKey), memoryInternalQuery(schema))
}

// OwnedMemoryQuery is the ordinary top-level invocation expected from the app.
func OwnedMemoryQuery(schema string) string {
	return memoryQuery(schema) + " FROM (VALUES (1)) AS invocation(slot)"
}

// RunOwnedProjectMemory applies one measured function-local work_mem change.
// Guards.CheckCapacity must independently confirm the project has headroom for
// the function's admitted concurrent calls.
func RunOwnedProjectMemory(ctx context.Context, config *pgx.ConnConfig, req OwnedMemoryRequest, investigate IndexInvestigator, guards ProjectFunctionMemoryGuards) (report MemoryReport, err error) {
	w, f := req.Workload, req.Function
	if config == nil || !w.valid("pt_hosted_fn_") || f.Project != w.Project || f.Schema != w.Target.Schema ||
		f.DatabaseOID != w.Target.DatabaseOID || f.Function != memoryFunction || f.FunctionOID == 0 ||
		investigate == nil || guards.CheckPermission == nil || guards.CheckCapacity == nil || guards.RecordIntent == nil {
		return report, errors.New("owned memory requires a bound function workload and action guards")
	}
	if w.Decision != Investigate {
		report.Outcome, report.Reason = "no_change", "the workload did not trigger a function-memory investigation"
		return report, nil
	}
	db, err := pgx.ConnectConfig(ctx, config.Copy())
	if err != nil {
		return report, err
	}
	defer closeVacuumConnection(db)
	start := time.Now()
	baseline, err := ReadProjectFunctionMemory(ctx, db, f)
	if err != nil {
		return report, err
	}
	if baseline.WorkMem != memoryOriginal || baseline.WorkMemBytes != 4<<20 {
		report.Outcome, report.Reason = "no_change", "the enrolled function is no longer at the scoped baseline"
		return report, nil
	}
	report.Before, err = sampleOwnedMemory(ctx, db, req, baseline)
	report.BaselineMS = elapsedMS(start)
	if err != nil {
		return report, err
	}
	report.Function = MemoryIdentity{OID: f.FunctionOID, Owner: w.RoleOID, Signature: baseline.Signature, WorkMem: baseline.WorkMem, Private: true}
	report.OriginalWorkMem = baseline.WorkMem
	spill := MemoryEvidence{
		FunctionOID: f.FunctionOID, TempBytes: report.Before.TempBytes, InternalTempBytes: report.Before.InternalTempBytes, SortNodes: 1,
		MaxConcurrentCalls: 3, CandidateBytes: 32 << 20, AggregateBytes: 96 << 20,
		CurrentWorkMem: baseline.WorkMem, CandidateWorkMem: memoryCandidate, IdentityVerified: true,
	}
	report.Evidence = IndexEvidence{
		RunID: w.RunID, At: time.Now().UTC(), Decision: Investigate, TableOID: w.Target.TableOID,
		Column: 2, Label: "lookup", ExecutionMS: report.Before.ReadMS, Memory: &spill,
		Plan: PlanEvidence{Node: report.Before.Plan, ExaminedRows: report.Before.PlanExamined,
			ReturnedRows: report.Before.PlanReturned, SharedBlocks: int64(report.Before.Blocks)},
	}
	if !memoryCause(report.Evidence) {
		report.Outcome, report.Reason = "insufficient_evidence", "the matched call and internal plan did not establish an attributable spill"
		return report, nil
	}
	intent := ProjectFunctionMemoryIntent{Phase: "apply", Target: f, RunID: w.RunID, Signature: baseline.Signature,
		Fingerprint: baseline.Fingerprint, PreviousWorkMem: baseline.WorkMem, AppliedWorkMem: "32768kB",
		MaxConcurrentCalls: spill.MaxConcurrentCalls, CandidateBytes: spill.CandidateBytes, AggregateBytes: spill.AggregateBytes}
	if err := guards.CheckCapacity(ctx, intent); err != nil {
		return report, err
	}
	graph := time.Now()
	report.Proposal, err = investigate(ctx, report.Evidence)
	report.GraphMS = elapsedMS(graph)
	if err != nil {
		return report, err
	}
	if ownedRepairDeclined(report.Evidence, report.Proposal, "memory") {
		report.Outcome, report.Reason = "no_change", report.Proposal.Reason
		return report, nil
	}
	if len(report.Proposal.Columns) != 0 {
		return report, errors.New("function-memory proposal changed its enrolled column binding")
	}
	if err := ValidateDiagnosticProposal(report.Evidence, report.Proposal, time.Now()); err != nil {
		return report, err
	}
	recorded := false
	applyGuards := guards
	applyGuards.RecordIntent = func(ctx context.Context, next ProjectFunctionMemoryIntent) error {
		if err := guards.RecordIntent(ctx, next); err != nil {
			return err
		}
		recorded = true
		return nil
	}
	start = time.Now()
	change, err := ApplyProjectFunctionMemory(ctx, config, f, ProjectFunctionMemoryEvidence{
		RunID: w.RunID, At: report.Evidence.At, Baseline: baseline, Spill: spill,
	}, applyGuards)
	if err != nil && !recorded {
		return report, err
	}
	if err != nil {
		report.Outcome, report.Reason = "repair_interrupted", "PostgreSQL did not confirm the recorded function-memory action"
		return report, nil
	}
	report.MemoryMS = elapsedMS(start)
	report.AppliedWorkMem = change.After.WorkMem
	start = time.Now()
	report.After, err = sampleOwnedMemory(ctx, db, req, change.After)
	report.VerifyMS = elapsedMS(start)
	if err != nil {
		report.Outcome, report.Reason = "inconclusive", "PostgreSQL confirmed function memory but matched after measurements are unavailable"
	} else {
		report.Outcome, report.Reason = verifyMemory(report.Before, report.After)
	}
	if report.Outcome == "improved_tested_inputs" {
		return report, nil
	}
	if err := RestoreProjectFunctionMemory(ctx, config, change, guards); err != nil {
		report.Outcome, report.Reason = "repair_interrupted", "the function-memory setting needs reconciliation after the guarded restore failed"
		return report, nil
	}
	report.Restored = true
	return report, nil
}

func exactOwnedMemory(ctx context.Context, tx pgx.Tx, req OwnedMemoryRequest, baseline ProjectFunctionMemorySnapshot) (uint32, error) {
	f, w := req.Function, req.Workload
	current, err := readProjectFunctionMemory(ctx, tx, f)
	if err != nil || current != baseline {
		return 0, errors.Join(err, errors.New("enrolled function definition or setting changed"))
	}
	var oid uint32
	var definition string
	err = tx.QueryRow(ctx, `SELECT n.oid,p.prosrc FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
		WHERE p.oid=$1 AND n.nspname=$2 AND p.proname=$3 AND p.proowner=$4 AND p.pronargs=0
			AND p.prorettype='int8'::regtype AND p.provolatile='v' AND NOT p.proretset
			AND p.prolang=(SELECT oid FROM pg_language WHERE lanname='plpgsql')
			AND cardinality(p.proconfig)=4 AND 'search_path=pg_catalog'=ANY(p.proconfig) AND 'jit=off'=ANY(p.proconfig)
			AND NOT EXISTS(SELECT FROM aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) a WHERE a.grantee<>p.proowner)`,
		f.FunctionOID, f.Schema, f.Function, w.RoleOID).Scan(&oid, &definition)
	if err != nil || definition != OwnedMemoryFunctionBody(f.Schema, oid) {
		return 0, errors.Join(err, errors.New("the owned function is outside the approved private serial fixture"))
	}
	return oid, nil
}

func sampleOwnedMemory(ctx context.Context, db *pgx.Conn, req OwnedMemoryRequest, baseline ProjectFunctionMemorySnapshot) (sample MemorySample, err error) {
	w := req.Workload
	tx, err := beginOwnedSamples(ctx, db, w, false)
	if err != nil {
		return sample, err
	}
	defer func() { err = errors.Join(err, rollbackOwnedSamples(tx)) }()
	id, err := exactOwnedMemory(ctx, tx, req, baseline)
	if err != nil {
		return sample, err
	}
	if err := admitMemory(ctx, tx, id); err != nil {
		return sample, err
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL work_mem='%dkB'", baseline.WorkMemBytes/1024)); err != nil {
		return sample, err
	}
	query := OwnedMemoryQuery(w.Target.Schema)
	if err := bindOwnedMeasuredQuery(ctx, tx, w.QueryID, query); err != nil {
		return sample, err
	}
	var sum int64
	if err := tx.QueryRow(ctx, query).Scan(&sum); err != nil || sum != 5000050000 {
		return sample, errors.Join(err, errors.New("owned function result differs from the approved aggregate"))
	}
	var blockBytes int64
	if err := tx.QueryRow(ctx, "SELECT current_setting('block_size')::bigint").Scan(&blockBytes); err != nil || blockBytes <= 0 {
		return sample, errors.Join(err, errors.New("missing PostgreSQL page size"))
	}
	batch := &pgx.Batch{}
	batch.Queue(ownedMeasuredSQL + memoryInternalQuery(w.Target.Schema))
	for round := range 10 {
		batch.Queue(ownedMeasuredSQL + query)
		queueOwnedProtectedSamples(batch, w.Target.Schema, round)
	}
	res := tx.SendBatch(ctx, batch)
	defer func() { err = errors.Join(err, res.Close()) }()
	p, err := scanTrialPlan(res.QueryRow())
	if err != nil {
		return sample, err
	}
	sort, err := memorySort(p)
	if err != nil || sort.Plans[0].Schema != w.Target.Schema {
		return sample, errors.Join(err, errors.New("internal sort did not use the exact enrolled relation"))
	}
	sample.Plan, sample.SortRows, sample.PlanReturned, sample.PlanExamined = sort.Type, sort.Rows, sort.Rows, sort.Rows
	sample.InternalTempBytes = float64(p.Node.TempWritten) * float64(blockBytes)
	var reads, healthy, writes []float64
	for range 10 {
		p, err := scanTrialPlan(res.QueryRow())
		if err != nil {
			return sample, err
		}
		if p.QueryID != w.QueryID || p.Node.Type != "Result" || p.Node.Rows != 1 || p.Node.Loops != 1 || p.Node.TempWritten < 0 {
			return sample, errors.New("measured invocation did not match the discovered function query")
		}
		sample.TempBytes += float64(p.Node.TempWritten) * float64(blockBytes)
		sample.Blocks += float64(p.Node.Hit + p.Node.Read)
		reads = append(reads, *p.ExecutionMS)
		if err := readOwnedProtectedSamples(res, &healthy, &writes); err != nil {
			return sample, err
		}
	}
	if err := res.Close(); err != nil {
		return sample, err
	}
	if _, err := exactOwnedMemory(ctx, tx, req, baseline); err != nil {
		return sample, err
	}
	if err := tx.QueryRow(ctx, query).Scan(&sum); err != nil || sum != 5000050000 {
		return sample, errors.Join(err, errors.New("owned function result changed during measurement"))
	}
	sample.ResultsCorrect = true
	sample.TempBytes /= float64(len(reads))
	attachOwnedSampleTiming(&sample.AnalyzeSample, reads, healthy, writes)
	return sample, nil
}
