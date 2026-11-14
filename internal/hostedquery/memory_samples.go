package hostedquery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

func memorySort(plan trialPlan) (explainNode, error) {
	p := plan.Node
	if p.Type != "Aggregate" || p.Rows != 1 || p.Loops != 1 || len(p.Plans) != 1 {
		return explainNode{}, errors.New("memory target requires a fully consumed aggregate")
	}
	n := p.Plans[0]
	if n.Type != "Sort" || n.Rows != 100000 || n.Loops != 1 || len(n.Plans) != 1 || n.Plans[0].Type != "Seq Scan" || n.Plans[0].Relation != "candidate" || n.Plans[0].Rows != 100000 || n.Plans[0].Loops != 1 || len(n.Plans[0].Plans) != 0 || n.TempWritten < 0 || (n.SortSpace != "Disk" && n.SortSpace != "Memory") {
		return explainNode{}, fmt.Errorf("internal memory plan is not one fully consumed serial sort: %+v", n)
	}
	return n, nil
}
func sampleMemory(ctx context.Context, target *pgx.Conn, id string, objects fixtureObjects, fn MemoryIdentity, workMem string) (sample MemorySample, err error) {
	if workMem != memoryOriginal && workMem != memoryCandidate {
		return sample, errors.New("unmatched memory diagnostic setting")
	}
	tx, err := target.Begin(ctx)
	if err != nil {
		return sample, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err = errors.Join(err, tx.Rollback(cleanup))
	}()
	if _, err := tx.Exec(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL statement_timeout='750ms'; SET LOCAL lock_timeout='250ms'; SET LOCAL transaction_timeout='10s'; SET LOCAL max_parallel_workers_per_gather=0; SET LOCAL jit=off; SET LOCAL work_mem='"+workMem+"'"); err != nil {
		return sample, err
	}
	schema := trialSchema(id)
	if err := lockSampleTables(ctx, tx, schema); err != nil {
		return sample, err
	}
	if err := validateMemoryFixture(ctx, tx, id, objects, fn, workMem); err != nil {
		return sample, err
	}
	if err := admitMemory(ctx, tx, objects.Schema); err != nil {
		return sample, err
	}
	var sum int64
	if err := tx.QueryRow(ctx, memoryQuery(schema)).Scan(&sum); err != nil || sum != 5000050000 {
		return sample, errors.Join(errors.New("memory result changed"), err)
	}
	const measured = "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, TIMING OFF) "
	batch := &pgx.Batch{}
	for round := range 10 {
		batch.Queue(measured + memoryQuery(schema))
		batch.Queue(measured + memoryInternalQuery(schema))
		batch.Queue(measured+"SELECT id FROM ONLY "+qualified(schema, "healthy")+" WHERE id=$1", int32(43))
		batch.Queue(measured+"UPDATE ONLY "+qualified(schema, "healthy")+" SET padding=CASE padding WHEN 'a' THEN 'b' ELSE 'a' END WHERE id=$1 RETURNING id", int32(80001+round))
	}
	res := tx.SendBatch(ctx, batch)
	defer func() { err = errors.Join(err, res.Close()) }()
	var reads, healthy, writes []float64
	for range 10 {
		p, err := scanTrialPlan(res.QueryRow())
		if err != nil {
			return sample, err
		}
		if p.Node.Type != "Result" || p.Node.Rows != 1 || p.Node.Loops != 1 || p.Node.TempWritten < 0 {
			return sample, errors.New("invalid measured memory function call")
		}
		sample.TempBytes += float64(p.Node.TempWritten) * 8192
		sample.Blocks += float64(p.Node.Hit + p.Node.Read)
		reads = append(reads, *p.ExecutionMS)
		sample.Reads++
		p, err = scanTrialPlan(res.QueryRow())
		if err != nil {
			return sample, err
		}
		n, err := memorySort(p)
		if err != nil {
			return sample, err
		}
		sample.Plan = n.Type
		sample.PlanReturned = n.Rows
		sample.PlanExamined = n.Rows
		sample.SortRows = n.Rows
		sample.InternalTempBytes += float64(p.Node.TempWritten) * 8192
		for _, times := range []*[]float64{&healthy, &writes} {
			p, err := scanTrialPlan(res.QueryRow())
			if err != nil {
				return sample, err
			}
			if p.Node.Rows != 1 || p.Node.Loops != 1 {
				return sample, errors.New("missing protected read/write")
			}
			*times = append(*times, *p.ExecutionMS)
		}
		sample.HealthyReads++
		sample.Writes++
	}
	if err := res.Close(); err != nil {
		return sample, err
	}
	if err := tx.QueryRow(ctx, memoryQuery(schema)).Scan(&sum); err != nil || sum != 5000050000 {
		return sample, errors.Join(errors.New("memory result changed"), err)
	}
	if err := validateMemoryFixture(ctx, tx, id, objects, fn, workMem); err != nil {
		return sample, err
	}
	sample.ResultsCorrect = true
	sample.TempBytes /= 10
	sample.InternalTempBytes /= 10
	sample.Blocks /= 10
	sample.ReadMS, sample.ReadMAD = meanMAD(reads)
	sample.HealthyMS, sample.HealthyMAD = meanMAD(healthy)
	sample.WriteMS, sample.WriteMAD = meanMAD(writes)
	return sample, nil
}
