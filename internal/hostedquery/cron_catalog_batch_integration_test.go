//go:build integration

package hostedquery

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Five serial metadata exchanges used to consume five cross-region round trips.
// The same real catalog checks must fit one exchange before any action starts.
func TestCronFixtureValidationBatchesMetadataWithinLatencyBudget(t *testing.T) {
	ctx, cfg, _, state, f, cf := cronCatalogAdapter(t)
	for _, transaction := range []bool{false, true} {
		name := "connection"
		if transaction {
			name = "transaction"
		}
		t.Run(name, func(t *testing.T) {
			trace := &cronCatalogDelay{}
			traced := cfg.Copy()
			traced.Tracer = trace
			db, err := pgx.ConnectConfig(ctx, traced)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close(ctx)
			var reader cronCatalogReader = db
			if transaction {
				tx, err := db.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(ctx)
				reader = tx
			}
			trace.queries, trace.batches = 0, 0
			bounded, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
			defer cancel()
			start := time.Now()
			err = validateCronFixture(bounded, reader, state, f.ID, cf, cronOriginal)
			t.Logf("validation=%s standalone=%d batches=%d", time.Since(start), trace.queries, trace.batches)
			if err != nil {
				t.Fatalf("catalog validation exceeded bounded network budget: %v", err)
			}
			if trace.queries != 0 || trace.batches != 1 {
				t.Fatalf("metadata was not one batch: standalone=%d batches=%d", trace.queries, trace.batches)
			}
		})
	}
}

// Scanning or validation failure midway through a batch must preserve refusal
// and drain/close all pending rows so the transaction remains usable.
func TestCronBatchedCatalogPreservesRefusalAndClosesPendingRows(t *testing.T) {
	ctx, _, target, state, f, cf := cronCatalogAdapter(t)
	for _, tc := range []struct{ name, sql string }{
		{"namespace grant", "GRANT USAGE ON SCHEMA " + pgx.Identifier{f.Schema}.Sanitize() + " TO PUBLIC"},
		{"function permission", "GRANT EXECUTE ON FUNCTION " + qualified(f.Schema, cronReadFunction) + "() TO PUBLIC"},
		{"procedure definition", "ALTER PROCEDURE " + qualified(f.Schema, cronProcedure) + "(integer) SET work_mem='8MB'"},
		{"target command", "UPDATE cron.job SET command='SELECT 42' WHERE jobid=1"},
		{"target schedule", "UPDATE cron.job SET schedule='0 * * * *' WHERE jobid=1"},
		{"witness schedule", "UPDATE cron.job SET schedule='3 seconds' WHERE jobid=2"},
		{"missing target", "DELETE FROM cron.job WHERE jobid=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := target.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx, tc.sql); err != nil {
				t.Fatal(err)
			}
			if err := validateCronFixture(ctx, tx, state, f.ID, cf, cronOriginal); err == nil {
				t.Fatal("changed catalog admitted")
			}
			var check int
			if err := tx.QueryRow(ctx, "SELECT 1").Scan(&check); err != nil || check != 1 {
				t.Fatalf("validation left pending batch rows: %d %v", check, err)
			}
		})
	}
}

type cronCatalogDelay struct{ queries, batches int }

func (p *cronCatalogDelay) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	p.queries++
	plannerDelay(ctx, 100*time.Millisecond)
	return ctx
}
func (*cronCatalogDelay) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func (p *cronCatalogDelay) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	p.batches++
	plannerDelay(ctx, 100*time.Millisecond)
	return ctx
}
func (*cronCatalogDelay) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {}
func (*cronCatalogDelay) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData)     {}
