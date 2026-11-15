package hostedquery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type StatisticsEvidence struct {
	Correlated   bool                            `json:"correlated_columns"`
	Cases        []AnalyzeEvidence               `json:"cases"`
	ColumnTarget *StatisticsColumnTargetEvidence `json:"column_target,omitempty"`
}

type StatisticsColumnTargetEvidence struct {
	CurrentTarget int32 `json:"current_target"`
	NextTarget    int32 `json:"next_target"`
}

// SupportsRepair checks the measured statistics cause in either supported
// variant. The live executor must still bind and authorize its database target.
func (s *StatisticsEvidence) SupportsRepair(existingIndex bool) bool {
	if s == nil {
		return false
	}
	if v := s.ColumnTarget; v != nil {
		return existingIndex && !s.Correlated && len(s.Cases) == 1 &&
			boundedProjectStatisticsTarget(v.CurrentTarget, v.NextTarget) &&
			s.Cases[0].EstimatedRows > 0 && s.Cases[0].ActualRows/10 >= s.Cases[0].EstimatedRows
	}
	if !s.Correlated || len(s.Cases) != 2 {
		return false
	}
	for _, c := range s.Cases {
		if c.ActualRows <= 0 || c.EstimatedRows <= 0 || rowEstimateError(c.EstimatedRows, c.ActualRows) < 10 {
			return false
		}
	}
	return true
}

type StatisticsSample struct {
	Samples    []AnalyzeSample `json:"samples"`
	Correlated bool            `json:"correlated_columns"`
	Retained   bool            `json:"retained"`
	Applied    bool            `json:"applied"`
	Bytes      int64           `json:"statistics_bytes,omitempty"`
	CreateMS   float64         `json:"create_wall_ms,omitempty"`
	VerifyMS   float64         `json:"verify_wall_ms,omitempty"`
	AnalyzeMS  float64         `json:"analyze_wall_ms,omitempty"`
}

type StatisticsReport struct {
	RecoveredObject string           `json:"recovered_object,omitempty"`
	JobID           int64            `json:"job_id"`
	Fixture         string           `json:"fixture"`
	Before          StatisticsSample `json:"before"`
	After           StatisticsSample `json:"after"`
	Outcome         string           `json:"outcome"`
	Reason          string           `json:"reason"`
	Evidence        IndexEvidence    `json:"evidence"`
	Proposal        IndexProposal    `json:"proposal"`
	BaselineMS      float64          `json:"baseline_wall_ms"`
	GraphMS         float64          `json:"investigation_wall_ms"`
	ApplyMS         float64          `json:"apply_and_verify_wall_ms"`

	ColumnTarget *StatisticsColumnTargetReport `json:"column_target,omitempty"`
}

type StatisticsColumnTargetReport struct {
	PriorTarget      int32 `json:"prior_target"`
	PriorUsesDefault bool  `json:"prior_uses_default"`
	TrialTarget      int32 `json:"trial_target"`
	Restored         bool  `json:"restored"`
}

func statisticsBaseline(ctx context.Context, target *pgx.Conn, fixture string, expected fixtureObjects) (sample StatisticsSample, err error) {
	tx, err := target.Begin(ctx)
	if err != nil {
		return sample, err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		err = errors.Join(err, tx.Rollback(cleanup))
	}()
	if err := lockStatisticsFixture(ctx, tx, fixture, expected, false); err != nil {
		return sample, err
	}
	schema := trialSchema(fixture)
	table := qualified(schema, "candidate")
	sample.Correlated, err = currentCorrelation(ctx, tx, table, expected.Candidate)
	if err != nil {
		return sample, err
	}
	if !sample.Correlated {
		return sample, nil
	}
	sample.Samples, err = sampleStatistics(ctx, tx, schema)
	return sample, err
}

func currentCorrelation(ctx context.Context, tx pgx.Tx, table string, oid uint32) (bool, error) {
	var valid bool
	err := tx.QueryRow(ctx, `
		SELECT coalesce((SELECT last_analyze > statement_timestamp()-interval '10 minutes'
		                 FROM pg_stat_all_tables WHERE relid=$1),false)
		 AND obj_description($1,'pg_class')='pooltune fresh correlated fixture'
		 AND (SELECT count(*)=100000 AND count(*) FILTER (WHERE
		   lookup IS DISTINCT FROM CASE WHEN id<=100 OR id BETWEEN 401 AND 50000 THEN 1 ELSE 2 END
		   OR padding IS DISTINCT FROM CASE WHEN id BETWEEN 101 AND 50000 THEN 'a' ELSE 'b' END)=0
			  FROM ONLY `+table+`)
	`, oid).Scan(&valid)
	return valid, err
}

func lockStatisticsFixture(ctx context.Context, tx pgx.Tx, fixture string, expected fixtureObjects, withStats bool) error {
	if _, err := tx.Exec(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL statement_timeout='2s'; SET LOCAL lock_timeout='250ms'; SET LOCAL transaction_timeout='12s'; SET LOCAL max_parallel_workers_per_gather=0; SET LOCAL enable_bitmapscan=off; SET LOCAL random_page_cost=5"); err != nil {
		return err
	}
	schema := trialSchema(fixture)
	if err := lockSampleTables(ctx, tx, schema); err != nil {
		return err
	}
	return validateStatisticsFixture(ctx, tx, fixture, expected, withStats)
}

func sampleStatistics(ctx context.Context, tx pgx.Tx, schema string) (samples []AnalyzeSample, err error) {
	// A savepoint rolls back the guard writes without rolling back the statistics change.
	sampleTx, err := tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, sampleTx.Rollback(ctx)) }()
	for _, input := range statisticsCases {
		candidate := statisticsLookup(schema)
		healthy := "SELECT id FROM ONLY " + qualified(schema, "healthy") + " WHERE id=$1"
		rows, err := sampleTx.Query(ctx, candidate, input.Lookup, input.Padding)
		if err != nil {
			return nil, err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int32])
		if err != nil {
			return nil, err
		}
		if len(ids) != input.Rows {
			return nil, errors.New("unexpected statistics fixture result count")
		}
		seen := make(map[int32]bool, len(ids))
		for _, id := range ids {
			if id < input.FirstID || id >= input.FirstID+int32(input.Rows) || seen[id] {
				return nil, errors.New("unexpected statistics fixture result identity")
			}
			seen[id] = true
		}
		const explain = "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, TIMING OFF) "
		batch := &pgx.Batch{}
		for range analyzeSampleCount {
			batch.Queue(explain+candidate, input.Lookup, input.Padding)
			batch.Queue(explain+healthy, input.FirstID)
			batch.Queue(explain+"UPDATE ONLY "+qualified(schema, "candidate")+" SET padding=CASE padding WHEN 'b' THEN 'c' ELSE 'b' END WHERE id=$1 RETURNING id", int32(fixtureRows))
		}
		var sample AnalyzeSample
		result := sampleTx.SendBatch(ctx, batch)
		var reads, controls, writes []float64
		for range analyzeSampleCount {
			plan, err := scanTrialPlan(result.QueryRow())
			if err != nil {
				return nil, errors.Join(err, result.Close())
			}
			p := plan.Node
			if p.Rows != int64(input.Rows) || p.Loops != 1 || p.EstRows < 1 ||
				(sample.Reads > 0 && (sample.EstimatedRows != p.EstRows || sample.Plan != p.Type)) {
				return nil, errors.Join(errors.New("incomparable measured statistics lookup"), result.Close())
			}
			sample.Plan, sample.PlanExamined, sample.PlanReturned = p.Type, p.Rows+p.Filtered, p.Rows
			sample.EstimatedRows = p.EstRows
			sample.Blocks += float64(p.Hit + p.Read)
			sample.Reads++
			reads = append(reads, *plan.ExecutionMS)
			for _, times := range []*[]float64{&controls, &writes} {
				guard, err := scanTrialPlan(result.QueryRow())
				if err != nil {
					return nil, errors.Join(err, result.Close())
				}
				if guard.Node.Loops != 1 || guard.Node.Rows != 1 {
					return nil, errors.Join(errors.New("invalid statistics guard result"), result.Close())
				}
				*times = append(*times, *guard.ExecutionMS)
			}
			sample.HealthyReads++
			sample.Writes++
		}
		if err := result.Close(); err != nil {
			return nil, err
		}
		sample.ReadMS, sample.ReadMAD = meanMAD(reads)
		sample.HealthyMS, sample.HealthyMAD = meanMAD(controls)
		sample.WriteMS, sample.WriteMAD = meanMAD(writes)
		sample.Blocks /= float64(sample.Reads)
		sample.EstimateError = rowEstimateError(sample.EstimatedRows, sample.PlanReturned)
		samples = append(samples, sample)
	}
	return samples, nil
}

func refreshExtendedStatistics(ctx context.Context, target *pgx.Conn, fixture string, expected fixtureObjects, before StatisticsSample) (after StatisticsSample, err error) {
	if !before.Correlated || len(before.Samples) != len(statisticsCases) {
		return after, errors.New("missing statistics baseline")
	}
	err = pgx.BeginFunc(ctx, target, func(tx pgx.Tx) error {
		if err := lockStatisticsFixture(ctx, tx, fixture, expected, false); err != nil {
			return err
		}
		schema := trialSchema(fixture)
		for i, input := range statisticsCases {
			current, err := readStatisticsPlan(ctx, tx, schema, input)
			if err != nil {
				return err
			}
			if current.Node.Schema != schema || current.Node.Relation != "candidate" ||
				current.Node.Type != before.Samples[i].Plan || current.Node.EstRows != before.Samples[i].EstimatedRows {
				return nil
			}
		}
		table := qualified(schema, "candidate")
		fresh, err := currentCorrelation(ctx, tx, table, expected.Candidate)
		if err != nil || !fresh {
			return err
		}
		createStart := time.Now()
		if _, err := tx.Exec(ctx, "CREATE STATISTICS "+qualified(schema, statisticsObject)+" (mcv) ON lookup,padding FROM "+table); err != nil {
			return err
		}
		// The journal identity is canonical hex. Record it on the exact object in the creation transaction.
		if ValidateConsumerFixtureID(fixture) != nil {
			return errors.New("invalid statistics fixture ID")
		}
		if _, err := tx.Exec(ctx, "COMMENT ON STATISTICS "+qualified(schema, statisticsObject)+" IS '"+statisticsMarker(fixture)+"'"); err != nil {
			return err
		}
		after.CreateMS = elapsedMS(createStart)
		start := time.Now()
		if _, err := tx.Exec(ctx, "ANALYZE "+table+" (lookup,padding)"); err != nil {
			return err
		}
		after.AnalyzeMS = elapsedMS(start)
		if err := validateStatisticsFixture(ctx, tx, fixture, expected, true); err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `
			SELECT pg_column_size(d.stxdmcv)::bigint FROM pg_statistic_ext s
			JOIN pg_statistic_ext_data d ON d.stxoid=s.oid AND NOT d.stxdinherit
			WHERE s.stxnamespace=$1 AND s.stxrelid=$2 AND s.stxname=$3 AND d.stxdmcv IS NOT NULL
		`, expected.Schema, expected.Candidate, statisticsObject).Scan(&after.Bytes)
		if err != nil {
			return err
		}
		if after.Bytes <= 0 || after.Bytes > maxStatisticsBytes {
			return errors.New("extended statistics exceeded the fixture storage budget")
		}
		verifyStart := time.Now()
		after.Samples, err = sampleStatistics(ctx, tx, schema)
		after.VerifyMS = elapsedMS(verifyStart)
		if err != nil {
			return err
		}
		outcome, _ := verifyStatistics(before, StatisticsSample{Applied: true, Samples: after.Samples})
		after.Retained = outcome != "regressed"
		if !after.Retained {
			if _, err := tx.Exec(ctx, "DROP STATISTICS "+qualified(schema, statisticsObject)+" RESTRICT"); err != nil {
				return err
			}
		}
		after.Applied = true
		return nil
	})
	return after, err
}

func verifyStatistics(before, after StatisticsSample) (string, string) {
	if !after.Applied || len(before.Samples) != len(statisticsCases) || len(after.Samples) != len(statisticsCases) {
		return "inconclusive", "missing matched statistics comparison"
	}
	improved := true
	for i := range statisticsCases {
		outcome, reason := verifyAnalyze(before.Samples[i], after.Samples[i])
		if outcome == "regressed" {
			return outcome, fmt.Sprintf("input %d: %s", i+1, reason)
		}
		improved = improved && outcome == "improved_tested_inputs"
	}
	if improved {
		return "improved_tested_inputs", "both correlated lookups improved without a sampled regression"
	}
	return "inconclusive", "one or both inputs did not meet the measured latency and estimation gates"
}
