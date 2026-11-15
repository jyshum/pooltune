package hostedquery

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"time"
)

const compoundDefinition = "nonunique btree candidate (lookup,padding), default ordering and operator classes"

// CreateCompoundFixture prepares a two-equality lookup. Each key alone matches
// about 25000 rows; their conjunction identifies one row for each enrolled input.
func CreateCompoundFixture(ctx context.Context, target, state *pgx.Conn, route string) (ConsumerFixture, error) {
	if err := recoverCompoundPreparations(ctx, target, state, route); err != nil {
		return ConsumerFixture{}, err
	}
	fixture, err := createConsumerFixture(ctx, target, state, route, "compound")
	if err == nil || fixture.ID == "" {
		return fixture, err
	}
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cleanupTarget, cleanupErr := pgx.ConnectConfig(cleanup, target.Config())
	if cleanupErr != nil {
		return fixture, errors.Join(err, cleanupErr)
	}
	defer closeVacuumConnection(cleanupTarget)
	cleanupState, cleanupErr := pgx.ConnectConfig(cleanup, state.Config())
	if cleanupErr != nil {
		return fixture, errors.Join(err, cleanupErr)
	}
	defer closeVacuumConnection(cleanupState)
	cleanupErr = RemoveConsumerFixture(cleanup, cleanupTarget, cleanupState, route, fixture.ID)
	return fixture, errors.Join(err, cleanupErr)
}
func compoundQuery(schema string) string {
	return "SELECT id FROM ONLY " + qualified(schema, "candidate") + " WHERE lookup=$1 AND padding=$2"
}
func indexReadArgs(input int32, compound bool) []any {
	if compound {
		return []any{input, fmt.Sprintf("%064d", input)}
	}
	return []any{input}
}
func seedCompoundFixture(ctx context.Context, target, state *pgx.Conn, id string) error {
	expected, err := expectedFixture(ctx, state, id)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, target, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout='5s'; SET LOCAL lock_timeout='250ms'"); err != nil {
			return err
		}
		if err := lockSampleTables(ctx, tx, trialSchema(id)); err != nil {
			return err
		}
		if err := validateSampleFixture(ctx, tx, id, expected); err != nil {
			return err
		}
		table := qualified(trialSchema(id), "candidate")
		if _, err := tx.Exec(ctx, "UPDATE "+table+" SET lookup=($1::int[])[(id%4)+1], padding=lpad((($1::int[])[((id+1)%4)+1])::text,64,'0')", inputIDs[:]); err != nil {
			return err
		}
		for _, input := range inputIDs {
			if _, err := tx.Exec(ctx, "UPDATE "+table+" SET lookup=id,padding=lpad(id::text,64,'0') WHERE id=$1", input); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, "ANALYZE "+table); err != nil {
			return err
		}
		// Record the intended definition before commit. A lost acknowledgement leaves
		// owned preparation state; cleanup will inspect the exact shape, never retry DDL.
		tag, err := state.Exec(ctx, "UPDATE "+journalTable+" SET index_definition=$2 WHERE id=$1 AND status='compound_preparing'", id, compoundDefinition)
		if err == nil && tag.RowsAffected() != 1 {
			return errors.New("compound preparation intent changed")
		}
		return err
	})
}
func compoundTrialEvidence(ctx context.Context, target *pgx.Conn, id string, sample TrialSample) (IndexEvidence, error) {
	e, err := collectCompoundCatalog(ctx, target, id)
	e.RunID, e.At, e.Decision = id, time.Now().UTC(), NoChange
	if sample.Breaches >= 3 {
		e.Decision = Investigate
	}
	e.Plan = PlanEvidence{Node: sample.Plan, ExaminedRows: sample.PlanExamined, ReturnedRows: sample.PlanReturned, SharedBlocks: int64(sample.Blocks)}
	return e, err
}
func collectCompoundCatalog(ctx context.Context, target trialReader, id string) (IndexEvidence, error) {
	e := IndexEvidence{Label: "lookup", Columns: []int16{2, 3}}
	err := target.QueryRow(ctx, `
 SELECT c.oid,a.attnum,EXISTS(SELECT FROM pg_index i JOIN pg_class ix ON ix.oid=i.indexrelid JOIN pg_am am ON am.oid=ix.relam
 WHERE i.indrelid=c.oid AND i.indisvalid AND i.indisready AND i.indpred IS NULL AND i.indexprs IS NULL AND am.amname='btree'
 AND i.indnkeyatts>=2 AND ((i.indkey[0]=2 AND i.indkey[1]=3) OR (i.indkey[0]=3 AND i.indkey[1]=2))
 AND NOT EXISTS(SELECT FROM generate_series(0,1) k JOIN pg_attribute at ON at.attrelid=c.oid AND at.attnum=i.indkey[k]
 WHERE i.indcollation[k]<>at.attcollation OR i.indclass[k]<>(SELECT oid FROM pg_opclass WHERE opcdefault AND opcintype=at.atttypid AND opcmethod=am.oid AND opcnamespace='pg_catalog'::regnamespace)))
 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum=2
 JOIN pg_attribute b ON b.attrelid=c.oid AND b.attnum=3
 WHERE n.nspname=$1 AND obj_description(n.oid,'pg_namespace')=$2 AND c.relname='candidate' AND c.relkind='r' AND NOT c.relispartition
 AND a.attname='lookup' AND a.atttypid='int4'::regtype AND NOT a.attisdropped AND b.attname='padding' AND b.atttypid='text'::regtype AND NOT b.attisdropped
 AND pg_relation_size(c.oid)<=25165824`, trialSchema(id), "pooltune trial "+id).Scan(&e.TableOID, &e.Column, &e.Existing)
	return e, err
}
func inspectCompoundIndex(ctx context.Context, target trialReader, schema string) (trialIndexState, error) {
	var out trialIndexState
	err := target.QueryRow(ctx, `
 SELECT true,i.indisvalid,i.indisready,c.relname='candidate' AND c.relnamespace=n.oid AND NOT i.indisunique
 AND i.indnkeyatts=2 AND i.indnatts=2 AND i.indpred IS NULL AND i.indexprs IS NULL AND am.amname='btree'
 AND i.indkey[0]=2 AND i.indkey[1]=3 AND i.indoption[0]=0 AND i.indoption[1]=0
 AND NOT EXISTS(SELECT FROM generate_series(0,1) k JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum=i.indkey[k]
 WHERE i.indcollation[k]<>a.attcollation OR i.indclass[k]<>(SELECT oid FROM pg_opclass WHERE opcdefault AND opcintype=a.atttypid AND opcmethod=am.oid AND opcnamespace='pg_catalog'::regnamespace)),pg_relation_size(ix.oid)
 FROM pg_class ix JOIN pg_namespace n ON n.oid=ix.relnamespace JOIN pg_index i ON i.indexrelid=ix.oid
 JOIN pg_class c ON c.oid=i.indrelid JOIN pg_am am ON am.oid=ix.relam WHERE n.nspname=$1 AND ix.relname=$2`, schema, trialIndex).Scan(&out.Exists, &out.Valid, &out.Ready, &out.Matches, &out.Bytes)
	if errors.Is(err, pgx.ErrNoRows) {
		var collision bool
		err = target.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", qualified(schema, trialIndex)).Scan(&collision)
		if err == nil && collision {
			return out, errors.New("compound index name occupied")
		}
	}
	return out, err
}

// Only interrupted preparation is eligible here. Ready and queued-action states
// belong to their consumer; the target lock excludes a live concurrent setup.
func recoverCompoundPreparations(ctx context.Context, target, state *pgx.Conn, route string) (err error) {
	if err := prepareJournal(ctx, state); err != nil {
		return err
	}
	if err := lockTrial(ctx, target); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, releaseTrialLock(target)) }()
	return reconcileTrialStatuses(ctx, target, state, journalTarget(target, route), []string{"compound_preparing"})
}
