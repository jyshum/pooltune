package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

func sampleTrial(ctx context.Context, target *pgx.Conn, id string, expected fixtureObjects) (sample TrialSample, err error) {
	tx, err := target.Begin(ctx)
	if err != nil {
		return sample, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		err = errors.Join(err, tx.Rollback(cleanup))
	}()
	err = func() error {
		for _, setting := range []string{
			"SET LOCAL statement_timeout='750ms'",
			"SET LOCAL lock_timeout='250ms'",
			"SET LOCAL transaction_timeout='10s'",
			"SET LOCAL idle_in_transaction_session_timeout='5s'",
			"SET LOCAL max_parallel_workers_per_gather=0",
		} {
			if _, err := tx.Exec(ctx, setting); err != nil {
				return err
			}
		}
		schema := trialSchema(id)
		candidate := qualified(schema, "candidate")
		healthyTable := qualified(schema, "healthy")
		if _, err := tx.Exec(ctx, "LOCK TABLE ONLY "+candidate+", ONLY "+healthyTable+" IN SHARE ROW EXCLUSIVE MODE NOWAIT"); err != nil {
			return err
		}
		if err := validateSampleFixture(ctx, tx, id, expected); err != nil {
			return err
		}
		reads := "SELECT id FROM ONLY " + candidate + " WHERE lookup=$1"
		healthy := "SELECT id FROM ONLY " + healthyTable + " WHERE id=$1"
		// Use distinct write keys that never overlap the verification reads. Changing the
		// indexed value exercises index maintenance even when PostgreSQL could use HOT.
		writes := "UPDATE ONLY " + candidate + " SET lookup=-lookup WHERE id=$1 RETURNING id"
		// A single roundtrip per iteration. Each phase uses the same known inputs and order.
		var beforeRead, beforeHealthy, beforeWrite []float64
		var cohort [len(inputIDs)][3][]float64
		for round := range trialRuns {
			batch := &pgx.Batch{}
			for _, input := range inputIDs {
				if round == 0 {
					batch.Queue(reads, input)
					batch.Queue(healthy, input)
				}
				const measured = "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, TIMING OFF) "
				batch.Queue(measured+reads, input)
				batch.Queue(measured+healthy, input)
				batch.Queue(measured+writes, fixtureRows+1-input)
			}
			batchRead, batchHealthy := 0.0, 0.0
			result := tx.SendBatch(ctx, batch)
			for position, input := range inputIDs {
				if round == 0 {
					for range 2 {
						var id int32
						if err := result.QueryRow().Scan(&id); err != nil || id != input {
							sample.Errors++
							return errors.Join(errors.New("unexpected fixture result"), err, result.Close())
						}
					}
				}
				for kind := range 3 {
					plan, err := scanTrialPlan(result.QueryRow())
					if err != nil {
						sample.Errors++
						return errors.Join(err, result.Close())
					}
					cohort[position][kind] = append(cohort[position][kind], *plan.ExecutionMS)
					switch kind {
					case 0:
						if plan.Node.Rows != 1 || plan.Node.Loops != 1 {
							return errors.Join(errors.New("target did not return a single row"), result.Close())
						}
						if sample.Reads > 0 && (sample.Plan != plan.Node.Type || sample.PlanExamined != plan.Node.Rows+plan.Node.Filtered) {
							return errors.Join(errors.New("target plan varied across enrolled inputs"), result.Close())
						}
						sample.Plan, sample.PlanReturned = plan.Node.Type, plan.Node.Rows
						sample.PlanExamined = plan.Node.Rows + plan.Node.Filtered
						sample.Blocks += float64(plan.Node.Hit + plan.Node.Read)
						sample.Reads++
						batchRead += *plan.ExecutionMS
						beforeRead = append(beforeRead, *plan.ExecutionMS)
					case 1:
						sample.HealthyReads++
						batchHealthy += *plan.ExecutionMS
						beforeHealthy = append(beforeHealthy, *plan.ExecutionMS)
					case 2:
						if plan.Node.Rows != 1 || plan.Node.Loops != 1 {
							return errors.Join(errors.New("write guard did not update a single row"), result.Close())
						}
						sample.Writes++
						beforeWrite = append(beforeWrite, *plan.ExecutionMS)
					}
				}
			}
			if err := result.Close(); err != nil {
				return err
			}
			if round >= trialRuns-5 {
				if batchRead/float64(len(inputIDs)) > objectiveMS {
					sample.Breaches++
				}
				if batchHealthy/float64(len(inputIDs)) > objectiveMS {
					sample.HealthyBreaches++
				}
			}
		}
		sample.ReadMS, sample.ReadMAD = meanMAD(beforeRead)
		sample.HealthyMS, sample.HealthyMAD = meanMAD(beforeHealthy)
		sample.WriteMS, sample.WriteMAD = meanMAD(beforeWrite)
		sample.Blocks /= float64(sample.Reads)
		for i, measurements := range cohort {
			read, readMAD := meanMAD(measurements[0])
			healthy, healthyMAD := meanMAD(measurements[1])
			write, writeMAD := meanMAD(measurements[2])
			sample.Inputs = append(sample.Inputs, TrialInput{inputIDs[i], read, readMAD, healthy, healthyMAD, write, writeMAD})
		}
		return nil
	}()
	return sample, err
}

func validateSampleFixture(ctx context.Context, tx trialReader, id string, expected fixtureObjects) error {
	actual, err := loadFixture(ctx, tx, id)
	if err != nil || actual != expected {
		return errors.Join(errors.New("sample fixture identity changed"), err)
	}
	var supported bool
	err = tx.QueryRow(ctx, `
		SELECT (SELECT count(*)=2 AND bool_and(NOT relrowsecurity AND NOT relhassubclass AND NOT relhasrules
			 AND NOT relhastriggers AND relam=(SELECT oid FROM pg_am WHERE amname='heap')
			 AND pg_relation_size(oid)<=25165824) FROM pg_class WHERE oid=ANY($1))
		  AND (SELECT count(*)=6 AND bool_and(NOT atthasdef AND attgenerated='' AND attidentity=''
			 AND CASE attnum WHEN 1 THEN attname='id' AND atttypid='int4'::regtype
			 WHEN 2 THEN attname='lookup' AND atttypid='int4'::regtype
			 WHEN 3 THEN attname='padding' AND atttypid='text'::regtype ELSE false END)
		     FROM pg_attribute WHERE attrelid=ANY($1) AND attnum>0 AND NOT attisdropped)
		  AND NOT EXISTS(SELECT FROM pg_constraint WHERE conrelid=ANY($1) AND contype NOT IN ('p','n'))
		  AND NOT EXISTS(SELECT FROM pg_index WHERE indrelid=ANY($1)
		    AND (indexprs IS NOT NULL OR indpred IS NOT NULL OR NOT indisvalid OR NOT indisready
			 OR (indexrelid<>ALL($2) AND (indexrelid IS DISTINCT FROM to_regclass($3) OR indrelid<>$4))))
	`, []uint32{actual.Candidate, actual.Healthy}, []uint32{actual.CandidatePK, actual.HealthyPK},
		qualified(trialSchema(id), trialIndex), actual.Candidate).Scan(&supported)
	if err != nil {
		return err
	}
	if !supported {
		return errors.New("sample fixture contains unsupported tables")
	}
	return nil
}

type trialPlan struct {
	explain
	ExecutionMS *float64 `json:"Execution Time"`
}

func scanTrialPlan(row pgx.Row) (trialPlan, error) {
	var raw []byte
	if err := row.Scan(&raw); err != nil {
		return trialPlan{}, err
	}
	var plans []trialPlan
	if err := json.Unmarshal(raw, &plans); err != nil {
		return trialPlan{}, err
	}
	if len(plans) != 1 || plans[0].ExecutionMS == nil ||
		!nonnegativeFinite(*plans[0].ExecutionMS) || plans[0].Node.Type == "" {
		return trialPlan{}, errors.New("incomplete measured plan")
	}
	return plans[0], nil
}

func meanMAD(values []float64) (float64, float64) {
	total := 0.0
	for _, value := range values {
		total += value
	}
	mid := median(values)
	for i := range values {
		values[i] = math.Abs(values[i] - mid)
	}
	return total / float64(len(values)), median(values)
}

func median(values []float64) float64 {
	sort.Float64s(values)
	return (values[(len(values)-1)/2] + values[len(values)/2]) / 2
}

func compareTrial(before, after TrialSample) (string, string) {
	if before.Errors+after.Errors != 0 {
		return "failed", "fixture workload reported errors"
	}
	required := len(inputIDs) * trialRuns
	for _, sample := range []TrialSample{before, after} {
		if sample.Reads != required || sample.HealthyReads != required || sample.Writes != required ||
			sample.PlanReturned != 1 || sample.Blocks <= 0 || !nonnegativeFinite(sample.ReadMS) ||
			!nonnegativeFinite(sample.HealthyMS) || !nonnegativeFinite(sample.WriteMS) ||
			!nonnegativeFinite(sample.ReadMAD) || !nonnegativeFinite(sample.HealthyMAD) ||
			!nonnegativeFinite(sample.WriteMAD) {
			return "inconclusive", "missing comparable read, write or plan evidence"
		}
	}
	if len(before.Inputs) != len(inputIDs) || len(after.Inputs) != len(inputIDs) {
		return "inconclusive", "missing enrolled input comparisons"
	}
	for i, input := range inputIDs {
		old, recent := before.Inputs[i], after.Inputs[i]
		if old.Input != input || recent.Input != input {
			return "inconclusive", "enrolled input comparisons changed"
		}
		for _, metric := range []struct{ old, recent, oldMAD, recentMAD, relative, absolute float64 }{
			{old.HealthyMS, recent.HealthyMS, old.HealthyMAD, recent.HealthyMAD, .2, 5},
			{old.WriteMS, recent.WriteMS, old.WriteMAD, recent.WriteMAD, .1, 2},
			{old.ReadMS, recent.ReadMS, old.ReadMAD, recent.ReadMAD, .2, 5},
		} {
			delta := metric.recent - metric.old
			if delta >= metric.absolute && delta >= metric.old*metric.relative &&
				delta > 3*math.Max(metric.oldMAD, metric.recentMAD) {
				return "regressed", "an enrolled fixture input crossed a read or write regression guard"
			}
		}
	}
	delta := before.ReadMS - after.ReadMS
	if delta >= 5 && delta >= .2*before.ReadMS && delta > 3*math.Max(before.ReadMAD, after.ReadMAD) &&
		after.Blocks <= .7*before.Blocks && (after.Plan == "Index Scan" || after.Plan == "Index Only Scan") {
		return "improved_tested_inputs", "target latency and logical block work improved with observed fixture guards"
	}
	return "inconclusive", "measured result did not meet the preselected benefit and plan checks"
}
