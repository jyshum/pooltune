package hostedquery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

func vacuumScan(plan trialPlan, schema string) (explainNode, error) {
	p := plan.Node
	if p.Type != "Aggregate" || p.Rows != 1 || p.Loops != 1 || len(p.Plans) != 1 {
		return explainNode{}, errors.New("vacuum target needs one fully consumed aggregate")
	}
	n := p.Plans[0]
	if n.Type != "Index Only Scan" || n.Rows != 50000 || n.Loops != 1 || n.HeapFetches == nil || *n.HeapFetches < 0 || n.Relation != "candidate" || (schema != "" && n.Schema != schema) {
		return explainNode{}, fmt.Errorf("vacuum target lacks complete index-only visibility evidence: %+v", n)
	}
	return n, nil
}
func sampleVacuum(ctx context.Context, target *pgx.Conn, id string, objects fixtureObjects, index uint32) (sample VacuumSample, err error) {
	tx, err := target.Begin(ctx)
	if err != nil {
		return sample, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err = errors.Join(err, tx.Rollback(cleanup))
	}()
	if _, err := tx.Exec(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL statement_timeout='750ms'; SET LOCAL lock_timeout='250ms'; SET LOCAL transaction_timeout='10s'; SET LOCAL max_parallel_workers_per_gather=0; SET LOCAL random_page_cost=1"); err != nil {
		return sample, err
	}
	schema := trialSchema(id)
	if err := lockSampleTables(ctx, tx, schema); err != nil {
		return sample, err
	}
	if err := validateVacuumFixture(ctx, tx, id, objects, index); err != nil {
		return sample, err
	}
	query := vacuumQuery(schema)
	var sum int64
	if err := tx.QueryRow(ctx, query, int32(1), int32(50000)).Scan(&sum); err != nil || sum != 1250025000 {
		return sample, errors.Join(errors.New("vacuum aggregate result changed"), err)
	}
	const measured = "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, TIMING OFF) "
	batch := &pgx.Batch{}
	for round := range 10 {
		batch.Queue(measured+query, int32(1), int32(50000))
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
		n, err := vacuumScan(p, "")
		if err != nil {
			return sample, err
		}
		sample.Plan = n.Type
		sample.ScanRows = n.Rows
		sample.PlanReturned = n.Rows
		sample.PlanExamined = n.Rows + n.Filtered
		sample.HeapFetches += float64(*n.HeapFetches)
		sample.Blocks += float64(p.Node.Hit + p.Node.Read)
		reads = append(reads, *p.ExecutionMS)
		sample.Reads++
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
	sample.ReadMS, sample.ReadMAD = meanMAD(reads)
	sample.HealthyMS, sample.HealthyMAD = meanMAD(healthy)
	sample.WriteMS, sample.WriteMAD = meanMAD(writes)
	sample.HeapFetches /= 10
	sample.Blocks /= 10
	return sample, nil
}
