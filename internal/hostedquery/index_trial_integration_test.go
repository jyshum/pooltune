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
	if err := target.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(ctx); err != nil {
		t.Fatal(err)
	}
	state, err = pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)
	config.Database = "postgres"
	target, err = pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close(ctx)
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

func TestRecoveryPreservesReplacedTable(t *testing.T) {
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

	stop := errors.New("interrupted after build")
	r, err := runIndexTrial(ctx, target, state, ScriptedInvestigator, "test", func() error { return stop })
	if !errors.Is(err, stop) {
		t.Fatalf("expected interruption, got %v", err)
	}
	table := qualified(trialSchema(r.RunID), "candidate")
	if _, err := target.Exec(ctx, "DROP TABLE "+table); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec(ctx, "CREATE TABLE "+table+" (id int PRIMARY KEY); INSERT INTO "+table+" VALUES (7)"); err != nil {
		t.Fatal(err)
	}
	if err := RecoverIndexTrials(ctx, target, state, "test"); err == nil {
		t.Fatal("recovery accepted replaced table")
	}
	var id int
	if err := target.QueryRow(ctx, "SELECT id FROM "+table).Scan(&id); err != nil || id != 7 {
		t.Fatalf("replacement was not preserved: %d %v", id, err)
	}
}

func TestReconcileAbsentAndInvalidIndex(t *testing.T) {
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
	if err := prepareJournal(ctx, state); err != nil {
		t.Fatal(err)
	}
	absent, invalid := "123456789012345678901234", "aaaaaaaaaaaaaaaaaaaaaaaa"
	for _, id := range []string{absent, invalid} {
		if _, err := state.Exec(ctx, "INSERT INTO "+journalTable+" (id,target,status) VALUES($1,$2,'setup')", id, journalTarget(target, "test")); err != nil {
			t.Fatal(err)
		}
	}
	if err := createTrial(ctx, target, state, invalid); err != nil {
		t.Fatal(err)
	}
	if err := writeTrialStatus(ctx, state, invalid, "building", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := buildTrialIndex(ctx, target, trialSchema(invalid)); err != nil {
		t.Fatal(err)
	}
	// Simulate the persistent catalog state of a failed concurrent build.
	if _, err := target.Exec(ctx, "UPDATE pg_index SET indisvalid=false WHERE indexrelid=to_regclass($1)", qualified(trialSchema(invalid), trialIndex)); err != nil {
		t.Fatal(err)
	}
	if err := RecoverIndexTrials(ctx, target, state, "test"); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{absent: "absent", invalid: "invalid"} {
		assertTrialRemoved(t, ctx, target, id)
		var status string
		if err := state.QueryRow(ctx, "SELECT recovered_index FROM "+journalTable+" WHERE id=$1", id).Scan(&status); err != nil || status != want {
			t.Fatalf("recovery wants %s, got %s (%v)", want, status, err)
		}
	}
}

func TestIndexTrialDefersWhenOtherSessionOwnsLock(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	owner, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close(ctx)
	contender, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Close(ctx)
	if err := lockTrial(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := lockTrial(ctx, contender); err == nil {
		t.Fatal("accepted overlapping trial")
	}
	if err := unlockTrial(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := lockTrial(ctx, contender); err != nil {
		t.Fatal(err)
	}
}
