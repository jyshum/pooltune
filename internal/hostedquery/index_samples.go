package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"

	"github.com/jackc/pgx/v5"
)

func sampleTrial(ctx context.Context, target *pgx.Conn, schema string) (sample TrialSample, err error) {
	err = pgx.BeginFunc(ctx, target, func(tx pgx.Tx) error {
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
		candidate := qualified(schema, "candidate")
		reads := "SELECT id FROM " + candidate + " WHERE lookup=$1"
		healthy := "SELECT id FROM " + qualified(schema, "healthy") + " WHERE id=$1"
		writes := "UPDATE " + candidate + " SET lookup=lookup WHERE id=$1"
		// A single roundtrip per iteration. Each phase uses the same known inputs and order.
		var beforeRead, beforeHealthy, beforeWrite []float64
		for round := range trialRuns {
			batch := &pgx.Batch{}
			for _, input := range inputIDs {
				batch.Queue(reads, input)
				batch.Queue(healthy, input)
				for _, sql := range []string{reads, healthy, writes} {
					batch.Queue("EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, TIMING OFF) "+sql, input)
				}
			}
			batchRead, batchHealthy := 0.0, 0.0
			result := tx.SendBatch(ctx, batch)
			for _, input := range inputIDs {
				for range 2 {
					var id int32
					if err := result.QueryRow().Scan(&id); err != nil || id != input {
						sample.Errors++
						return errors.Join(errors.New("unexpected fixture result"), err, result.Close())
					}
				}
				for kind := range 3 {
					plan, err := scanTrialPlan(result.QueryRow())
					if err != nil {
						sample.Errors++
						return errors.Join(err, result.Close())
					}
					switch kind {
					case 0:
						if plan.Node.Rows != 1 || plan.Node.Loops != 1 {
							return errors.Join(errors.New("target did not return a single row"), result.Close())
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
		return nil
	})
	return sample, err
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
	for _, metric := range []struct{ old, recent, oldMAD, recentMAD, relative, absolute float64 }{
		{before.HealthyMS, after.HealthyMS, before.HealthyMAD, after.HealthyMAD, .2, 5},
		{before.WriteMS, after.WriteMS, before.WriteMAD, after.WriteMAD, .1, 2},
		{before.ReadMS, after.ReadMS, before.ReadMAD, after.ReadMAD, .2, 5},
	} {
		delta := metric.recent - metric.old
		if delta >= metric.absolute && delta >= metric.old*metric.relative &&
			delta > 3*math.Max(metric.oldMAD, metric.recentMAD) {
			return "regressed", "measured fixture read or write crossed a regression guard"
		}
	}
	delta := before.ReadMS - after.ReadMS
	if delta >= 5 && delta >= .2*before.ReadMS && delta > 3*math.Max(before.ReadMAD, after.ReadMAD) &&
		after.Blocks <= .7*before.Blocks && (after.Plan == "Index Scan" || after.Plan == "Index Only Scan") {
		return "improved_tested_inputs", "target latency and logical block work improved with observed fixture guards"
	}
	return "inconclusive", "measured result did not meet the preselected benefit and plan checks"
}
