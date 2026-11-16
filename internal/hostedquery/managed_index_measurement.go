package hostedquery

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
)

// ManagedIndexMeasurement is a bounded experiment using a sampled existing key.
// Inputs stay in the executor and must be reused for the after measurement.
// It does not claim that the sampled key was the original application's input.
type ManagedIndexMeasurement struct {
	EstimatedRows int64               `json:"estimated_rows"`
	HeapFetches   *int64              `json:"heap_fetches,omitempty"`
	ScanRows      int64               `json:"scan_rows"`
	Catalog       ProjectIndexCatalog `json:"-"`
	Inputs        []int32             `json:"-"`
	Evidence      PlanEvidence        `json:"plan"`
	Sample        TrialSample         `json:"sample"`
	IndexName     string              `json:"-"`
}

// MeasureManagedIndexLookup measures only the identity-bound int4 equality
// shape accepted by ReadDiscoveredIndexEvidence. It runs as the observed role
// in a bounded read-only transaction and refuses request-dependent RLS tables.
func MeasureManagedIndexLookup(ctx context.Context, db *pgx.Conn, observed supabaseproject.DiscoveredIndexEvidence, inputs []int32) (result ManagedIndexMeasurement, err error) {
	if db == nil || observed.RoleOID == 0 || observed.QueryID == 0 || len(observed.Columns) < 1 || len(observed.Columns) > 2 || (len(inputs) > 0 && len(inputs) != len(observed.Columns)) {
		return result, ErrProjectIndexTargetChanged
	}
	columns := observed.Columns
	optionalColumns := columns
	if len(columns) == 1 {
		optionalColumns = nil
	}
	target := ProjectIndexTarget{DatabaseOID: observed.DatabaseOID, TableOID: observed.TableOID, Schema: observed.Schema, Relation: observed.Relation}
	catalog, err := ResolveProjectIndex(ctx, db, target, columns[0], optionalColumns)
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	err = pgx.BeginTxFunc(ctx, db, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		result, err = MeasureManagedIndexLookupTx(ctx, tx, observed, catalog, inputs)
		return err
	})
	if err != nil {
		return ManagedIndexMeasurement{}, err
	}
	return result, nil
}

// MeasureManagedIndexLookupTx measures within an existing action transaction.
// A savepoint restores the caller's role and settings after the read. The SQL
// remains restricted to catalog-validated integer equality and plain heap tables.
func MeasureManagedIndexLookupTx(ctx context.Context, outer pgx.Tx, observed supabaseproject.DiscoveredIndexEvidence, catalog ProjectIndexCatalog, inputs []int32) (result ManagedIndexMeasurement, err error) {
	target := ProjectIndexTarget{DatabaseOID: observed.DatabaseOID, TableOID: observed.TableOID, Schema: observed.Schema, Relation: observed.Relation}
	columns := observed.Columns
	if outer == nil || catalog.Target != target || observed.QueryID == 0 || observed.RoleOID == 0 || len(columns) < 1 || len(columns) > 2 || len(catalog.Keys) != len(columns) || (len(inputs) > 0 && len(inputs) != len(columns)) {
		return result, ErrProjectIndexTargetChanged
	}
	for i, key := range catalog.Keys {
		if key.Number != columns[i] || key.Name == "" {
			return result, ErrProjectIndexTargetChanged
		}
	}
	result.Catalog = catalog
	result.Inputs = slices.Clone(inputs)
	tx, err := outer.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err = errors.Join(err, tx.Rollback(cleanup))
		if err != nil {
			result = ManagedIndexMeasurement{}
		}
	}()
	err = func() error {
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='750ms'; SET LOCAL lock_timeout='100ms'; SET LOCAL idle_in_transaction_session_timeout='2s'; SET LOCAL search_path=pg_catalog; SET LOCAL max_parallel_workers_per_gather=0; SET LOCAL enable_bitmapscan=off`); err != nil {
			return err
		}
		var role string
		if err := tx.QueryRow(ctx, `SELECT rolname FROM pg_catalog.pg_roles WHERE oid=$1`, observed.RoleOID).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{role}.Sanitize()); err != nil {
			return err
		}
		relation := qualified(target.Schema, target.Relation)
		if _, err := tx.Exec(ctx, "LOCK TABLE ONLY "+relation+" IN ACCESS SHARE MODE NOWAIT"); err != nil {
			return err
		}
		var eligible bool
		err := tx.QueryRow(ctx, `SELECT EXISTS (
    SELECT FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
    WHERE c.oid=$1 AND c.relname=$2 AND n.nspname=$3
      AND c.relkind='r' AND c.relpersistence='p' AND c.relam=(SELECT oid FROM pg_catalog.pg_am WHERE amname='heap')
      AND NOT(c.relrowsecurity OR c.relispartition OR c.relhasrules)
      AND pg_catalog.pg_total_relation_size(c.oid)<=25165824
      AND current_user::regrole::oid=$4
      AND NOT EXISTS(SELECT FROM pg_catalog.pg_inherits WHERE inhrelid=c.oid OR inhparent=c.oid)
      AND (SELECT count(*) FROM pg_catalog.pg_attribute a WHERE a.attrelid=c.oid AND a.attnum=ANY($5::smallint[])
        AND NOT a.attisdropped AND a.atttypid='pg_catalog.int4'::regtype
        AND pg_catalog.has_column_privilege(current_user,c.oid,a.attnum,'SELECT'))=cardinality($5::smallint[])
      AND NOT EXISTS(SELECT FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class ix ON ix.oid=i.indexrelid
        JOIN pg_catalog.pg_am am ON am.oid=ix.relam WHERE i.indrelid=c.oid AND (
        i.indexprs IS NOT NULL OR i.indpred IS NOT NULL OR am.amname<>'btree'
        OR EXISTS(SELECT FROM unnest(i.indclass) k(opclass) JOIN pg_catalog.pg_opclass op ON op.oid=k.opclass
          WHERE NOT op.opcdefault OR op.opcnamespace<>'pg_catalog'::regnamespace)))
  )`, target.TableOID, target.Relation, target.Schema, observed.RoleOID, columns).Scan(&eligible)
		if err != nil || !eligible {
			return errors.Join(ErrProjectIndexTargetChanged, err)
		}
		names := make([]string, len(catalog.Keys))
		predicates := make([]string, len(names))
		notNull := make([]string, len(names))
		for i, key := range catalog.Keys {
			names[i] = pgx.Identifier{key.Name}.Sanitize()
			predicates[i] = fmt.Sprintf("%s OPERATOR(pg_catalog.=) $%d", names[i], i+1)
			notNull[i] = names[i] + " IS NOT NULL"
		}
		if len(result.Inputs) == 0 {
			result.Inputs = make([]int32, len(names))
			dest := make([]any, len(names))
			for i := range dest {
				dest[i] = &result.Inputs[i]
			}
			if err := tx.QueryRow(ctx, "SELECT "+strings.Join(names, ",")+" FROM ONLY "+relation+" WHERE "+strings.Join(notNull, " AND ")+" LIMIT 1").Scan(dest...); err != nil {
				return err
			}
		}
		args := make([]any, len(result.Inputs))
		for i, value := range result.Inputs {
			args[i] = value
		}
		from := relation
		if observed.Only {
			from = "ONLY " + from
		}
		statement := "EXPLAIN (ANALYZE,BUFFERS,VERBOSE,FORMAT JSON,TIMING OFF) SELECT " + names[0] + " FROM " + from + " WHERE " + strings.Join(predicates, " AND ")
		plan, err := readLookupPlan(ctx, tx, statement, args...)
		if err != nil {
			return err
		}
		if plan.QueryID != observed.QueryID || plan.Node.Schema != target.Schema || plan.Node.Relation != target.Relation || len(plan.Node.Plans) != 0 || plan.Node.Loops != 1 {
			return ErrProjectIndexTargetChanged
		}
		switch plan.Node.Type {
		case "Seq Scan", "Index Scan", "Index Only Scan":
		default:
			return ErrProjectIndexTargetChanged
		}
		result.EstimatedRows = plan.Node.EstRows
		result.HeapFetches = plan.Node.HeapFetches
		result.ScanRows = plan.Node.Rows + plan.Node.Filtered
		result.Sample = fixtureTiming(plan)
		result.Evidence = PlanEvidence{Node: plan.Node.Type, ExaminedRows: plan.Node.Rows + plan.Node.Filtered, ReturnedRows: plan.Node.Rows, SharedBlocks: plan.Node.Read + plan.Node.Hit}
		result.IndexName = plan.Node.Index
		return nil
	}()
	return result, err
}
