package hostedquery

import (
	"context"
	"errors"
	"fmt"
	"pooltune/internal/supabaseproject"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	projectIndexMaxRelationBytes = 24 << 20
	projectIndexMaxIndexBytes    = 8 << 20
	projectIndexLockNamespace    = 1514145797
)

type ProjectIndexActionIntent struct {
	Target            ProjectIndexTarget
	RunID             string
	IndexName         string
	Keys              []ProjectIndexKey
	RelationBytes     int64
	AppliedIndexOID   uint32 `json:",omitempty"`
	RollbackRequested bool   `json:",omitempty"`
}

type ProjectIndexActionGuards struct {
	// CheckPermission checks the owner grant for this exact action. The caller
	// must coordinate schema DDL through the action's return. A table lock
	// prevents table replacement but cannot hold a namespace name in place.
	CheckPermission func(context.Context, ProjectIndexActionIntent) error
	CheckCapacity   func(context.Context, ProjectIndexActionIntent) error
	// RecordIntent returns success only after the exact action is durable.
	RecordIntent func(context.Context, ProjectIndexActionIntent) error
}

type ProjectIndexActionResult struct {
	AlreadyCovered bool
	WriteAttempted bool
	IndexOID       uint32
	IndexBytes     int64
	IndexVersion   string
}

// ApplyProjectIndex builds one plain nonunique B-tree for a qualifying lookup.
// The caller supplies an authorized native route with a direct table-owner
// login, coordinates schema changes, and checks project capacity. An error
// after WriteAttempted requires journal reconciliation before another action.
func ApplyProjectIndex(ctx context.Context, config *pgx.ConnConfig, target ProjectIndexTarget, evidence IndexEvidence, proposal IndexProposal, guards ProjectIndexActionGuards) (result ProjectIndexActionResult, err error) {
	if config == nil || guards.CheckPermission == nil || guards.CheckCapacity == nil || guards.RecordIntent == nil || target.TableOID != evidence.TableOID {
		return result, errors.New("project index requires exact target, permissions, capacity and durable intent")
	}
	if err := qualifyIndex(evidence, proposal, time.Now()); err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	db, err := supabaseproject.ConnectRepair(ctx, config)
	if err != nil {
		return result, err
	}
	defer closeVacuumConnection(db)
	var backendPID int32
	if err := db.QueryRow(ctx, "SELECT pg_catalog.pg_backend_pid()").Scan(&backendPID); err != nil {
		return result, err
	}
	if backendPID != int32(db.PgConn().PID()) {
		return result, errors.New("project index requires a native PostgreSQL session")
	}
	if _, err := db.Exec(ctx, `SET search_path = pg_catalog; SET lock_timeout = '250ms'; SET statement_timeout = '5s';
		SET maintenance_work_mem = '16MB'; SET max_parallel_maintenance_workers = 0`); err != nil {
		return result, err
	}
	var locked bool
	if err := db.QueryRow(ctx, "SELECT pg_catalog.pg_try_advisory_lock($1, $2)", projectIndexLockNamespace, int32(target.TableOID)).Scan(&locked); err != nil {
		return result, err
	}
	if !locked {
		return result, errors.New("another project index action is active for this table")
	}
	catalog, err := ResolveProjectIndex(ctx, db, target, evidence.Column, evidence.Columns)
	if err != nil {
		return result, err
	}
	name := fmt.Sprintf("pooltune_ix_%d", target.TableOID)
	keyNames := make([]string, len(catalog.Keys))
	keys := make([]int16, len(catalog.Keys))
	for i, key := range catalog.Keys {
		name += fmt.Sprintf("_%d", key.Number)
		keyNames[i] = pgx.Identifier{key.Name}.Sanitize()
		keys[i] = key.Number
	}
	intent := ProjectIndexActionIntent{Target: target, RunID: evidence.RunID, IndexName: name, Keys: catalog.Keys}
	check := func(guard func(context.Context, ProjectIndexActionIntent) error) error {
		copy := intent
		copy.Keys = slices.Clone(intent.Keys)
		return guard(ctx, copy)
	}
	if err := check(guards.CheckPermission); err != nil {
		return result, err
	}
	if catalog.Covering {
		result.AlreadyCovered = true
		return result, nil
	}
	// Hold ACCESS SHARE on a companion session. CIC cannot run in a transaction
	// block and the lock is compatible with both the build and ordinary DML.
	pin, err := supabaseproject.ConnectRepair(ctx, config)
	if err != nil {
		return result, err
	}
	defer closeVacuumConnection(pin)
	tx, err := pin.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	if _, err := tx.Exec(ctx, `SET LOCAL search_path=pg_catalog; SET LOCAL statement_timeout='750ms'; SET LOCAL idle_in_transaction_session_timeout='12s'`); err != nil {
		return result, err
	}
	relation := qualified(target.Schema, target.Relation)
	if _, err := tx.Exec(ctx, "LOCK TABLE ONLY "+relation+" IN ACCESS SHARE MODE NOWAIT"); err != nil {
		return result, err
	}
	// Keep the pin transaction free of query snapshots. CIC waits for older
	// snapshots after validating, including snapshots held by an idle reader.
	pinPID := int32(pin.PgConn().PID())
	intent.RelationBytes, err = projectIndexPreflight(ctx, db, intent, keys, pinPID)
	if err != nil {
		return result, err
	}
	if err := check(guards.CheckCapacity); err != nil {
		return result, err
	}
	if err := check(guards.RecordIntent); err != nil {
		return result, err
	}
	current, err := ResolveProjectIndex(ctx, db, target, evidence.Column, evidence.Columns)
	if err != nil {
		return result, err
	}
	if !slices.Equal(catalog.Keys, current.Keys) {
		return result, ErrProjectIndexTargetChanged
	}
	if current.Covering {
		result.AlreadyCovered = true
		return result, nil
	}
	if _, err := projectIndexPreflight(ctx, db, intent, keys, pinPID); err != nil {
		return result, err
	}
	if err := check(guards.CheckPermission); err != nil {
		return result, err
	}
	if err := qualifyIndex(evidence, proposal, time.Now()); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	result.WriteAttempted = true
	if _, err = db.Exec(ctx, "CREATE INDEX CONCURRENTLY "+pgx.Identifier{name}.Sanitize()+" ON "+relation+" USING btree ("+strings.Join(keyNames, ", ")+")"); err != nil {
		return result, err
	}
	var matches bool
	err = db.QueryRow(ctx, projectIndexReadbackSQL, target.TableOID, target.Schema, name, keys).Scan(&result.IndexOID, &result.IndexBytes, &matches)
	if err != nil {
		return result, err
	}
	if !matches || result.IndexBytes <= 0 || result.IndexBytes > projectIndexMaxIndexBytes {
		return result, errors.New("project index readback failed exact definition or size checks")
	}
	if managedIndexRunID.MatchString(evidence.RunID) {
		err = db.QueryRow(ctx, managedIndexVersionSQL, result.IndexOID).Scan(&result.IndexVersion)
	}
	return result, err
}

func projectIndexPreflight(ctx context.Context, db *pgx.Conn, intent ProjectIndexActionIntent, keys []int16, pinPID int32) (int64, error) {
	var relationBytes int64
	var occupied bool
	err := db.QueryRow(ctx, supabaseproject.RepairQuery(ctx, db, `
		SELECT pg_catalog.pg_total_relation_size(c.oid), EXISTS (SELECT FROM pg_catalog.pg_class x WHERE x.relnamespace=n.oid AND x.relname::text=$4)
		FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
		WHERE c.oid=$1 AND n.nspname::text=$2 AND c.relname::text=$3
		  AND NOT pg_catalog.pg_is_in_recovery() AND current_user=session_user
		  AND c.relowner=(SELECT oid FROM pg_catalog.pg_roles WHERE rolname=current_user)
		  AND EXISTS (SELECT FROM pg_catalog.pg_locks l JOIN pg_catalog.pg_stat_activity a ON a.pid=l.pid
		       WHERE l.pid=$6 AND a.backend_xmin IS NULL AND l.database=$7 AND l.relation=c.oid AND l.mode='AccessShareLock' AND l.granted)
		  AND (SELECT count(*) FROM pg_catalog.pg_attribute a WHERE a.attrelid=c.oid AND a.attnum=ANY($5::smallint[]) AND NOT a.attisdropped
		       AND a.atttypid IN ('int2'::regtype,'int4'::regtype,'int8'::regtype,'text'::regtype,'varchar'::regtype,'date'::regtype,
			                  'timestamp'::regtype,'timestamptz'::regtype,'uuid'::regtype,'bool'::regtype))=pg_catalog.cardinality($5::smallint[])
	`), intent.Target.TableOID, intent.Target.Schema, intent.Target.Relation, intent.IndexName, keys, pinPID, intent.Target.DatabaseOID).Scan(&relationBytes, &occupied)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errors.New("project index requires the exact table owner and lock on a writable target with supported keys")
	}
	if err != nil {
		return 0, err
	}
	if occupied || relationBytes <= 0 || relationBytes > projectIndexMaxRelationBytes {
		return 0, errors.New("project index name is occupied or relation exceeds the action size bounds")
	}
	return relationBytes, nil
}

const projectIndexReadbackSQL = `
	SELECT ix.oid, pg_catalog.pg_relation_size(ix.oid),
		i.indisvalid AND i.indisready AND i.indislive AND NOT i.indisunique AND i.indpred IS NULL AND i.indexprs IS NULL
		AND am.amname='btree' AND i.indnkeyatts=pg_catalog.cardinality($4::smallint[]) AND i.indnatts=i.indnkeyatts
		AND NOT EXISTS (
			SELECT FROM pg_catalog.unnest($4::smallint[]) WITH ORDINALITY k (number, position)
			JOIN pg_catalog.pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=k.number
			LEFT JOIN pg_catalog.pg_opclass opc ON opc.oid=i.indclass[k.position - 1]
			WHERE i.indkey[k.position - 1] <> k.number OR i.indcollation[k.position - 1] <> a.attcollation
			   OR i.indoption[k.position - 1] <> 0 OR opc.opcdefault IS DISTINCT FROM true
		)
	FROM pg_catalog.pg_class ix
	JOIN pg_catalog.pg_namespace n ON n.oid=ix.relnamespace
	JOIN pg_catalog.pg_index i ON i.indexrelid=ix.oid
	JOIN pg_catalog.pg_am am ON am.oid=ix.relam
	WHERE i.indrelid=$1 AND n.nspname::text=$2 AND ix.relname::text=$3
`
