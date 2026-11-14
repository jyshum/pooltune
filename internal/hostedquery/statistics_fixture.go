package hostedquery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const statisticsObject = "pooltune_lookup_mcv"
const maxStatisticsBytes = 64 << 10

type statisticsInput struct {
	Lookup  int32  `json:"lookup"`
	Padding string `json:"padding"`
	FirstID int32  `json:"-"`
	Rows    int    `json:"-"`
}

var statisticsCases = [...]statisticsInput{{1, "b", 1, 100}, {2, "a", 101, 300}}

func seedCorrelatedFixture(ctx context.Context, target *pgx.Conn, schema string) (float64, error) {
	err := pgx.BeginFunc(ctx, target, func(tx pgx.Tx) error {
		table := qualified(schema, "candidate")
		for _, sql := range []string{
			"SET LOCAL statement_timeout='5s'",
			"SET LOCAL lock_timeout='250ms'",
			"ALTER TABLE " + table + " SET (autovacuum_enabled=false)",
			"TRUNCATE " + table,
			fmt.Sprintf(`INSERT INTO %s SELECT n, CASE WHEN n<=100 OR n BETWEEN 401 AND 50000 THEN 1 ELSE 2 END,
				CASE WHEN n BETWEEN 101 AND 50000 THEN 'a' ELSE 'b' END FROM generate_series(1,%d) n ORDER BY (n * 7919) %% 100003`, table, fixtureRows),
			"CREATE INDEX " + pgx.Identifier{trialIndex}.Sanitize() + " ON " + table + " (lookup,padding)",
		} {
			if _, err := tx.Exec(ctx, sql); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	var ordinaryMS float64
	err = pgx.BeginFunc(ctx, target, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout='2s'; SET LOCAL lock_timeout='250ms'"); err != nil {
			return err
		}
		start := time.Now()
		_, err := tx.Exec(ctx, "ANALYZE "+qualified(schema, "candidate")+" (lookup,padding)")
		ordinaryMS = elapsedMS(start)
		if err != nil {
			return err
		}
		// Pin the fresh snapshot to the exact synthetic data that was analyzed.
		_, err = tx.Exec(ctx, "COMMENT ON TABLE "+qualified(schema, "candidate")+" IS 'pooltune fresh correlated fixture'")
		return err
	})
	return ordinaryMS, err
}

func statisticsLookup(schema string) string {
	return "SELECT id FROM ONLY " + qualified(schema, "candidate") + " WHERE lookup=$1 AND padding=$2"
}

func readStatisticsPlan(ctx context.Context, tx pgx.Tx, schema string, input statisticsInput) (explain, error) {
	return readLookupPlan(ctx, tx, "EXPLAIN (FORMAT JSON, VERBOSE) "+statisticsLookup(schema), input.Lookup, input.Padding)
}

func validateStatisticsFixture(ctx context.Context, tx trialReader, fixture string, expected fixtureObjects, withStats bool) error {
	if err := validateSampleFixture(ctx, tx, fixture, expected); err != nil {
		return err
	}
	var indexOK, statsOK bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_am am ON am.oid=c.relam
			WHERE i.indexrelid=to_regclass($1) AND i.indrelid=$2 AND am.amname='btree'
			AND i.indisvalid AND i.indisready AND i.indislive AND NOT i.indisunique
			AND i.indnkeyatts=2 AND i.indnatts=2 AND i.indkey::text='2 3' AND i.indoption::text='0 0'
			AND i.indclass[0]=(SELECT oid FROM pg_opclass WHERE opcname='int4_ops' AND opcmethod=am.oid AND opcnamespace='pg_catalog'::regnamespace)
			AND i.indclass[1]=(SELECT oid FROM pg_opclass WHERE opcname='text_ops' AND opcmethod=am.oid AND opcnamespace='pg_catalog'::regnamespace)
			AND i.indpred IS NULL AND i.indexprs IS NULL AND pg_relation_size(c.oid)<=$5
		),
		CASE WHEN $6::boolean THEN
			(SELECT count(*)=1 AND coalesce(bool_and(stxname=$3 AND stxnamespace=$4 AND stxowner=current_user::regrole::oid
			  AND stxrelid=$2 AND stxkeys::text='2 3' AND stxkind=ARRAY['m']::"char"[] AND stxexprs IS NULL
			  AND obj_description(oid,'pg_statistic_ext')=$8),false)
			  FROM pg_statistic_ext WHERE stxrelid IN ($2,$7) OR stxnamespace=$4)
		ELSE NOT EXISTS(SELECT FROM pg_statistic_ext WHERE stxrelid IN ($2,$7) OR stxnamespace=$4) END
	`, qualified(trialSchema(fixture), trialIndex), expected.Candidate, statisticsObject, expected.Schema, maxTrialIndexBytes, withStats, expected.Healthy, statisticsMarker(fixture)).Scan(&indexOK, &statsOK)
	if err != nil {
		return err
	}
	if !indexOK || !statsOK {
		return errors.New("unexpected composite index or extended statistics on the fixture")
	}
	return nil
}

func statisticsMarker(fixture string) string {
	return "pooltune extended statistics " + fixture
}
