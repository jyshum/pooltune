//go:build integration

package hostedquery

import (
	"context"
	"errors"
	"testing"
	"time"

	"pooltune/internal/supabaseproject"
	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
)

func TestProjectActivityRetainsScopedWindowsAndDiagnosisAcrossReconnect(t *testing.T) {
	ctx, state, config := projectActivityTestState(t)
	project, err := supabaseproject.New("osdjqwpzgyzwfaxyufze", "20139985697ea001")
	if err != nil {
		t.Fatal(err)
	}
	other := "supabase/osdjqwpzgyzwfaxyufze/20139985697ea002"
	if _, err := state.Exec(ctx, "INSERT INTO pooltune_worker.projects(project_id, monitor_epoch) VALUES ($1,1),($2,1)", project.ID(), other); err != nil {
		t.Fatal(err)
	}
	const (
		healthyID = 42
		slowID    = -4567890123456789012
		idleID    = 44
		missingID = 45
	)
	at := time.Now().UTC().Add(-10 * time.Second).Truncate(time.Microsecond)
	policy := SamplingPolicy{Interval: time.Second, Window: time.Second}
	enrollment := Enrollment{Project: project.ID(), Queries: []QueryObjective{
		{Database: 7, Role: 9, Query: healthyID, MeanMS: 2},
		{Database: 7, Role: 9, Query: slowID, MeanMS: 2},
		{Database: 7, Role: 9, Query: idleID, MeanMS: 2},
		{Database: 7, Role: 9, Query: missingID, MeanMS: 2},
	}}
	record := func(db *pgx.Conn, enrollment Enrollment, sequence int64, epoch int64) (PollResult, error) {
		return recordProjectPoll(ctx, db, enrollment.Project, policy, at.Add(time.Duration(sequence)*time.Second), epoch,
			func(context.Context) (Enrollment, map[queryKey]Counter, error) {
				samples := make(map[queryKey]Counter)
				for _, entry := range []struct {
					query, calls, blocks int64
					execution            float64
				}{
					{healthyID, 10 + sequence, 10 + sequence, 10},
					{slowID, 10 + sequence, 10 + 3*sequence, 10 + float64(5*sequence)},
					{idleID, 10, 10, 10},
				} {
					counter := Counter{Database: 7, Role: 9, Query: entry.query, Boot: at, Reset: at, Entry: at,
						Calls: entry.calls, Blocks: entry.blocks, ExecutionMS: entry.execution}
					samples[counter.key()] = counter
				}
				return enrollment, samples, nil
			})
	}
	for interval := int64(0); interval <= 5; interval++ {
		if _, err := record(state, enrollment, interval, 1); err != nil {
			t.Fatal(err)
		}
	}
	if result, err := record(state, enrollment, 5, 1); err != nil || !result.Skipped {
		t.Fatalf("duplicate poll must be skipped: %+v, %v", result, err)
	}
	if _, err := record(state, Enrollment{Project: other, Queries: enrollment.Queries}, 0, 1); err != nil {
		t.Fatal(err)
	}
	var epoch int64 = 1
	job, err := claimProjectInvestigation(ctx, state, project.ID(), &epoch)
	if err != nil || job == nil || job.Query != slowID {
		t.Fatalf("expected the slow investigation: %+v, %v", job, err)
	}
	if err := finishInvestigation(ctx, state, *job, DispatchResult{
		Outcome: "diagnosed", ElapsedMS: 1200, TargetPlanMS: 100,
		LookupDiagnosis: &LookupDiagnosis{Reason: "One\n  reason.", ModelMS: 800},
		TrafficPlan:     &TrafficPlan{Node: "Seq Scan"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := DisconnectProjectMonitor(ctx, state, project); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(ctx); err != nil {
		t.Fatal(err)
	}
	state = openMonitorConn(t, ctx, config)
	activity, err := ReadProjectActivity(ctx, state, project.ID(), at)
	if err != nil || activity.LastPoll == nil || !activity.LastPoll.Equal(at.Add(5*time.Second)) || len(activity.Polls) != 6 {
		t.Fatalf("disconnect and new state connection must preserve six successful polls: %+v, %v", activity, err)
	}
	if len(activity.Windows) != 20 || len(activity.Jobs) != 1 {
		t.Fatalf("wanted 20 finalized query windows and one diagnosis: windows=%d jobs=%d", len(activity.Windows), len(activity.Jobs))
	}
	var latest = make(map[int64]ProjectWindow)
	for _, window := range activity.Windows {
		latest[window.Query] = window
	}
	healthy, slow := latest[healthyID], latest[slowID]
	if healthy.Decision != NoChange || healthy.MeanMS == nil || *healthy.MeanMS != 0 ||
		slow.Decision != Investigate || slow.MeanMS == nil || *slow.MeanMS != 5 ||
		slow.SharedBlocks == nil || *slow.SharedBlocks != 3 || slow.ObjectiveMS != 2 {
		t.Fatalf("wrong healthy or slow metrics: %+v %+v", healthy, slow)
	}
	idle, missing := latest[idleID], latest[missingID]
	if idle.Decision != InsufficientEvidence || idle.Calls == nil || *idle.Calls != 0 ||
		idle.SharedBlocks == nil || *idle.SharedBlocks != 0 || idle.MeanMS != nil ||
		missing.Decision != InsufficientEvidence || missing.Calls != nil ||
		missing.SharedBlocks != nil || missing.MeanMS != nil {
		t.Fatalf("zero calls confused with an absent counter: idle=%+v missing=%+v", idle, missing)
	}
	saved := activity.Jobs[0]
	if saved.ID != job.ID || saved.Attempts != 1 || saved.ClaimedAt == nil || saved.FinishedAt == nil ||
		saved.ClaimedAt.Before(saved.CreatedAt) || saved.FinishedAt.Before(*saved.ClaimedAt) ||
		saved.Outcome != "diagnosed" || saved.Reason != "One reason." ||
		saved.PlanNode != "Seq Scan" || saved.PlanSource != "ordinary_traffic" ||
		saved.ModelMS == nil || *saved.ModelMS != 800 || saved.ElapsedMS == nil || *saved.ElapsedMS != 1200 {
		t.Fatalf("wrong typed investigation summary: %+v", saved)
	}
	foreign, err := ReadProjectActivity(ctx, state, other, at)
	if err != nil || len(foreign.Polls) != 1 || len(foreign.Windows) != 0 || len(foreign.Jobs) != 0 {
		t.Fatalf("activity crossed project boundaries: %+v, %v", foreign, err)
	}
	unset, err := ReadProjectActivity(ctx, state, "unregistered", at)
	if err != nil || unset.LastPoll != nil || len(unset.Polls)+len(unset.Windows)+len(unset.Jobs) != 0 {
		t.Fatalf("unregistered project should have no activity: %+v, %v", unset, err)
	}

	if _, err := state.Exec(ctx, "UPDATE pooltune_worker.projects SET monitor_epoch=2 WHERE project_id=$1", project.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := record(state, enrollment, 7, 2); err != nil {
		t.Fatal(err)
	}
	after, err := ReadProjectActivity(ctx, state, project.ID(), at)
	if err != nil || len(after.Polls) != 7 || !after.Polls[6].At.Equal(at.Add(7*time.Second)) ||
		len(after.Windows) != 20 || after.LastPoll == nil || !after.LastPoll.Equal(after.Polls[6].At) {
		t.Fatalf("reconnect should preserve the gap and start a new window: %+v, %v", after, err)
	}
}

func TestProjectActivityPrunesOldSuccessfulPolls(t *testing.T) {
	ctx, state, _ := projectActivityTestState(t)
	enrollment := Enrollment{Project: "supabase/osdjqwpzgyzwfaxyufze/20139985697ea003",
		Queries: []QueryObjective{{Database: 7, Role: 9, Query: 3, MeanMS: 2}}}
	if _, err := state.Exec(ctx, "INSERT INTO pooltune_worker.projects(project_id, monitor_epoch) VALUES ($1,1)", enrollment.Project); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	policy := SamplingPolicy{Interval: time.Second, Window: time.Second}
	collect := func(context.Context) (Enrollment, map[queryKey]Counter, error) {
		return enrollment, nil, nil
	}
	for _, at := range []time.Time{now.Add(-9 * 24 * time.Hour), now.Add(-9*24*time.Hour + time.Second), now.Add(-7 * 24 * time.Hour), now} {
		if _, err := recordProjectPoll(ctx, state, enrollment.Project, policy, at, 1, collect); err != nil {
			t.Fatal(err)
		}
	}
	var polls, windows int
	err := state.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM pooltune_worker.project_polls WHERE project_id=$1),
		(SELECT count(*) FROM pooltune_worker.project_windows WHERE project_id=$1)`, enrollment.Project).Scan(&polls, &windows)
	if err != nil || polls != 2 || windows != 2 {
		t.Fatalf("expected old polls and windows to be removed and week-old history retained: polls=%d windows=%d err=%v", polls, windows, err)
	}
	pollErr := errors.New("target error")
	_, err = recordProjectPoll(ctx, state, enrollment.Project, policy, now.Add(time.Second), 1,
		func(context.Context) (Enrollment, map[queryKey]Counter, error) { return Enrollment{}, nil, pollErr })
	if !errors.Is(err, pollErr) {
		t.Fatalf("expected target error, got %v", err)
	}
	recent, err := ReadProjectActivity(ctx, state, enrollment.Project, now.Add(-time.Hour))
	if err != nil || len(recent.Polls) != 1 || len(recent.Windows) != 1 || !recent.Polls[0].At.Equal(now) {
		t.Fatalf("a failed poll must leave a gap and explicit ranges must filter history: %+v, %v", recent, err)
	}
}

func projectActivityTestState(t *testing.T) (context.Context, *pgx.Conn, *pgx.ConnConfig) {
	t.Helper()
	ctx, config, _ := testpg.Start(t)
	state := openMonitorConn(t, ctx, config)
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	return ctx, state, config
}
