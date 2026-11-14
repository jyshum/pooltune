//go:build integration

package hostedquery

import (
	"context"
	"sync"
	"testing"
	"time"

	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
)

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
}
