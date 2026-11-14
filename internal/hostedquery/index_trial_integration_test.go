//go:build integration

package hostedquery

import (
	"context"
	"errors"
	"testing"

	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
)

func TestAutomaticIndexTrial(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	target, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close(ctx)
	if _, err := target.Exec(ctx, "CREATE DATABASE trial_state"); err != nil {
		t.Fatal(err)
	}
	config.Database = "trial_state"
	state, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)

	report, err := RunIndexTrial(ctx, target, state, ScriptedInvestigator, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !report.IndexValid || report.After.Blocks >= report.Before.Blocks || report.After.ReadMS >= report.Before.ReadMS ||
		report.Before.Writes != 40 || report.After.Writes != 40 || report.Before.Errors+report.After.Errors != 0 {
		t.Fatalf("unexpected result: %+v", report)
	}
	if report.Before.PlanReturned != 1 || report.After.PlanReturned != 1 || report.HealthyBuilt {
		t.Fatalf("unexpected query result or healthy repair: %+v", report)
	}
	assertTrialRemoved(t, ctx, target, report.RunID)
	var status string
	if err := state.QueryRow(ctx, "SELECT status FROM pooltune_lab_state.index_runs WHERE id=$1", report.RunID).Scan(&status); err != nil || status != "complete" {
		t.Fatalf("journal status %q: %v", status, err)
	}

	declined, err := RunIndexTrial(ctx, target, state, func(_ context.Context, e IndexEvidence) (IndexProposal, error) {
		return IndexProposal{Outcome: "propose", RunID: e.RunID, TableOID: e.TableOID + 1, Column: e.Column, Reason: "unrelated table"}, nil
	}, "test")
	if err != nil || declined.Outcome != "no_change" || declined.IndexValid {
		t.Fatalf("accepted mismatched index: %+v / %v", declined, err)
	}
	assertTrialRemoved(t, ctx, target, declined.RunID)
}

func TestIndexTrialRecoversInterruptedBuild(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	target, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close(ctx)
	if _, err := target.Exec(ctx, "CREATE DATABASE trial_state"); err != nil {
		t.Fatal(err)
	}
	config.Database = "trial_state"
	state, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)

	interrupted := errors.New("lost acknowledgement")
	report, err := runIndexTrial(ctx, target, state, ScriptedInvestigator, "test", func() error { return interrupted })
	if !errors.Is(err, interrupted) {
		t.Fatalf("wanted simulated interruption: %v", err)
	}
	if err := RecoverIndexTrials(ctx, target, state, "test"); err != nil {
		t.Fatal(err)
	}
	assertTrialRemoved(t, ctx, target, report.RunID)
	var status, observed string
	if err := state.QueryRow(ctx, "SELECT status, recovered_index FROM pooltune_lab_state.index_runs WHERE id=$1", report.RunID).Scan(&status, &observed); err != nil || status != "recovered" || observed != "valid" {
		t.Fatalf("recovery %s / %s: %v", status, observed, err)
	}
}

func assertTrialRemoved(t *testing.T, ctx context.Context, target *pgx.Conn, id string) {
	t.Helper()
	var removed bool
	if err := target.QueryRow(ctx, "SELECT to_regnamespace($1) IS NULL", trialSchema(id)).Scan(&removed); err != nil || !removed {
		t.Fatalf("fixture still present: %v", err)
	}
}
