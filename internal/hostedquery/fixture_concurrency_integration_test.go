//go:build integration

package hostedquery

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"pooltune/internal/testpg"
)

// Catch verification blocking unrelated healthy writes, while retaining the
// candidate-data and table-definition protections required by these fixtures.
func TestRepairFixtureAllowsHealthyWritesButProtectsIdentity(t *testing.T) {
	for _, kind := range []string{"index", "analyze", "statistics", "vacuum"} {
		t.Run(kind, func(t *testing.T) {
			ctx, config, _ := testpg.Start(t, "-c", "compute_query_id=on")
			target, err := pgx.ConnectConfig(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close(ctx)
			if _, err := target.Exec(ctx, "CREATE DATABASE fixture_state"); err != nil {
				t.Fatal(err)
			}
			stateConfig := config.Copy()
			stateConfig.Database = "fixture_state"
			state, err := pgx.ConnectConfig(ctx, stateConfig)
			if err != nil {
				t.Fatal(err)
			}
			defer state.Close(ctx)
			create := CreateAnalyzeFixture
			if kind == "vacuum" {
				create = CreateVacuumFixture
			}
			if kind == "statistics" {
				create = CreateStatisticsFixture
			}
			if kind == "index" {
				create = CreateConsumerFixture
			}
			fixture, err := create(ctx, target, state, "local")
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := RemoveConsumerFixture(ctx, target, state, "local", fixture.ID); err != nil {
					t.Error(err)
				}
			}()
			expected, err := expectedFixture(ctx, state, fixture.ID)
			if err != nil {
				t.Fatal(err)
			}
			other, err := pgx.ConnectConfig(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close(ctx)
			if _, err := other.Exec(ctx, "SET lock_timeout='100ms'; SET statement_timeout='1s'"); err != nil {
				t.Fatal(err)
			}
			tx, err := target.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if kind == "statistics" {
				err = lockStatisticsFixture(ctx, tx, fixture.ID, expected, false)
			} else if kind == "index" {
				err = lockSampleTables(ctx, tx, fixture.Schema)
			} else {
				err = lockAnalyzeFixture(ctx, tx, fixture.ID, expected)
			}
			if err != nil {
				t.Fatal(err)
			}
			healthy := qualified(fixture.Schema, "healthy")
			if _, err := other.Exec(ctx, "UPDATE ONLY "+healthy+" SET padding='changed' WHERE id=90001"); err != nil {
				t.Fatalf("healthy write blocked by verification: %v", err)
			}
			for _, sql := range []string{
				"UPDATE ONLY " + qualified(fixture.Schema, "candidate") + " SET padding='changed' WHERE id=90001",
				"ALTER TABLE " + healthy + " ADD COLUMN unsafe int",
				"CREATE INDEX unexpected_healthy_index ON " + healthy + " (lookup)",
			} {
				_, err := other.Exec(ctx, sql)
				var pgerr *pgconn.PgError
				if !errors.As(err, &pgerr) || pgerr.Code != "55P03" {
					t.Fatalf("guard did not reject %q: %v", sql, err)
				}
			}
		})
	}
}
