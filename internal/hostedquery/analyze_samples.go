package hostedquery

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	analyzeSampleCount       = 10
	analyzeInput       int32 = 43
)

type AnalyzeEvidence struct {
	SampledValueFraction *float64 `json:"sampled_lookup_fraction,omitempty"`
	EstimatedRows        int64    `json:"estimated_rows"`
	ActualRows           int64    `json:"actual_rows"`
}

type AnalyzeSample struct {
	ReadMS        float64 `json:"read_mean_ms"`
	ReadMAD       float64 `json:"read_mad_ms"`
	HealthyMS     float64 `json:"healthy_mean_ms"`
	HealthyMAD    float64 `json:"healthy_mad_ms"`
	WriteMS       float64 `json:"write_mean_ms"`
	WriteMAD      float64 `json:"write_mad_ms"`
	Reads         int     `json:"read_calls"`
	HealthyReads  int     `json:"healthy_read_calls"`
	Writes        int     `json:"write_calls"`
	Blocks        float64 `json:"blocks_per_read"`
	Plan          string  `json:"plan"`
	PlanReturned  int64   `json:"returned_rows"`
	PlanExamined  int64   `json:"examined_rows"`
	EstimatedRows int64   `json:"estimated_rows"`
	EstimateError float64 `json:"estimate_error_ratio"`
}

type AnalyzeReport struct {
	JobID      int64         `json:"job_id"`
	Fixture    string        `json:"fixture"`
	Before     AnalyzeSample `json:"before"`
	After      AnalyzeSample `json:"after"`
	Outcome    string        `json:"outcome"`
	Reason     string        `json:"reason"`
	Evidence   IndexEvidence `json:"evidence"`
	Proposal   IndexProposal `json:"proposal"`
	BaselineMS float64       `json:"baseline_wall_ms"`
	GraphMS    float64       `json:"investigation_wall_ms"`
	AnalyzeMS  float64       `json:"analyze_wall_ms"`
	VerifyMS   float64       `json:"verify_wall_ms"`
}

func seedStaleLookupStats(ctx context.Context, target *pgx.Conn, schema string) error {
	return pgx.BeginFunc(ctx, target, func(tx pgx.Tx) error {
		table := qualified(schema, "candidate")
		for _, sql := range []string{
			"SET LOCAL statement_timeout='5s'",
			"SET LOCAL lock_timeout='250ms'",
			"ALTER TABLE " + table + " SET (autovacuum_enabled=false)",
			"TRUNCATE " + table,
			fmt.Sprintf("INSERT INTO %s SELECT n, %d, repeat('p',64) FROM generate_series(1,%d) n", table, analyzeInput, fixtureRows),
			"CREATE INDEX " + pgx.Identifier{trialIndex}.Sanitize() + " ON " + table + " (lookup)",
			"ANALYZE " + table + " (lookup)",
			"TRUNCATE " + table,
			fmt.Sprintf("INSERT INTO %s SELECT n, n, repeat('p',64) FROM generate_series(1,%d) n", table, fixtureRows),
		} {
			if _, err := tx.Exec(ctx, sql); err != nil {
				return err
			}
		}
		return nil
	})
}

func analyzeCandidate(schema string) string {
	return "SELECT id FROM ONLY " + qualified(schema, "candidate") + " WHERE lookup=$1"
}

func sampleAnalyze(ctx context.Context, target *pgx.Conn, fixture string, expected fixtureObjects) (sample AnalyzeSample, err error) {
	tx, err := target.Begin(ctx)
	if err != nil {
		return sample, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err = errors.Join(err, tx.Rollback(cleanup))
	}()
	if _, err := tx.Exec(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL statement_timeout='750ms'; SET LOCAL lock_timeout='250ms'; SET LOCAL transaction_timeout='10s'; SET LOCAL max_parallel_workers_per_gather=0"); err != nil {
		return sample, err
	}
	if err := lockAnalyzeFixture(ctx, tx, fixture, expected); err != nil {
		return sample, err
	}
	schema := trialSchema(fixture)
	const measured = "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, TIMING OFF) "
	batch := &pgx.Batch{}
	candidate := analyzeCandidate(schema)
	control := "SELECT id FROM ONLY " + qualified(schema, "healthy") + " WHERE id=$1"
	batch.Queue(candidate, analyzeInput)
	batch.Queue(control, analyzeInput)
	for range analyzeSampleCount {
		batch.Queue(measured+candidate, analyzeInput)
		batch.Queue(measured+control, analyzeInput)
		batch.Queue(measured+"UPDATE ONLY "+qualified(schema, "candidate")+" SET padding=CASE padding WHEN 'a' THEN 'b' ELSE 'a' END WHERE id=$1 RETURNING id", int32(97))
	}
	res := tx.SendBatch(ctx, batch)
	defer func() { err = errors.Join(err, res.Close()) }()
	for range 2 {
		var id int32
		if err := res.QueryRow().Scan(&id); err != nil || id != analyzeInput {
			return sample, errors.Join(errors.New("unexpected ANALYZE fixture result"), err)
		}
	}
	var reads, healthy, writes []float64
	for range analyzeSampleCount {
		plan, err := scanTrialPlan(res.QueryRow())
		if err != nil {
			return sample, err
		}
		p := plan.Node
		if p.Rows != 1 || p.Loops != 1 || p.EstRows < 1 || (sample.Reads > 0 && (sample.EstimatedRows != p.EstRows || sample.Plan != p.Type)) {
			return sample, errors.New("incomparable measured lookup")
		}
		sample.Plan, sample.PlanExamined, sample.PlanReturned = p.Type, p.Rows+p.Filtered, p.Rows
		sample.EstimatedRows = p.EstRows
		sample.Blocks += float64(p.Hit + p.Read)
		reads = append(reads, *plan.ExecutionMS)
		sample.Reads++
		for _, times := range []*[]float64{&healthy, &writes} {
			check, err := scanTrialPlan(res.QueryRow())
			if err != nil {
				return sample, err
			}
			if check.Node.Rows != 1 || check.Node.Loops != 1 {
				return sample, errors.New("invalid healthy read or write guard")
			}
			*times = append(*times, *check.ExecutionMS)
		}
		sample.HealthyReads++
		sample.Writes++
	}
	sample.ReadMS, sample.ReadMAD = meanMAD(reads)
	sample.HealthyMS, sample.HealthyMAD = meanMAD(healthy)
	sample.WriteMS, sample.WriteMAD = meanMAD(writes)
	sample.Blocks /= float64(sample.Reads)
	sample.EstimateError = rowEstimateError(sample.EstimatedRows, sample.PlanReturned)
	return sample, nil
}

func lockAnalyzeFixture(ctx context.Context, tx pgx.Tx, fixture string, expected fixtureObjects) error {
	schema := trialSchema(fixture)
	if _, err := tx.Exec(ctx, "LOCK TABLE ONLY "+qualified(schema, "candidate")+", ONLY "+qualified(schema, "healthy")+" IN SHARE ROW EXCLUSIVE MODE NOWAIT"); err != nil {
		return err
	}
	if err := validateSampleFixture(ctx, tx, fixture, expected); err != nil {
		return err
	}
	index, err := inspectTrialIndex(ctx, tx, schema)
	if err != nil {
		return err
	}
	if !index.Valid || !index.Ready || !index.Matches || index.Bytes > maxTrialIndexBytes {
		return errors.New("expected the existing fixture lookup index")
	}
	return nil
}

func rowEstimateError(estimate, actual int64) float64 {
	return float64(max(estimate, actual, 1)) / float64(max(min(estimate, actual), 1))
}

func verifyAnalyze(before, after AnalyzeSample) (string, string) {
	if before.Reads != analyzeSampleCount || after.Reads != analyzeSampleCount ||
		before.HealthyReads != analyzeSampleCount || after.HealthyReads != analyzeSampleCount ||
		before.Writes != analyzeSampleCount || after.Writes != analyzeSampleCount {
		return "inconclusive", "missing matched read or write evidence"
	}
	for _, m := range []struct{ before, after, noise, relative, absolute float64 }{
		{before.ReadMS, after.ReadMS, math.Max(before.ReadMAD, after.ReadMAD), .2, 5},
		{before.HealthyMS, after.HealthyMS, math.Max(before.HealthyMAD, after.HealthyMAD), .2, 5},
		{before.WriteMS, after.WriteMS, math.Max(before.WriteMAD, after.WriteMAD), .1, 2},
	} {
		delta := m.after - m.before
		if delta >= m.absolute && delta >= m.relative*m.before && delta > 3*m.noise {
			return "regressed", "a measured read or write regressed"
		}
	}
	gain := before.ReadMS - after.ReadMS
	estimateImproved := after.EstimateError <= 3 || after.EstimateError <= before.EstimateError*.5
	if estimateImproved && gain >= 5 && gain >= .2*before.ReadMS && gain > 3*math.Max(before.ReadMAD, after.ReadMAD) {
		return "improved_tested_inputs", "row estimation and measured query latency improved with no sampled regression"
	}
	return "inconclusive", fmt.Sprintf("estimate error moved from %.1fx to %.1fx; measured result did not meet all benefit gates", before.EstimateError, after.EstimateError)
}
