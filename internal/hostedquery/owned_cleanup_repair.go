package hostedquery

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const ownedMeasuredSQL = "EXPLAIN (ANALYZE, BUFFERS, VERBOSE, FORMAT JSON, TIMING OFF) "

// OwnedRepairWorkload identifies one bounded fixture and a query discovered by
// the project worker. RoleOID is the live application and fixture-owner role.
type OwnedRepairWorkload struct {
	Project, RunID string
	Target         ProjectIndexTarget
	RoleOID        uint32
	QueryID        int64
	HealthyOID     uint32
	Decision       Decision
}

type OwnedVacuumRequest struct {
	Workload  OwnedRepairWorkload
	IndexName string
	IndexOID  uint32
}

var ownedMaintenanceInstallation = regexp.MustCompile(`^[0-9a-f]{16}$`)

func (w OwnedRepairWorkload) valid(schemaPrefix string) bool {
	key, ok := strings.CutPrefix(w.Project, "supabase/"+OwnedIndexRookeryRef+"/")
	return ok && ownedMaintenanceInstallation.MatchString(key) && w.Target.Schema == schemaPrefix+key &&
		w.Target.Relation == "candidate" && w.Target.DatabaseOID > 0 && w.Target.TableOID > 0 &&
		w.RoleOID > 0 && w.QueryID != 0 && w.HealthyOID > 0 && w.HealthyOID != w.Target.TableOID &&
		w.RunID != "" && len(w.RunID) <= 64
}

// RunOwnedProjectVacuum measures and repairs one enrolled index-only scan.
// Guards.CheckCapacity must verify the absence of unsafe visibility horizons
// as well as available maintenance capacity before the action is admitted.
func RunOwnedProjectVacuum(ctx context.Context, config *pgx.ConnConfig, req OwnedVacuumRequest, investigate IndexInvestigator, guards ProjectMaintenanceGuards) (report VacuumReport, err error) {
	w := req.Workload
	if config == nil || !w.valid("pt_hosted_vc_") || req.IndexName == "" || req.IndexOID == 0 || investigate == nil ||
		guards.CheckCapacity == nil || guards.CheckPermission == nil || guards.RecordIntent == nil {
		return report, errors.New("owned cleanup requires a bound workload, index and action guards")
	}
	if w.Decision != Investigate {
		report.Outcome, report.Reason = "no_change", "the workload did not trigger a cleanup investigation"
		return report, nil
	}
	db, err := pgx.ConnectConfig(ctx, config.Copy())
	if err != nil {
		return report, err
	}
	defer closeVacuumConnection(db)
	baseline := time.Now()
	report.Before, err = sampleOwnedVacuum(ctx, db, req)
	report.BaselineMS, report.IndexOID = elapsedMS(baseline), req.IndexOID
	if err != nil {
		return report, err
	}
	action := ProjectMaintenanceRequest{Target: w.Target, RunID: w.RunID, Operation: ProjectVacuum, ObservedAt: time.Now().UTC()}
	var bytes int64
	if err := db.QueryRow(ctx, "SELECT pg_total_relation_size($1::oid)", w.Target.TableOID).Scan(&bytes); err != nil {
		return report, err
	}
	if err := guards.CheckCapacity(ctx, ProjectMaintenanceIntent{ProjectMaintenanceRequest: action, RelationBytes: bytes}); err != nil {
		return report, err
	}
	report.Evidence = IndexEvidence{
		RunID: w.RunID, At: action.ObservedAt, Decision: Investigate, TableOID: w.Target.TableOID,
		Column: 2, Label: "lookup", Existing: true, ExecutionMS: report.Before.ReadMS,
		Plan: PlanEvidence{Node: report.Before.Plan, ExaminedRows: report.Before.PlanExamined,
			ReturnedRows: report.Before.PlanReturned, SharedBlocks: int64(report.Before.Blocks)},
		Vacuum: &VacuumEvidence{HeapFetches: report.Before.HeapFetches, ScanRows: report.Before.ScanRows, ConditionsClear: true},
	}
	if !vacuumCause(report.Evidence) {
		report.Outcome, report.Reason = "insufficient_evidence", "the measured scan did not establish excess heap visibility work"
		return report, nil
	}
	graph := time.Now()
	report.Proposal, err = investigate(ctx, report.Evidence)
	report.GraphMS = elapsedMS(graph)
	if err != nil {
		return report, err
	}
	if ownedRepairDeclined(report.Evidence, report.Proposal, "vacuum") {
		report.Outcome, report.Reason = "no_change", report.Proposal.Reason
		return report, nil
	}
	if len(report.Proposal.Columns) != 0 {
		return report, errors.New("cleanup proposal changed its enrolled column binding")
	}
	if err := ValidateDiagnosticProposal(report.Evidence, report.Proposal, time.Now()); err != nil {
		return report, err
	}

	record := guards.RecordIntent
	noLongerNeeded := errors.New("the same scan did not demonstrate visibility pressure at action time")
	guards.RecordIntent = func(ctx context.Context, intent ProjectMaintenanceIntent) error {
		clear, err := recheckOwnedVacuum(ctx, db, req)
		if err != nil {
			return err
		}
		if !clear {
			return noLongerNeeded
		}
		return record(ctx, intent)
	}
	started := time.Now()
	applied, err := ApplyProjectMaintenance(ctx, config, action, guards)
	if errors.Is(err, noLongerNeeded) {
		report.Outcome, report.Reason = "no_change", noLongerNeeded.Error()
		return report, nil
	}
	if err != nil && !applied.WriteAttempted {
		return report, err
	}
	if err != nil || !applied.Confirmed {
		report.Outcome, report.Reason = "repair_interrupted", "PostgreSQL did not confirm the recorded cleanup"
		return report, nil
	}
	report.VacuumMS = elapsedMS(started)
	verify := time.Now()
	report.After, err = sampleOwnedVacuum(ctx, db, req)
	report.VerifyMS = elapsedMS(verify)
	if err != nil {
		report.Outcome, report.Reason = "inconclusive", "PostgreSQL confirmed cleanup but matched after measurements are unavailable"
		return report, nil
	}
	report.Outcome, report.Reason = verifyVacuum(report.Before, report.After)
	return report, nil
}

func ownedRepairDeclined(e IndexEvidence, p IndexProposal, repair string) bool {
	return p.Outcome == "no_change" && (p.Repair == "" || p.Repair == repair) && p.RunID == e.RunID &&
		p.TableOID == e.TableOID && p.Column == e.Column && len(p.Columns) == 0 && len(p.Reason) > 0 && len(p.Reason) <= 1000
}

func beginOwnedSamples(ctx context.Context, db *pgx.Conn, w OwnedRepairWorkload, readOnly bool) (pgx.Tx, error) {
	mode := pgx.ReadWrite
	if readOnly {
		mode = pgx.ReadOnly
	}
	tx, err := db.BeginTx(ctx, pgx.TxOptions{AccessMode: mode})
	if err != nil {
		return nil, err
	}
	settings := `SET LOCAL search_path=pg_catalog; SET LOCAL statement_timeout='750ms'; SET LOCAL lock_timeout='250ms';
		SET LOCAL transaction_timeout='10s'; SET LOCAL max_parallel_workers_per_gather=0;
		SET LOCAL random_page_cost=1; SET LOCAL jit=off`
	if _, err = tx.Exec(ctx, settings); err == nil {
		_, err = tx.Exec(ctx, "LOCK TABLE ONLY "+qualified(w.Target.Schema, w.Target.Relation)+", ONLY "+
			qualified(w.Target.Schema, "healthy")+" IN ACCESS SHARE MODE NOWAIT")
	}
	if err == nil {
		var exact bool
		marker := "Pooltune hosted vacuum fixture "
		if strings.HasPrefix(w.Target.Schema, "pt_hosted_fn_") {
			marker = "Pooltune hosted memory fixture "
		}
		err = tx.QueryRow(ctx, `
			SELECT n.nspowner=$5 AND current_user=session_user AND current_user::regrole::oid=$5
				AND obj_description(n.oid,'pg_namespace')=$6 AND d.oid=$1
				AND (SELECT count(*) FROM pg_class c JOIN pg_am a ON a.oid=c.relam AND a.amname='heap'
					WHERE c.relnamespace=n.oid AND c.relowner=$5 AND ((c.oid=$2 AND c.relname='candidate') OR (c.oid=$3 AND c.relname='healthy'))
					AND c.relkind='r' AND c.relpersistence='p' AND NOT (c.relispartition OR c.relrowsecurity OR c.relhasrules OR c.relhastriggers)
					AND NOT EXISTS(SELECT FROM pg_inherits i WHERE i.inhrelid=c.oid OR i.inhparent=c.oid)
					AND NOT EXISTS(SELECT FROM pg_attribute a WHERE a.attrelid=c.oid AND a.attgenerated<>'')
					AND NOT EXISTS(SELECT FROM pg_index i JOIN pg_class x ON x.oid=i.indexrelid JOIN pg_am m ON m.oid=x.relam
						WHERE i.indrelid=c.oid AND (m.amname<>'btree' OR i.indexprs IS NOT NULL OR i.indpred IS NOT NULL
							OR EXISTS(SELECT FROM unnest(i.indclass) op(oid) JOIN pg_opclass oc USING(oid)
								WHERE NOT oc.opcdefault OR oc.opcnamespace<>'pg_catalog'::regnamespace)))
					AND pg_total_relation_size(c.oid) <= $7)=2
				AND EXISTS(SELECT FROM pg_attribute WHERE attrelid=$2 AND attnum=2 AND attname='lookup' AND atttypid='int4'::regtype AND NOT attisdropped)
				AND EXISTS(SELECT FROM pg_attribute WHERE attrelid=$3 AND attnum=1 AND attname='id' AND atttypid='int4'::regtype AND NOT attisdropped)
				AND EXISTS(SELECT FROM pg_attribute WHERE attrelid=$3 AND attnum=2 AND attname='padding' AND atttypid='text'::regtype AND NOT attisdropped)
			FROM pg_namespace n CROSS JOIN pg_database d WHERE n.nspname=$4 AND d.datname=current_database()
		`, w.Target.DatabaseOID, w.Target.TableOID, w.HealthyOID, w.Target.Schema, w.RoleOID,
			marker+w.Project, int64(24<<20)).Scan(&exact)
		if err == nil && !exact {
			err = errors.New("owned repair sample target, owner or table policy changed")
		}
	}
	if err != nil {
		return nil, errors.Join(err, rollbackOwnedSamples(tx))
	}
	return tx, nil
}

func rollbackOwnedSamples(tx pgx.Tx) error {
	cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := tx.Rollback(cleanup)
	if errors.Is(err, pgx.ErrTxClosed) {
		return nil
	}
	return err
}

func ownedVacuumScan(ctx context.Context, tx pgx.Tx, req OwnedVacuumRequest) (trialPlan, explainNode, error) {
	w := req.Workload
	current, err := scanTrialPlan(tx.QueryRow(ctx, ownedMeasuredSQL+vacuumQuery(w.Target.Schema), int32(1), int32(50000)))
	if err != nil {
		return current, explainNode{}, err
	}
	scan, err := vacuumScan(current, w.Target.Schema)
	if err == nil && (current.QueryID != w.QueryID || scan.Index != req.IndexName) {
		err = errors.New("measured vacuum query or index differs from the enrolled workload")
	}
	return current, scan, err
}

func validateOwnedVacuumIndex(ctx context.Context, tx pgx.Tx, req OwnedVacuumRequest) error {
	var exact bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid
		WHERE i.indrelid=$1 AND i.indexrelid=$2 AND c.relname=$3 AND i.indisvalid AND i.indisready AND i.indislive
			AND i.indnkeyatts=1 AND i.indnatts=1 AND i.indkey[0]=2)`,
		req.Workload.Target.TableOID, req.IndexOID, req.IndexName).Scan(&exact)
	if err == nil && !exact {
		return errors.New("owned cleanup index identity changed")
	}
	return err
}

func recheckOwnedVacuum(ctx context.Context, db *pgx.Conn, req OwnedVacuumRequest) (clear bool, err error) {
	tx, err := beginOwnedSamples(ctx, db, req.Workload, true)
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, rollbackOwnedSamples(tx)) }()
	if err := validateOwnedVacuumIndex(ctx, tx, req); err != nil {
		return false, err
	}
	_, scan, err := ownedVacuumScan(ctx, tx, req)
	return err == nil && *scan.HeapFetches >= 1000 && float64(*scan.HeapFetches)/float64(scan.Rows) >= .5, err
}

func sampleOwnedVacuum(ctx context.Context, db *pgx.Conn, req OwnedVacuumRequest) (sample VacuumSample, err error) {
	w := req.Workload
	tx, err := beginOwnedSamples(ctx, db, w, false)
	if err != nil {
		return sample, err
	}
	defer func() { err = errors.Join(err, rollbackOwnedSamples(tx)) }()
	if err := validateOwnedVacuumIndex(ctx, tx, req); err != nil {
		return sample, err
	}
	query := vacuumQuery(w.Target.Schema)
	if err := bindOwnedMeasuredQuery(ctx, tx, w.QueryID, query, int32(1), int32(50000)); err != nil {
		return sample, err
	}
	var sum int64
	if err := tx.QueryRow(ctx, query, int32(1), int32(50000)).Scan(&sum); err != nil || sum != 1250025000 {
		return sample, errors.Join(err, errors.New("owned cleanup aggregate differs from its expected result"))
	}
	batch := &pgx.Batch{}
	for round := range 10 {
		batch.Queue(ownedMeasuredSQL+query, int32(1), int32(50000))
		queueOwnedProtectedSamples(batch, w.Target.Schema, round)
	}
	res := tx.SendBatch(ctx, batch)
	defer func() { err = errors.Join(err, res.Close()) }()
	var reads, healthy, writes []float64
	for range 10 {
		p, err := scanTrialPlan(res.QueryRow())
		if err != nil {
			return sample, err
		}
		n, err := vacuumScan(p, w.Target.Schema)
		if err != nil || p.QueryID != w.QueryID || n.Index != req.IndexName {
			return sample, errors.Join(err, errors.New("measured cleanup query or index did not match the discovered identity"))
		}
		sample.Plan, sample.ScanRows, sample.PlanReturned, sample.PlanExamined = n.Type, n.Rows, n.Rows, n.Rows+n.Filtered
		sample.HeapFetches += float64(*n.HeapFetches)
		sample.Blocks += float64(n.Hit + n.Read)
		reads = append(reads, *p.ExecutionMS)
		if err := readOwnedProtectedSamples(res, &healthy, &writes); err != nil {
			return sample, err
		}
	}
	sample.HeapFetches /= float64(len(reads))
	attachOwnedSampleTiming(&sample.AnalyzeSample, reads, healthy, writes)
	return sample, nil
}

func queueOwnedProtectedSamples(batch *pgx.Batch, schema string, round int) {
	table := qualified(schema, "healthy")
	batch.Queue(ownedMeasuredSQL+"SELECT id FROM ONLY "+table+" WHERE id=$1", int32(1))
	batch.Queue(ownedMeasuredSQL+"UPDATE ONLY "+table+" SET padding=CASE padding WHEN 'a' THEN 'b' ELSE 'a' END WHERE id=$1 RETURNING id", int32(2+round))
}

func bindOwnedMeasuredQuery(ctx context.Context, tx pgx.Tx, queryID int64, query string, args ...any) error {
	plan, err := readLookupPlan(ctx, tx, "EXPLAIN (VERBOSE, FORMAT JSON) "+query, args...)
	if err != nil {
		return err
	}
	if plan.QueryID != queryID {
		return errors.New("measured query does not match the discovered identity")
	}
	return nil
}

func readOwnedProtectedSamples(results pgx.BatchResults, healthy, writes *[]float64) error {
	for _, times := range []*[]float64{healthy, writes} {
		p, err := scanTrialPlan(results.QueryRow())
		if err != nil {
			return err
		}
		if p.Node.Rows != 1 || p.Node.Loops != 1 {
			return errors.New("owned repair did not measure a matched protected read and write")
		}
		*times = append(*times, *p.ExecutionMS)
	}
	return nil
}

func attachOwnedSampleTiming(sample *AnalyzeSample, reads, healthy, writes []float64) {
	sample.Reads, sample.HealthyReads, sample.Writes = len(reads), len(healthy), len(writes)
	sample.ReadMS, sample.ReadMAD = meanMAD(reads)
	sample.HealthyMS, sample.HealthyMAD = meanMAD(healthy)
	sample.WriteMS, sample.WriteMAD = meanMAD(writes)
	sample.Blocks /= float64(len(reads))
}
