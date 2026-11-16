package hostedquery

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
)

type ManagedMemoryEvidence struct {
	FunctionOID               uint32    `json:"function_oid"`
	BaselineWorkMemBytes      int64     `json:"baseline_work_mem_bytes"`
	CandidateBytes            int64     `json:"candidate_bytes"`
	ConcurrentBoostSlots      int       `json:"concurrent_boost_slots"`
	AvailableMemoryBytes      int64     `json:"available_memory_bytes"`
	TotalMemoryBytes          int64     `json:"total_memory_bytes"`
	MemoryObservedAt          time.Time `json:"memory_observed_at"`
	OuterTempBytes            float64   `json:"outer_temp_bytes"`
	InnerTempBytes            float64   `json:"inner_temp_bytes"`
	SortRows                  int64     `json:"sort_rows"`
	IdentityVerified          bool      `json:"identity_verified"`
	FallbackPreservesBaseline bool      `json:"fallback_preserves_baseline"`
}

func (e ManagedMemoryEvidence) Valid(now time.Time) bool {
	return e.AvailableMemoryBytes-max(int64(128<<20), e.TotalMemoryBytes/4) >= 2*e.CandidateBytes*int64(e.ConcurrentBoostSlots) && e.FunctionOID != 0 && e.BaselineWorkMemBytes >= 64<<10 && e.BaselineWorkMemBytes < e.CandidateBytes && e.CandidateBytes == 32<<20 && e.ConcurrentBoostSlots >= 1 && e.ConcurrentBoostSlots <= 3 && e.AvailableMemoryBytes > 0 && e.TotalMemoryBytes >= e.AvailableMemoryBytes && !e.MemoryObservedAt.After(now) && now.Sub(e.MemoryObservedAt) <= 30*time.Second && nonnegativeFinite(e.OuterTempBytes) && nonnegativeFinite(e.InnerTempBytes) && e.OuterTempBytes >= memoryMinSpill && e.InnerTempBytes >= memoryMinSpill && e.SortRows > 0 && e.SortRows <= 100000 && e.IdentityVerified && e.FallbackPreservesBaseline
}

type ManagedMemorySample struct {
	MeanMS         float64 `json:"mean_ms"`
	MADMS          float64 `json:"mad_ms"`
	Calls          int     `json:"calls"`
	OuterTempBytes float64 `json:"outer_temp_bytes"`
	InnerTempBytes float64 `json:"inner_temp_bytes"`
	SortRows       int64   `json:"sort_rows"`
	Result         int64   `json:"-"`
}

func measureManagedMemory(ctx context.Context, db *pgx.Conn, fn ManagedMemoryFunction, role uint32, queryID int64, change *ManagedMemoryChange) (sample ManagedMemorySample, err error) {
	var roleName string
	var allowed bool
	if err := db.QueryRow(ctx, `SELECT rolname,NOT rolsuper AND NOT rolbypassrls FROM pg_roles WHERE oid=$1`, role).Scan(&roleName, &allowed); err != nil || !allowed {
		return sample, errors.Join(err, errors.New("memory measurement requires observed unprivileged app role"))
	}
	// Keep the native catalog tuple fenced on a separate owner transaction;
	// the measured invocation itself remains a read-only app-role transaction.
	lockDB, err := supabaseproject.ConnectRepair(ctx, db.Config())
	if err != nil {
		return sample, err
	}
	defer lockDB.Close(context.Background())
	lockTx, err := lockDB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return sample, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err = errors.Join(err, lockTx.Rollback(cleanup))
	}()
	if _, err = lockTx.Exec(ctx, `SET LOCAL lock_timeout='250ms'; SET LOCAL statement_timeout='750ms'; SET LOCAL search_path=pg_catalog`); err != nil {
		return sample, err
	}
	expectedGeneration := fn.Snapshot.Generation
	if change != nil {
		expectedGeneration = change.AppliedGeneration
	}
	if _, err = lockManagedMemoryFunction(ctx, lockTx, fn.Target, expectedGeneration); err != nil {
		return sample, err
	}
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return sample, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err = errors.Join(err, tx.Rollback(cleanup))
	}()
	if _, err := tx.Exec(ctx, `SET LOCAL search_path=pg_catalog; SET LOCAL statement_timeout='750ms'; SET LOCAL lock_timeout='250ms'; SET LOCAL max_parallel_workers_per_gather=0;SET LOCAL jit=off`); err != nil {
		return sample, err
	}
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{roleName}.Sanitize()); err != nil {
		return sample, err
	}
	if _, err := tx.Exec(ctx, "LOCK TABLE ONLY "+qualified(fn.Table.Schema, fn.Table.Relation)+" IN ACCESS SHARE MODE"); err != nil {
		return sample, err
	}
	// The reader pins the exact table OID and refuses RLS on every phase. No
	// generated write is used for protection or evidence.
	var valid bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.oid=$1 AND c.relname=$2 AND n.nspname=$3 AND NOT c.relrowsecurity AND NOT c.relhasrules AND c.relkind='r' AND NOT c.relispartition AND pg_total_relation_size(c.oid)<=$4)`, fn.Table.TableOID, fn.Table.Relation, fn.Table.Schema, projectIndexMaxRelationBytes).Scan(&valid); err != nil || !valid {
		return sample, errors.Join(err, errors.New("memory table identity changed"))
	}
	checkFunction := func() error {
		var matches bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pg_proc WHERE oid=$1 AND xmin::text=$2 AND NOT prosecdef)`, fn.Target.FunctionOID, expectedGeneration).Scan(&matches)
		if err != nil || !matches {
			return errors.Join(err, errors.New("measured function changed generation"))
		}
		return nil
	}
	if err := checkFunction(); err != nil {
		return sample, err
	}
	workMem := fn.Snapshot.WorkMemBytes
	if change != nil {
		var candidate, slots int
		if err := tx.QueryRow(ctx, "SELECT candidate_kb,slots FROM "+qualified(change.Intent.PolicySchema, "allowance")+"($1::oid)", fn.Target.FunctionOID).Scan(&candidate, &slots); err != nil || candidate != 32768 || slots < 1 || slots > 3 {
			return sample, errors.Join(err, errors.New("memory measurement needs an active bounded allowance"))
		}
		admitted := false
		for i := 1; i <= slots; i++ {
			var got bool
			if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1,$2)`, managedMemoryLockNamespace, i).Scan(&got); err != nil {
				return sample, err
			}
			if got {
				admitted = true
				break
			}
		}
		if !admitted {
			return sample, errors.New("all extra-memory allowances are in use")
		}
		workMem = int64(candidate) * 1024
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL work_mem='%dkB'", workMem/1024)); err != nil {
		return sample, err
	}
	call := "SELECT " + fn.Snapshot.Signature
	if err := tx.QueryRow(ctx, call).Scan(&sample.Result); err != nil {
		return sample, err
	}
	internal, err := scanTrialPlan(tx.QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,VERBOSE,FORMAT JSON,TIMING OFF) "+fn.Query))
	if err != nil {
		return sample, err
	}
	root := internal.Node
	if root.Type != "Aggregate" || root.Rows != 1 || root.Loops != 1 || len(root.Plans) != 1 {
		return sample, errors.New("function internal aggregate did not match")
	}
	sort := root.Plans[0]
	if sort.Type != "Sort" || sort.Loops != 1 || sort.Rows < 1 || sort.Rows > 100000 || len(sort.Plans) != 1 || sort.Plans[0].Type != "Seq Scan" || sort.Plans[0].Schema != fn.Table.Schema || sort.Plans[0].Relation != fn.Table.Relation || sort.Plans[0].Rows != sort.Rows || len(sort.Plans[0].Plans) != 0 {
		return sample, errors.New("function internal plan is not one bounded serial sort")
	}
	sample.SortRows = sort.Rows
	sample.InnerTempBytes = float64(root.TempWritten) * 8192
	times := make([]float64, 0, 10)
	for range 10 {
		plan, err := scanTrialPlan(tx.QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,VERBOSE,FORMAT JSON,TIMING OFF) "+call))
		if err != nil {
			return sample, err
		}
		if plan.QueryID != queryID || plan.Node.Type != "Result" || plan.Node.Rows != 1 || plan.Node.Loops != 1 {
			return sample, errors.New("measured function invocation does not match observed traffic")
		}
		times = append(times, *plan.ExecutionMS)
		sample.OuterTempBytes += float64(plan.Node.TempWritten) * 8192
	}
	var result int64
	if err := tx.QueryRow(ctx, call).Scan(&result); err != nil || result != sample.Result {
		return sample, errors.Join(err, errors.New("function result changed within measurement"))
	}
	if err := checkFunction(); err != nil {
		return sample, err
	}
	sample.Calls = len(times)
	sample.OuterTempBytes /= float64(sample.Calls)
	sample.MeanMS, sample.MADMS = meanMAD(times)
	return sample, nil
}

func managedMemoryBenefit(before, after ManagedMemorySample) bool {
	gain := before.MeanMS - after.MeanMS
	return before.Calls == 10 && after.Calls == 10 && before.Result == after.Result && before.SortRows == after.SortRows && before.OuterTempBytes >= memoryMinSpill && before.InnerTempBytes >= memoryMinSpill && after.OuterTempBytes <= before.OuterTempBytes*.5 && after.InnerTempBytes <= before.InnerTempBytes*.5 && gain >= 5 && gain >= before.MeanMS*.2 && gain > 3*math.Max(before.MADMS, after.MADMS)
}

func memoryEvidence(fn ManagedMemoryFunction, before ManagedMemorySample, h supabaseproject.MemoryHeadroom) ManagedMemoryEvidence {
	return ManagedMemoryEvidence{FunctionOID: fn.Target.FunctionOID, BaselineWorkMemBytes: fn.Snapshot.WorkMemBytes, CandidateBytes: 32 << 20, ConcurrentBoostSlots: h.MemoryAllowanceSlots(time.Now()), AvailableMemoryBytes: h.AvailableBytes, TotalMemoryBytes: h.TotalBytes, MemoryObservedAt: h.ObservedAt, OuterTempBytes: before.OuterTempBytes, InnerTempBytes: before.InnerTempBytes, SortRows: before.SortRows, IdentityVerified: true, FallbackPreservesBaseline: true}
}
