package hostedquery

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type ownedStatisticsCatalog struct {
	names            []string
	bytes            int64
	covering         bool
	correlated       bool
	selectedFraction *float64
}

func readOwnedStatisticsSamples(ctx context.Context, db *pgx.Conn, request OwnedStatisticsRepairRequest) (
	samples []AnalyzeSample, info ownedStatisticsCatalog, err error,
) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, info, err
	}
	// The protected control writes deliberately exercise WAL and heap work.
	// None of their row changes become visible to the ordinary app workload.
	defer func() { err = errors.Join(err, tx.Rollback(ctx)) }()
	if err := lockOwnedStatisticsRelations(ctx, tx, request, false); err != nil {
		return nil, info, err
	}
	info, err = ownedStatisticsCatalogRead(ctx, tx, request)
	if err != nil {
		return nil, info, err
	}
	if !info.covering || (request.Operation == "statistics" && !info.correlated) {
		return nil, info, errOwnedStatisticsEvidence
	}
	samples, err = sampleOwnedStatistics(ctx, tx, request, info)
	return samples, info, err
}

func lockOwnedStatisticsRelations(ctx context.Context, tx pgx.Tx, request OwnedStatisticsRepairRequest, action bool) error {
	const settings = "SET LOCAL search_path=pg_catalog; SET LOCAL statement_timeout='750ms'; SET LOCAL lock_timeout='200ms'; " +
		"SET LOCAL max_parallel_workers_per_gather=0; SET LOCAL enable_bitmapscan=off; SET LOCAL jit=off; "
	mode := "ACCESS SHARE"
	if action {
		mode = "SHARE UPDATE EXCLUSIVE"
	}
	_, err := tx.Exec(ctx, settings+
		"LOCK TABLE ONLY "+qualified(request.Target.Schema, request.Target.Relation)+" IN "+mode+" MODE NOWAIT; "+
		"LOCK TABLE ONLY "+qualified(request.Target.Schema, "guard")+" IN ROW EXCLUSIVE MODE NOWAIT")
	return err
}

func ownedStatisticsCatalogRead(ctx context.Context, tx pgx.Tx, request OwnedStatisticsRepairRequest) (info ownedStatisticsCatalog, err error) {
	err = tx.QueryRow(ctx, `
		SELECT pg_total_relation_size(c.oid)
		FROM pg_namespace n JOIN pg_database d ON d.datname=current_database()
		JOIN pg_class c ON c.relnamespace=n.oid
		JOIN pg_class g ON g.relnamespace=n.oid
		WHERE d.oid=$1 AND n.nspname=$2 AND c.oid=$3 AND c.relname='workload' AND g.oid=$4 AND g.relname='guard'
			AND n.nspowner=$5 AND c.relowner=$5 AND g.relowner=$5
			AND current_user::regrole::oid=$5 AND session_user=current_user
			AND obj_description(n.oid,'pg_namespace')=$6
			AND c.relkind='r' AND g.relkind='r' AND c.relpersistence='p' AND g.relpersistence='p'
			AND NOT (c.relispartition OR g.relispartition OR c.relrowsecurity OR g.relrowsecurity OR c.relhasrules OR g.relhasrules OR g.relhastriggers)
			AND NOT EXISTS (SELECT FROM pg_inherits WHERE inhrelid IN (c.oid,g.oid) OR inhparent IN (c.oid,g.oid))
			AND NOT EXISTS (SELECT FROM pg_index WHERE indrelid IN (c.oid,g.oid) AND (indexprs IS NOT NULL OR indpred IS NOT NULL))
			AND NOT EXISTS (SELECT FROM pg_constraint WHERE conrelid=g.oid AND contype NOT IN ('p','n'))
			AND pg_total_relation_size(g.oid)<=1048576
			AND (SELECT count(*) FROM pg_attribute WHERE attrelid=g.oid AND attnum>0 AND NOT attisdropped)=2
			AND (SELECT count(*) FROM pg_attribute WHERE attrelid=g.oid AND attnum>0 AND NOT attisdropped
				AND attname IN ('row_id','protected_value') AND atttypid='int4'::regtype AND attgenerated='')=2
			AND EXISTS (SELECT FROM pg_index i JOIN pg_attribute a ON a.attrelid=g.oid AND a.attname='row_id'
				WHERE i.indrelid=g.oid AND i.indisprimary AND i.indisvalid AND i.indnkeyatts=1 AND i.indkey[0]=a.attnum)
			AND (SELECT count(*) FROM pg_attribute WHERE attrelid=c.oid AND attnum=ANY($7::smallint[])
				AND NOT attisdropped AND atttypid='int4'::regtype AND attgenerated='')=cardinality($7::smallint[])
	`, request.Target.DatabaseOID, request.Target.Schema, request.Target.TableOID, request.GuardTableOID, request.OwnerRoleOID,
		"Pooltune hosted statistics fixture "+request.ProjectID, request.Columns).Scan(&info.bytes)
	if errors.Is(err, pgx.ErrNoRows) {
		return info, errOwnedStatisticsEvidence
	}
	if err != nil {
		return info, err
	}
	if info.bytes <= 0 || info.bytes > projectMaintenanceMaxRelationBytes {
		return info, errOwnedStatisticsEvidence
	}
	table := request.Target
	err = tx.QueryRow(ctx, projectIndexCatalogSQL, table.DatabaseOID, table.TableOID, table.Schema, table.Relation, request.Columns).
		Scan(&info.names, &info.covering)
	if err != nil {
		return info, err
	}
	if !info.covering {
		return info, errOwnedStatisticsEvidence
	}
	if request.Operation == "analyze" {
		frequency, err := ownedMarginalFrequency(ctx, tx, table, info.names[0], request.Inputs[0][0])
		info.selectedFraction = frequency
		return info, err
	}
	info.correlated, err = ownedJointCorrelation(ctx, tx, request, info.names)
	return info, err
}

func ownedMarginalFrequency(ctx context.Context, tx pgx.Tx, target ProjectIndexTarget, column string, value int32) (*float64, error) {
	var frequency *float64
	err := tx.QueryRow(ctx, `SELECT (SELECT most_common_freqs[array_position(most_common_vals::text::int4[],$4)]::float8
		FROM pg_stats WHERE schemaname=$1 AND tablename=$2 AND attname=$3 AND NOT inherited)`,
		target.Schema, target.Relation, column, value).Scan(&frequency)
	return frequency, err
}

func ownedJointCorrelation(ctx context.Context, tx pgx.Tx, request OwnedStatisticsRepairRequest, keys []string) (bool, error) {
	var eligible bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT FROM pg_stat_all_tables WHERE relid=$1
			AND last_analyze > statement_timestamp()-interval '60 minutes' AND n_mod_since_analyze=0)
		AND NOT EXISTS (SELECT FROM pg_statistic_ext WHERE stxrelid=$1 OR
			(stxnamespace=$2::regnamespace AND stxname=$3))`,
		request.Target.TableOID, request.Target.Schema, ownedExtendedStatisticsName).Scan(&eligible); err != nil || !eligible {
		return false, err
	}
	first, second := pgx.Identifier{keys[0]}.Sanitize(), pgx.Identifier{keys[1]}.Sanitize()
	count := "SELECT count(*), count(*) FILTER (WHERE " + first + "=$1 AND " + second +
		"=$2), count(*) FILTER (WHERE " + first + "=$3 AND " + second + "=$4) FROM ONLY " + qualified(request.Target.Schema, request.Target.Relation)
	var total int64
	var actual [2]int64
	if err := tx.QueryRow(ctx, count, request.Inputs[0][0], request.Inputs[0][1], request.Inputs[1][0], request.Inputs[1][1]).
		Scan(&total, &actual[0], &actual[1]); err != nil {
		return false, err
	}
	if total < 10000 || actual[0] <= 0 || actual[1] <= 0 {
		return false, nil
	}
	for i, input := range request.Inputs {
		probability := 1.0
		for j, key := range keys {
			frequency, err := ownedMarginalFrequency(ctx, tx, request.Target, key, input[j])
			if err != nil {
				return false, err
			}
			if frequency == nil || !nonnegativeFinite(*frequency) || *frequency > 1 {
				return false, nil
			}
			probability *= *frequency
		}
		if probability*float64(total) < 10*float64(actual[i]) {
			return false, nil
		}
	}
	return true, nil
}

func ownedStatisticsQuery(request OwnedStatisticsRepairRequest, names []string) string {
	first := pgx.Identifier{names[0]}.Sanitize()
	query := "SELECT " + first + " FROM ONLY " + qualified(request.Target.Schema, request.Target.Relation) + " WHERE " + first + "=$1"
	if len(names) == 2 {
		query += " AND " + pgx.Identifier{names[1]}.Sanitize() + "=$2"
	}
	return query
}

func ownedSampleArgs(input []int32) []any {
	args := make([]any, len(input))
	for i, value := range input {
		args[i] = value
	}
	return args
}

func compareOwnedStatisticsEstimates(ctx context.Context, tx pgx.Tx, request OwnedStatisticsRepairRequest,
	info ownedStatisticsCatalog, before StatisticsSample,
) error {
	candidate := ownedStatisticsQuery(request, info.names)
	for i, input := range request.Inputs {
		plan, err := readLookupPlan(ctx, tx, "EXPLAIN (VERBOSE, FORMAT JSON) "+candidate, ownedSampleArgs(input)...)
		if err != nil {
			return err
		}
		sample := before.Samples[i]
		if plan.QueryID != request.QueryID || plan.Node.Schema != request.Target.Schema || plan.Node.Relation != request.Target.Relation ||
			plan.Node.EstRows != sample.EstimatedRows || plan.Node.Type != sample.Plan {
			return errOwnedStatisticsEvidence
		}
	}
	return nil
}

func sampleOwnedStatistics(ctx context.Context, tx pgx.Tx, request OwnedStatisticsRepairRequest, info ownedStatisticsCatalog) ([]AnalyzeSample, error) {
	candidate := ownedStatisticsQuery(request, info.names)
	guard := qualified(request.Target.Schema, "guard")
	healthy := "SELECT row_id FROM ONLY " + guard + " WHERE row_id=$1"
	write := "UPDATE ONLY " + guard + " SET protected_value=protected_value+1 WHERE row_id=$1 RETURNING row_id"
	const measured = "EXPLAIN (ANALYZE, VERBOSE, BUFFERS, FORMAT JSON, TIMING OFF) "
	samples := make([]AnalyzeSample, 0, len(request.Inputs))
	for _, input := range request.Inputs {
		args := ownedSampleArgs(input)
		estimated, err := readLookupPlan(ctx, tx, "EXPLAIN (VERBOSE, FORMAT JSON) "+candidate, args...)
		if err != nil {
			return nil, err
		}
		if estimated.QueryID != request.QueryID || estimated.Node.Schema != request.Target.Schema ||
			estimated.Node.Relation != request.Target.Relation || len(estimated.Node.Plans) != 0 {
			return nil, errOwnedStatisticsEvidence
		}
		batch := &pgx.Batch{}
		for range analyzeSampleCount {
			batch.Queue(measured+candidate, args...)
			batch.Queue(measured+healthy, int32(1))
			batch.Queue(measured+write, int32(1))
		}
		reader := tx.SendBatch(ctx, batch)
		sample, err := collectOwnedStatisticsSamples(reader, request, estimated.Node)
		if err = errors.Join(err, reader.Close()); err != nil {
			return nil, err
		}
		samples = append(samples, sample)
	}
	return samples, nil
}

func collectOwnedStatisticsSamples(reader pgx.BatchResults, request OwnedStatisticsRepairRequest, estimated explainNode) (sample AnalyzeSample, err error) {
	var candidate, healthy, write []float64
	for range analyzeSampleCount {
		plan, err := scanTrialPlan(reader.QueryRow())
		if err != nil {
			return sample, err
		}
		p := plan.Node
		if plan.QueryID != request.QueryID || p.Schema != request.Target.Schema || p.Relation != request.Target.Relation ||
			p.Loops != 1 || p.Rows <= 0 || len(p.Plans) != 0 || p.EstRows != estimated.EstRows || p.Type != estimated.Type ||
			(len(candidate) > 0 && (p.Rows != sample.PlanReturned || p.Rows+p.Filtered != sample.PlanExamined)) {
			return sample, errOwnedStatisticsEvidence
		}
		sample.Plan, sample.PlanExamined, sample.PlanReturned = p.Type, p.Rows+p.Filtered, p.Rows
		sample.EstimatedRows = p.EstRows
		sample.Blocks += float64(p.Read + p.Hit)
		candidate = append(candidate, *plan.ExecutionMS)
		for _, control := range []*[]float64{&healthy, &write} {
			guard, err := scanTrialPlan(reader.QueryRow())
			if err != nil {
				return sample, err
			}
			if guard.Node.Loops != 1 || guard.Node.Rows != 1 || guard.Node.Schema != request.Target.Schema || guard.Node.Relation != "guard" {
				return sample, fmt.Errorf("%w: protected sample did not match the guard row", errOwnedStatisticsEvidence)
			}
			*control = append(*control, *guard.ExecutionMS)
		}
	}
	sample.Reads, sample.HealthyReads, sample.Writes = len(candidate), len(healthy), len(write)
	sample.ReadMS, sample.ReadMAD = meanMAD(candidate)
	sample.HealthyMS, sample.HealthyMAD = meanMAD(healthy)
	sample.WriteMS, sample.WriteMAD = meanMAD(write)
	sample.Blocks /= float64(sample.Reads)
	sample.EstimateError = rowEstimateError(sample.EstimatedRows, sample.PlanReturned)
	return sample, nil
}
