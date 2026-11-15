//go:build integration

package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
)

func TestManagedPollSkipsRepairFenceAndResumes(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	poll := openMonitorConn(t, ctx, config)
	repair := openMonitorConn(t, ctx, config)
	if err := PrepareWorkerState(ctx, poll); err != nil {
		t.Fatal(err)
	}
	const project = "repair-fence"
	if _, err := poll.Exec(ctx, "INSERT INTO pooltune_worker.projects(project_id,monitor_epoch) VALUES($1,5)", project); err != nil {
		t.Fatal(err)
	}
	fence, err := repair.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Rollback(ctx)
	if _, err := fence.Exec(ctx, "SELECT project_id FROM pooltune_worker.projects WHERE project_id=$1 FOR UPDATE", project); err != nil {
		t.Fatal(err)
	}
	policy := SamplingPolicy{Interval: time.Second, Window: time.Second}
	collects := 0
	collect := func(context.Context) (Enrollment, map[queryKey]Counter, error) {
		collects++
		return Enrollment{Project: project}, nil, nil
	}
	at := time.Now().UTC()
	bounded, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	skip, err := recordProjectPoll(bounded, poll, project, policy, at, 5, collect)
	if err != nil || !skip.Skipped || collects != 0 {
		t.Fatalf("held repair fence should skip a sample: result=%+v reads=%d err=%v", skip, collects, err)
	}
	if _, err := recordProjectPoll(bounded, poll, project, policy, at, 4, collect); !errors.Is(err, ErrProjectDisconnected) {
		t.Fatalf("stale epoch under repair fence: %v", err)
	}
	if err := fence.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	next, err := recordProjectPoll(ctx, poll, project, policy, at.Add(time.Second), 5, collect)
	if err != nil || next.Skipped || collects != 1 {
		t.Fatalf("poll resumes after the repair: result=%+v reads=%d err=%v", next, collects, err)
	}
}

func TestRecordPollUpgradesHistoricalNumericCounter(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	state, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(context.Background())
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	const query int64 = -1234567890123456789
	at := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	policy := SamplingPolicy{Interval: time.Second, Window: time.Second}
	enrollment := Enrollment{Project: "upgrade", Queries: []QueryObjective{{Database: 1, Role: 2, Query: query, MeanMS: 1}}}
	// Historical persisted JSON, deliberately not serialized by today's Counter.
	raw := `{"Policy":{"Interval":1000000000,"Window":1000000000},"States":[{"Objective":{"database_oid":1,"role_oid":2,"query_id":"-1234567890123456789","mean_execution_ms":1},"Window":{"LastAt":"2026-11-14T12:00:00Z","Start":"2026-11-14T12:00:00Z","Last":{"Database":1,"Role":2,"Query":-1234567890123456789,"Boot":"2026-11-14T12:00:00Z","Reset":"2026-11-14T12:00:00Z","Entry":"2026-11-14T12:00:00Z","Calls":10,"Blocks":20,"ExecutionMS":40},"Recent":[{"Calls":2,"Blocks":4,"ExecutionMS":8},{"Calls":2,"Blocks":4,"ExecutionMS":8},{"Calls":2,"Blocks":4,"ExecutionMS":8},{"Calls":2,"Blocks":4,"ExecutionMS":8}]}}]}`
	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.projects(project_id,last_poll,evidence) VALUES($1,$2,$3::jsonb)`, enrollment.Project, at, raw); err != nil {
		t.Fatal(err)
	}
	collected := false
	sample := Counter{Database: 1, Role: 2, Query: query, Boot: at, Reset: at, Entry: at, Calls: 12, Blocks: 24, ExecutionMS: 48}
	result, err := recordPoll(ctx, state, enrollment, policy, at.Add(time.Second), func(context.Context) (map[queryKey]Counter, error) {
		collected = true
		return map[queryKey]Counter{sample.key(): sample}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !collected || result.Skipped || len(result.Windows) != 1 || result.Windows[0].Calls != 2 || result.Windows[0].MeanMS != 4 || result.Windows[0].Decision != Investigate || len(result.Jobs) != 1 || result.Jobs[0].Query != query {
		t.Fatalf("historical state did not advance into an investigation: %+v, collected=%v", result, collected)
	}
	var saved []byte
	var last time.Time
	var kind, identity string
	if err := state.QueryRow(ctx, `SELECT last_poll,evidence,jsonb_typeof(evidence#>'{States,0,Window,Last,Query}'),evidence#>>'{States,0,Window,Last,Query}' FROM pooltune_worker.projects WHERE project_id=$1`, enrollment.Project).Scan(&last, &saved, &kind, &identity); err != nil {
		t.Fatal(err)
	}
	var poll savedPoll
	if err := json.Unmarshal(saved, &poll); err != nil {
		t.Fatal(err)
	}
	if !last.Equal(at.Add(time.Second)) || kind != "string" || identity != fmt.Sprint(query) || len(poll.States) != 1 || poll.States[0].Window.Last == nil || *poll.States[0].Window.Last != sample || len(poll.States[0].Window.Recent) != 5 {
		t.Fatalf("upgraded state not persisted exactly: last=%s kind=%s identity=%s poll=%+v", last, kind, identity, poll)
	}
}

func TestWorkerQueueSurvivesRestartAndConcurrentPolls(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	connect := func() *pgx.Conn {
		t.Helper()
		conn, err := pgx.ConnectConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close(context.Background()) })
		return conn
	}
	primary, competing := connect(), connect()
	if err := PrepareWorkerState(ctx, primary); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	enrollment := Enrollment{Project: "project-one", Queries: []QueryObjective{
		{Database: 1, Role: 2, Query: 3, MeanMS: 1},
		{Database: 1, Role: 2, Query: 4, MeanMS: 1},
	}}
	policy := SamplingPolicy{Interval: time.Second, Window: 2 * time.Second}
	record := func(conn *pgx.Conn, project Enrollment, i int) (PollResult, error) {
		collect := func(context.Context) (map[queryKey]Counter, error) {
			healthy := Counter{Database: 1, Role: 2, Query: 3, Boot: now, Reset: now, Entry: now, Calls: int64(i * 2), ExecutionMS: float64(i)}
			slow := healthy
			slow.Query, slow.ExecutionMS = 4, float64(i*8)
			return map[queryKey]Counter{healthy.key(): healthy, slow.key(): slow}, nil
		}
		return recordPoll(ctx, conn, project, policy, now.Add(time.Duration(i)*time.Second), collect)
	}
	for i := 0; i <= 7; i++ {
		result, err := record(primary, enrollment, i)
		if err != nil || len(result.Jobs) != 0 {
			t.Fatalf("before five windows, got %+v, %v", result, err)
		}
	}
	if err := primary.Close(ctx); err != nil {
		t.Fatal(err)
	}
	primary = connect()
	queued := false
	for _, i := range []int{8, 9, 10, 10, 11, 12} {
		var group sync.WaitGroup
		type outcome struct {
			result PollResult
			err    error
		}
		outcomes := make(chan outcome, 2)
		for _, conn := range []*pgx.Conn{primary, competing} {
			group.Go(func() {
				result, err := record(conn, enrollment, i)
				outcomes <- outcome{result, err}
			})
		}
		group.Wait()
		close(outcomes)
		jobs, healthy := 0, 0
		for sample := range outcomes {
			if sample.err != nil {
				t.Fatal(sample.err)
			}
			jobs += len(sample.result.Jobs)
			for _, window := range sample.result.Windows {
				if window.Query == 3 && window.Decision == NoChange {
					healthy++
				}
			}
		}
		if i == 10 && !queued {
			if jobs != 1 || healthy != 1 {
				t.Fatalf("first decision: jobs=%d healthy=%d", jobs, healthy)
			}
			queued = true
		} else if jobs != 0 {
			t.Fatalf("tick %d created %d extra jobs", i, jobs)
		}
	}
	enrollment.Project = "project-two"
	for i := 0; i <= 10; i++ {
		if _, err := record(primary, enrollment, i); err != nil {
			t.Fatal(err)
		}
	}
	var total, distinctProjects, healthy int
	err := primary.QueryRow(ctx, `SELECT count(*), count(DISTINCT project_id), count(*) FILTER (WHERE query_id=3)
		FROM pooltune_worker.investigations`).Scan(&total, &distinctProjects, &healthy)
	if err != nil || total != 2 || distinctProjects != 2 || healthy != 0 {
		t.Fatalf("jobs=%d projects=%d healthy=%d error=%v", total, distinctProjects, healthy, err)
	}
	if _, err := primary.Exec(ctx, `UPDATE pooltune_worker.investigations
		SET finished_at=now(), result='{"outcome":"propose"}' WHERE project_id=$1`, enrollment.Project); err != nil {
		t.Fatal(err)
	}
	for _, tick := range []int{11, 12} {
		result, err := record(primary, enrollment, tick)
		if err != nil || len(result.Jobs) != 0 {
			t.Fatalf("cooldown allowed a duplicate: %+v, %v", result, err)
		}
	}
	if _, err := primary.Exec(ctx, `UPDATE pooltune_worker.investigations
		SET finished_at=now()-interval '61 minutes' WHERE project_id=$1`, enrollment.Project); err != nil {
		t.Fatal(err)
	}
	for _, tick := range []int{13, 14} {
		result, err := record(primary, enrollment, tick)
		if err != nil || (tick == 14 && len(result.Jobs) != 1) {
			t.Fatalf("expired cooldown did not allow new evidence: %+v, %v", result, err)
		}
	}

}
