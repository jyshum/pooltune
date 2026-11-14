//go:build integration

package hostedquery

import (
	"testing"

	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
)

func TestRicherStatisticsCorrectBothLookupEstimates(t *testing.T) {
	ctx, config, _ := testpg.Start(t, "-c", "compute_query_id=on")
	target, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close(ctx)
	if _, err := target.Exec(ctx, "CREATE DATABASE stats_state"); err != nil {
		t.Fatal(err)
	}
	stateConfig := config.Copy()
	stateConfig.Database = "stats_state"
	state, err := pgx.ConnectConfig(ctx, stateConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)
	fixture, err := CreateStatisticsFixture(ctx, target, state, "local")
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
	ordinary, err := statisticsBaseline(ctx, target, fixture.ID, expected)
	if err != nil || !ordinary.Correlated || len(ordinary.Samples) != 2 {
		t.Fatalf("baseline: %+v %v", ordinary, err)
	}
	for i, sample := range ordinary.Samples {
		if sample.EstimateError < 10 || sample.PlanReturned != int64(statisticsCases[i].Rows) || sample.Plan != "Seq Scan" {
			t.Fatalf("expected correlated predicate error: %+v", sample)
		}
	}
	if err := writeTrialStatus(ctx, state, fixture.ID, "statistics_pending", nil, nil); err != nil {
		t.Fatal(err)
	}
	after, err := refreshExtendedStatistics(ctx, target, fixture.ID, expected, ordinary)
	if err != nil || !after.Applied || !after.Retained || after.Bytes <= 0 || len(after.Samples) != 2 {
		t.Fatalf("after: %+v %v", after, err)
	}
	for i, sample := range after.Samples {
		t.Logf("input %d: %s %.3f ms to %s %.3f ms, estimate %d to %d", i, ordinary.Samples[i].Plan, ordinary.Samples[i].ReadMS, sample.Plan, sample.ReadMS, ordinary.Samples[i].EstimatedRows, sample.EstimatedRows)
		if sample.EstimateError > 3 || sample.PlanReturned != int64(statisticsCases[i].Rows) || sample.Plan == "Seq Scan" {
			t.Fatalf("expected accurate indexed lookup: %+v", sample)
		}
	}
	second, err := refreshExtendedStatistics(ctx, target, fixture.ID, expected, ordinary)
	if err == nil || second.Applied {
		t.Fatalf("duplicate extended statistics: %+v %v", second, err)
	}
	observed, err := inspectPendingStatistics(ctx, state, config, fixture.ID)
	if err != nil || observed != "present" {
		t.Fatalf("pending object: %q %v", observed, err)
	}
	if err := pgx.BeginFunc(ctx, target, func(tx pgx.Tx) error { return validateStatisticsFixture(ctx, tx, fixture.ID, expected, true) }); err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(ctx, target, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "CREATE STATISTICS public.unrelated_healthy_mcv (mcv) ON lookup,padding FROM "+qualified(fixture.Schema, "healthy"))
		if err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := RemoveConsumerFixture(ctx, target, state, "local", fixture.ID); err == nil {
		t.Fatal("cleanup accepted an unrelated statistics object")
	}
	if _, err := target.Exec(ctx, "DROP STATISTICS public.unrelated_healthy_mcv"); err != nil {
		t.Fatal(err)
	}

}
