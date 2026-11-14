//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"pooltune/internal/hostedquery"
	"pooltune/internal/labdb"
	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
)

type consumerRig struct {
	ctx                 context.Context
	target, state       *pgx.Conn
	targetDSN, stateDSN string
	root, binary, route string
	fixture             hostedquery.ConsumerFixture
}

func newConsumerRig(t *testing.T, rookery bool) *consumerRig {
	t.Helper()
	_, local, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	r := &consumerRig{ctx: ctx, route: "local"}
	r.root, _ = filepath.Abs("../..")
	r.binary = filepath.Join(t.TempDir(), "pooltune-consumer")
	build := exec.CommandContext(ctx, "go", "build", "-o", r.binary, "./cmd/pooltune-consumer")
	build.Dir = r.root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build consumer: %v %s", err, output)
	}
	stateURL := url.URL{Scheme: "postgres", User: url.UserPassword(local.User, local.Password), Host: net.JoinHostPort(local.Host, strconv.Itoa(int(local.Port))), Path: "/postgres", RawQuery: "sslmode=disable"}
	r.stateDSN = stateURL.String()
	r.state = connectTest(t, ctx, local)
	if rookery {
		r.route, r.targetDSN = "rookery", os.Getenv("POOLTUNE_BENCH_TARGET_DSN")
	} else {
		if _, err := r.state.Exec(ctx, "CREATE DATABASE consumer_target"); err != nil {
			t.Fatal(err)
		}
		stateURL.Path = "/consumer_target"
		r.targetDSN = stateURL.String()
	}
	config, err := labdb.Config(r.targetDSN, rookery)
	if err != nil {
		t.Fatal(err)
	}
	r.target = connectTest(t, ctx, config)
	if !rookery {
		if _, err := r.target.Exec(ctx, "CREATE EXTENSION pg_stat_statements"); err != nil {
			t.Fatal(err)
		}
	}
	r.fixture, err = hostedquery.CreateConsumerFixture(ctx, r.target, r.state, r.route)
	if r.fixture.ID != "" {
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 12*time.Second)
			defer stop()
			if err := hostedquery.RemoveConsumerFixture(cleanup, r.target, r.state, r.route, r.fixture.ID); err != nil {
				t.Errorf("cleanup fixture %s: %v", r.fixture.ID, err)
			}
		})
	}
	if err != nil {
		t.Fatalf("setup fixture: %v", err)
	}
	if err := hostedquery.PrepareWorkerState(ctx, r.state); err != nil {
		t.Fatal(err)
	}
	if _, err := r.state.Exec(ctx, "INSERT INTO pooltune_worker.projects(project_id) VALUES($1)", r.fixture.Enrollment.Project); err != nil {
		t.Fatal(err)
	}
	return r
}

func connectTest(t *testing.T, ctx context.Context, config *pgx.ConnConfig) *pgx.Conn {
	t.Helper()
	c, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(labdb.Diagnostic("connect test", err))
	}
	t.Cleanup(func() { labdb.Close(c) })
	return c
}

func (r *consumerRig) sqlLookup(table, column string) string {
	return "SELECT id FROM " + pgx.Identifier{r.fixture.Schema, table}.Sanitize() + " WHERE " + column + "=$1"
}

// Queue directly so the test controls when the consumer gets work.
func (r *consumerRig) enqueue(t *testing.T) int64 {
	t.Helper()
	query := r.fixture.Enrollment.Queries[1]
	var result int32
	if err := r.target.QueryRow(r.ctx, r.sqlLookup("candidate", "lookup"), int32(43)).Scan(&result); err != nil || result != 43 {
		t.Fatalf("warmup = %d, %v", result, err)
	}
	var schema string
	if err := r.target.QueryRow(r.ctx, "SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='pg_stat_statements'").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	var boot, reset, entry time.Time
	err := r.target.QueryRow(r.ctx, fmt.Sprintf(`SELECT pg_postmaster_start_time(),i.stats_reset,s.stats_since FROM %s(false) s CROSS JOIN %s i WHERE s.dbid=$1 AND s.userid=$2 AND s.queryid=$3 AND s.toplevel`,
		pgx.Identifier{schema, "pg_stat_statements"}.Sanitize(), pgx.Identifier{schema, "pg_stat_statements_info"}.Sanitize()),
		query.Database, query.Role, query.Query).Scan(&boot, &reset, &entry)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{"counter_generation": []time.Time{boot.UTC(), reset.UTC(), entry.UTC()}})
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	err = r.state.QueryRow(r.ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence)
		VALUES($1,$2,$3,$4,$5::jsonb) RETURNING id`, r.fixture.Enrollment.Project, query.Database, query.Role, query.Query, string(data)).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (r *consumerRig) start(t *testing.T) func() {
	t.Helper()
	cmd := exec.CommandContext(r.ctx, r.binary, "-target", r.route, "-project", r.fixture.Enrollment.Project, "-fixture", r.fixture.ID, "-poll", "200ms")
	cmd.Dir = r.root
	cmd.Env = append(os.Environ(), "POOLTUNE_WORKER_TARGET_DSN="+r.targetDSN, "POOLTUNE_WORKER_STATE_DSN="+r.stateDSN)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			cmd.Process.Kill()
			cmd.Wait()
		}
	}
	t.Cleanup(stop)
	return stop
}

func (r *consumerRig) job(t *testing.T, id int64) (int, *time.Time, *hostedquery.DispatchResult) {
	t.Helper()
	var attempts int
	var lease *time.Time
	var result *hostedquery.DispatchResult
	if err := r.state.QueryRow(r.ctx, "SELECT attempts,lease_until,result FROM pooltune_worker.investigations WHERE id=$1", id).Scan(&attempts, &lease, &result); err != nil {
		t.Fatal(err)
	}
	return attempts, lease, result
}

func awaitJob(t *testing.T, limit time.Duration, check func() bool) {
	t.Helper()
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the consumer")
}

func TestConsumerRestartsAndCollectsFreshEvidence(t *testing.T) {
	r := newConsumerRig(t, false)
	id := r.enqueue(t)
	blocker, err := r.target.Begin(r.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(r.ctx)
	table := pgx.Identifier{r.fixture.Schema, "candidate"}.Sanitize()
	if _, err := blocker.Exec(r.ctx, "LOCK TABLE "+table+" IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	stop := r.start(t)
	var lease *time.Time
	awaitJob(t, 5*time.Second, func() bool {
		attempts, until, result := r.job(t, id)
		lease = until
		return attempts == 1 && until != nil && result == nil
	})
	stop()
	if err := blocker.Rollback(r.ctx); err != nil {
		t.Fatal(err)
	}
	index := pgx.Identifier{r.fixture.Schema, "consumer_recovery_idx"}.Sanitize()
	if _, err := r.target.Exec(r.ctx, "CREATE INDEX consumer_recovery_idx ON "+table+" (lookup)"); err != nil {
		t.Fatal(err)
	}
	defer r.target.Exec(r.ctx, "DROP INDEX "+index)
	resume := r.start(t)
	time.Sleep(time.Second)
	if attempts, _, result := r.job(t, id); attempts != 1 || result != nil {
		t.Fatalf("job ran before lease expiry: %d %+v", attempts, result)
	}
	awaitJob(t, time.Until(*lease)+6*time.Second, func() bool {
		attempts, _, result := r.job(t, id)
		if result == nil {
			return false
		}
		if attempts != 2 || result.JobID != id || result.Outcome != "no_change" || result.Evidence == nil || !result.Evidence.Existing || result.Evidence.Plan.Node == "Seq Scan" {
			t.Fatalf("did not recover the job with fresh evidence: %+v", result)
		}
		t.Logf("same job %d finished on attempt %d after the original lease expired; fresh plan %s, saved %.3f s after lease expiry", id, attempts, result.Evidence.Plan.Node, time.Since(*lease).Seconds())
		return true
	})
	resume()
}

func TestConsumerRookeryLoad(t *testing.T) {
	if os.Getenv("POOLTUNE_BENCH_TARGET_DSN") == "" {
		t.Skip("explicit Rookery benchmark configuration required")
	}
	r := newConsumerRig(t, true)
	fingerprint := func() string {
		var hash string
		err := r.target.QueryRow(r.ctx, "SELECT md5(coalesce(string_agg(indexdef, E'\\n' ORDER BY indexdef),'')) FROM pg_indexes WHERE schemaname='public'").Scan(&hash)
		if err != nil {
			t.Fatal(err)
		}
		return hash
	}
	before := fingerprint()
	for phase, on := range []bool{false, true, true, false, false, true} {
		r.measurePhase(t, phase+1, on)
	}
	if err := hostedquery.RemoveConsumerFixture(r.ctx, r.target, r.state, r.route, r.fixture.ID); err != nil {
		t.Fatal(err)
	}
	var absent bool
	if err := r.target.QueryRow(r.ctx, "SELECT to_regnamespace($1) IS NULL", r.fixture.Schema).Scan(&absent); err != nil || !absent || fingerprint() != before {
		t.Fatalf("cleanup verification failed: schema absent=%v, error=%v", absent, err)
	}
	t.Log("verified test schema absent and public index fingerprint unchanged")
}

type statementTotal struct {
	Calls       int64
	ExecutionMS float64
}

func (r *consumerRig) statementTotals(t *testing.T, schema string) [2]statementTotal {
	t.Helper()
	var totals [2]statementTotal
	for i, query := range r.fixture.Enrollment.Queries {
		sql := fmt.Sprintf(`
			SELECT coalesce(sum(calls),0)::bigint, coalesce(sum(total_exec_time),0) FROM %s(false)
			WHERE dbid=$1 AND userid=$2 AND queryid=$3 AND toplevel
		`, pgx.Identifier{schema, "pg_stat_statements"}.Sanitize())
		if err := r.target.QueryRow(r.ctx, sql, query.Database, query.Role, query.Query).Scan(&totals[i].Calls, &totals[i].ExecutionMS); err != nil {
			t.Fatal(err)
		}
	}
	return totals
}

func (r *consumerRig) measurePhase(t *testing.T, phase int, on bool) {
	t.Helper()
	jobID := r.enqueue(t)
	var statsSchema string
	if err := r.target.QueryRow(r.ctx, "SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='pg_stat_statements'").Scan(&statsSchema); err != nil {
		t.Fatal(err)
	}
	pre := r.statementTotals(t, statsSchema)
	var healthy, slow []float64
	var attemptMS, planMS float64
	var blocks int64
	var stop func()
	const cadence = 150 * time.Millisecond
	const count = 28
	start := time.Now()
	for i := range count {
		remaining := time.Until(start.Add(time.Duration(i) * cadence))
		if remaining > 0 {
			time.Sleep(remaining)
		}
		if on && i == 3 {
			stop = r.start(t)
		}
		input := int32(1 + (i*7919)%100000)
		for _, tc := range []struct {
			sql    string
			values *[]float64
		}{
			{r.sqlLookup("healthy", "id"), &healthy},
			{r.sqlLookup("candidate", "lookup"), &slow},
		} {
			before := time.Now()
			var actual int32
			ctx, cancel := context.WithTimeout(r.ctx, 750*time.Millisecond)
			err := r.target.QueryRow(ctx, tc.sql, input).Scan(&actual)
			cancel()
			if err != nil || actual != input {
				t.Fatalf("traffic lookup %d: %v", actual, err)
			}
			*tc.values = append(*tc.values, float64(time.Since(before))/float64(time.Millisecond))
		}
	}
	if on {
		_, _, result := r.job(t, jobID)
		if result == nil || result.Outcome != "propose" || result.Evidence == nil || result.Evidence.Plan.ExaminedRows != 100000 || result.TargetPlanMS <= 0 {
			t.Fatalf("planned consumer work did not overlap traffic: %+v", result)
		}
		attemptMS, planMS, blocks = result.ElapsedMS, result.TargetPlanMS, result.Evidence.Plan.SharedBlocks
		stop()
	} else {
		if _, err := r.state.Exec(r.ctx, "UPDATE pooltune_worker.investigations SET finished_at=now() WHERE id=$1 AND attempts=0", jobID); err != nil {
			t.Fatal(err)
		}
	}
	post := r.statementTotals(t, statsSchema)
	for index := range post {
		if post[index].Calls-pre[index].Calls != count {
			t.Fatalf("foreground statement %d recorded %d calls, expected %d", index, post[index].Calls-pre[index].Calls, count)
		}
	}
	slices.Sort(healthy)
	slices.Sort(slow)
	t.Logf("phase=%d consumer=%v requests=%d healthy_p50_ms=%.3f healthy_p95_ms=%.3f slow_p50_ms=%.3f slow_p95_ms=%.3f healthy_db_ms=%.3f slow_db_ms=%.3f attempt_ms=%.3f plan_ms=%.3f plan_blocks=%d",
		phase, on, len(healthy)+len(slow), healthy[13], healthy[26], slow[13], slow[26],
		(post[0].ExecutionMS-pre[0].ExecutionMS)/count, (post[1].ExecutionMS-pre[1].ExecutionMS)/count, attemptMS, planMS, blocks)
}
