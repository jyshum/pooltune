//go:build integration

package hostedquery

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"pooltune/internal/supabaseproject"
	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestSupabaseProjectMonitoringLocal(t *testing.T) {
	_, config, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	testSupabaseProjectMonitoring(t, config, false)
}

func TestSupabaseProjectMonitoringRookery(t *testing.T) {
	password := os.Getenv("POOLTUNE_TEST_ROOKERY_PASSWORD")
	if password == "" {
		t.Skip("Rookery fixture credentials unavailable")
	}
	ca, err := os.ReadFile(os.Getenv("POOLTUNE_TEST_ROOKERY_CA"))
	if err != nil {
		t.Fatal("Rookery CA unavailable")
	}
	config, err := supabaseproject.DirectConfig("osdjqwpzgyzwfaxyufze", "postgres", password, ca)
	if err != nil {
		t.Fatal(err)
	}
	testSupabaseProjectMonitoring(t, config, true)
}

func testSupabaseProjectMonitoring(t *testing.T, targetConfig *pgx.ConnConfig, remote bool) {
	t.Helper()
	ctx, stop := context.WithTimeout(context.Background(), 50*time.Second)
	defer stop()
	admin := openMonitorConn(t, ctx, targetConfig)
	if !remote {
		mustMonitorExec(t, ctx, admin, "CREATE EXTENSION pg_stat_statements")
	}
	_, stateConfig, _ := testpg.Start(t)
	state := openMonitorConn(t, ctx, stateConfig)
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}

	key := monitorRandom(t, 8)
	project, err := supabaseproject.New("osdjqwpzgyzwfaxyufze", key)
	if err != nil {
		t.Fatal(err)
	}
	role := project.Role()
	appRole := "pt_app_" + key
	appSchema := pgx.Identifier{appRole}.Sanitize()
	monitorSchema := pgx.Identifier{role}.Sanitize()
	database := pgx.Identifier{targetConfig.Database}.Sanitize()
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		conn, err := pgx.ConnectConfig(cleanup, targetConfig)
		if err != nil {
			t.Error("could not reconnect for test cleanup")
			return
		}
		defer conn.Close(cleanup)
		if _, err := conn.Exec(cleanup, fmt.Sprintf(`
			DROP FUNCTION IF EXISTS %s.snapshot();
			DROP TABLE IF EXISTS %s.enrolled_queries;
			DROP TABLE IF EXISTS %s.items;
			DROP SCHEMA IF EXISTS %s;
			DROP SCHEMA IF EXISTS %s;
			DO $$ BEGIN IF EXISTS (SELECT FROM pg_roles WHERE rolname='%s')
				THEN REVOKE CONNECT ON DATABASE %s FROM %s; END IF; END $$;
			DROP ROLE IF EXISTS %s;
			DROP ROLE IF EXISTS %s;`,
			monitorSchema, monitorSchema, appSchema, monitorSchema, appSchema, role, database, monitorSchema, monitorSchema, appSchema)); err != nil {
			t.Error("owned fixture cleanup failed", monitorSQLState(err))
			return
		}
		var remains bool
		if err := conn.QueryRow(cleanup, "SELECT to_regnamespace($1) IS NOT NULL OR to_regnamespace($2) IS NOT NULL OR EXISTS (SELECT FROM pg_roles WHERE rolname IN ($1,$2))", role, appRole).Scan(&remains); err != nil || remains {
			t.Error("owned fixture cleanup readback failed")
		}
	})

	mustMonitorExec(t, ctx, admin, project.InstallSQL())
	mustMonitorExec(t, ctx, admin, fmt.Sprintf(`
		CREATE ROLE %s NOLOGIN;
		GRANT %s TO CURRENT_USER;
		CREATE SCHEMA %s AUTHORIZATION %s;
		SET ROLE %s;
		CREATE TABLE %s.items (id int NOT NULL);
		INSERT INTO %s.items VALUES (1), (2), (3);
		RESET ROLE;`, appSchema, appSchema, appSchema, appSchema, appSchema, appSchema, appSchema))
	password := monitorRandom(t, 24)
	verifier, err := fixtureSCRAMVerifier(password, []byte(key))
	if err != nil {
		t.Fatal(err)
	}
	mustMonitorExec(t, ctx, admin, "ALTER ROLE "+monitorSchema+" LOGIN PASSWORD '"+verifier+"'")
	monitorConfig := targetConfig.Copy()
	monitorConfig.User, monitorConfig.Password = role, password
	if remote {
		ca, err := os.ReadFile(os.Getenv("POOLTUNE_TEST_ROOKERY_CA"))
		if err != nil {
			t.Fatal(err)
		}
		monitorConfig, err = project.Config(password, ca)
		if err != nil {
			t.Fatal(err)
		}
	}
	monitor := openMonitorConn(t, ctx, monitorConfig)
	policy := SamplingPolicy{Interval: time.Second, Window: time.Second}
	run, err := NewProjectMonitor(monitor, state, project, policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.poll(ctx, time.Now()); !errors.Is(err, ErrProjectDisconnected) {
		t.Fatalf("unregistered monitor should refuse polling, got %v", err)
	}
	if enrollment, err := run.Connect(ctx); err != nil || len(enrollment.Queries) != 0 {
		t.Fatalf("empty registered project: queries=%d err=%v", len(enrollment.Queries), err)
	}

	roleTraffic := openMonitorConn(t, ctx, targetConfig)
	mustMonitorExec(t, ctx, roleTraffic, "SET ROLE "+appSchema)
	slow := "SELECT pg_sleep(0.004) FROM " + appSchema + ".items WHERE id=1"
	fast := "SELECT id FROM " + appSchema + ".items WHERE id=2"
	unenrolled := "SELECT id * id FROM " + appSchema + ".items"
	tickTraffic := func() {
		for _, query := range []string{slow, fast, unenrolled} {
			mustMonitorExec(t, ctx, roleTraffic, query)
		}
	}
	tickTraffic()
	var statsSchema string
	if err := admin.QueryRow(ctx, "SELECT extnamespace::regnamespace::text FROM pg_extension WHERE extname='pg_stat_statements'").Scan(&statsSchema); err != nil {
		t.Fatal(err)
	}
	stats := pgx.Identifier{statsSchema, "pg_stat_statements"}.Sanitize()
	findID := func(prefix string) int64 {
		t.Helper()
		var id int64
		err := admin.QueryRow(ctx, "SELECT queryid FROM "+stats+" WHERE dbid=(SELECT oid FROM pg_database WHERE datname=current_database()) AND userid=$1::regrole AND toplevel AND left(query,length($2))=$2 AND strpos(query,$3)>0", appRole, prefix, appRole).Scan(&id)
		if err != nil || id == 0 {
			t.Fatalf("locate owned query %s: %v", prefix, err)
		}
		return id
	}
	slowID, fastID := findID("SELECT pg_sleep"), findID("SELECT id FROM")
	mustMonitorExec(t, ctx, admin, fmt.Sprintf("INSERT INTO %s.enrolled_queries(slot,role_oid,query_id,mean_execution_ms) VALUES (1,'%s'::regrole,%d,1), (2,'%s'::regrole,%d,50)", monitorSchema, appRole, slowID, appRole, fastID))
	_, samples, err := run.read(ctx)
	if err != nil || len(samples) != 2 {
		t.Fatalf("read exactly two live enrolled query counters: samples=%d err=%v", len(samples), err)
	}
	tx, err := monitor.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	_, isolationErr := tx.Exec(ctx, "SELECT * FROM "+monitorSchema+".snapshot()")
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if monitorSQLState(isolationErr) != "42501" {
		t.Fatalf("monitor reader accepted a repeatable snapshot: %v", isolationErr)
	}
	var canReadAll bool
	if err := monitor.QueryRow(ctx, "SELECT pg_has_role(current_user,'pg_read_all_stats','USAGE')").Scan(&canReadAll); err != nil || canReadAll {
		t.Fatal("monitor must not have cross-role statistics authority outside its reader")
	}
	if _, err := monitor.Exec(ctx, "SELECT * FROM "+appSchema+".items"); monitorSQLState(err) != "42501" {
		t.Fatalf("monitor could read application data: %s", monitorSQLState(err))
	}
	// Exercise the database ACL even when the client read-only default is off.
	mustMonitorExec(t, ctx, monitor, "SET default_transaction_read_only = off")
	if _, err := monitor.Exec(ctx, "DELETE FROM "+monitorSchema+".enrolled_queries WHERE false"); monitorSQLState(err) != "42501" {
		t.Fatalf("monitor could edit enrollments: %s", monitorSQLState(err))
	}
	mustMonitorExec(t, ctx, monitor, "SET default_transaction_read_only = on")
	if _, err := roleTraffic.Exec(ctx, "SELECT * FROM "+monitorSchema+".snapshot()"); monitorSQLState(err) != "42501" {
		t.Fatalf("application role could call private monitor reader: %s", monitorSQLState(err))
	}

	at := time.Now().UTC().Add(2 * time.Second)
	var detected bool
	for i := 0; i <= 5; i++ {
		tickTraffic()
		result, err := run.poll(ctx, at.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Jobs) > 0 {
			if i != 5 || len(result.Jobs) != 1 || result.Jobs[0].Query != slowID {
				t.Fatalf("unexpected jobs from the real detector: %+v", result.Jobs)
			}
			detected = true
		}
		if i == 5 {
			for _, window := range result.Windows {
				t.Logf("query=%d calls=%d mean=%.3f ms decision=%s", window.Query, window.Calls, window.MeanMS, window.Decision)
			}
		}
	}
	if !detected {
		t.Fatal("slow traffic did not queue an investigation")
	}
	if claim, err := claimInvestigation(ctx, state, project.ID()); err != nil || claim != nil {
		t.Fatalf("fixture dispatcher claimed a monitoring-only project: %v", err)
	}
	mustMonitorExec(t, ctx, admin, "UPDATE "+monitorSchema+".enrolled_queries SET mean_execution_ms=2 WHERE slot=1")
	if _, err := run.poll(ctx, at.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	var outcome string
	if err := state.QueryRow(ctx, "SELECT result->>'outcome' FROM pooltune_worker.investigations WHERE project_id=$1", project.ID()).Scan(&outcome); err != nil || outcome != "query_objective_changed" {
		t.Fatalf("retire pending work after objective changes: %q, %v", outcome, err)
	}
	for i := 7; i <= 11; i++ {
		tickTraffic()
		result, err := run.poll(ctx, at.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if i == 11 && (len(result.Jobs) != 1 || result.Jobs[0].Query != slowID) {
			t.Fatalf("expected a new job under the changed objective: %+v", result.Jobs)
		}
	}
	mustMonitorExec(t, ctx, admin, "DELETE FROM "+monitorSchema+".enrolled_queries WHERE slot=1")
	if _, err := run.poll(ctx, at.Add(12*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := state.QueryRow(ctx, "SELECT result->>'outcome' FROM pooltune_worker.investigations WHERE project_id=$1 ORDER BY id DESC LIMIT 1", project.ID()).Scan(&outcome); err != nil || outcome != "query_unenrolled" {
		t.Fatalf("retire pending work on unenrollment: %q, %v", outcome, err)
	}
	mustMonitorExec(t, ctx, admin, "ALTER ROLE "+monitorSchema+" NOLOGIN")
	if _, _, err := run.read(ctx); monitorSQLState(err) != "42501" {
		t.Fatalf("NOLOGIN must stop reads on an existing connection: %v", err)
	}
	if err := DisconnectProjectMonitor(ctx, state, project); err != nil {
		t.Fatal(err)
	}
	if _, err := run.poll(ctx, at.Add(13*time.Second)); !errors.Is(err, ErrProjectDisconnected) {
		t.Fatalf("disconnect must stop subsequent polls before target I/O: %v", err)
	}
	rotatedPassword := monitorRandom(t, 24)
	rotatedVerifier, err := fixtureSCRAMVerifier(rotatedPassword, []byte(monitorRandom(t, 8)))
	if err != nil {
		t.Fatal(err)
	}
	mustMonitorExec(t, ctx, admin, "ALTER ROLE "+monitorSchema+" LOGIN PASSWORD '"+rotatedVerifier+"'")
	if previous, err := pgx.ConnectConfig(ctx, monitorConfig); err == nil {
		previous.Close(ctx)
		t.Fatal("the previous password remained usable after direct rotation")
	} else if monitorSQLState(err) != "28P01" {
		t.Fatalf("expected invalid password after rotation, got %q", monitorSQLState(err))
	}
	rotatedConfig := monitorConfig.Copy()
	rotatedConfig.Password = rotatedPassword
	rotated := openMonitorConn(t, ctx, rotatedConfig)
	next, err := NewProjectMonitor(rotated, state, project, policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := next.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := run.poll(ctx, at.Add(14*time.Second)); !errors.Is(err, ErrProjectDisconnected) {
		t.Fatalf("previous monitor must not resume after reconnect: %v", err)
	}
	if _, err := next.poll(ctx, at.Add(14*time.Second)); err != nil {
		t.Fatal(err)
	}
	if remote {
		if err := monitor.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if err := rotated.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if err := DisconnectProjectMonitor(ctx, state, project); err != nil {
			t.Fatal(err)
		}
		mustMonitorExec(t, ctx, admin, fmt.Sprintf("INSERT INTO %s.enrolled_queries VALUES (1,'%s'::regrole,%d,1)", monitorSchema, appRole, slowID))
		exerciseMonitorCLI(t, ctx, stateConfig, key, rotatedPassword, tickTraffic, slowID, fastID)
		if err := state.QueryRow(ctx, "SELECT result->>'outcome' FROM pooltune_worker.investigations WHERE project_id=$1 ORDER BY id DESC LIMIT 1", project.ID()).Scan(&outcome); err != nil || outcome != "project_disconnected" {
			t.Fatalf("CLI disconnect must retire the open job: %q, %v", outcome, err)
		}
	}
}

func exerciseMonitorCLI(t *testing.T, ctx context.Context, stateConfig *pgx.ConnConfig, key, password string, traffic func(), slowID, fastID int64) {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "pooltune-project")
	env := slices.DeleteFunc(os.Environ(), func(entry string) bool {
		return strings.HasPrefix(entry, "POOLTUNE_TEST_ROOKERY_PASSWORD=")
	})
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../../cmd/pooltune-project")
	build.Env = env
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compile operator command: %v %s", err, output)
	}
	stateURL := url.URL{
		Scheme: "postgres", User: url.UserPassword(stateConfig.User, stateConfig.Password),
		Host: net.JoinHostPort(stateConfig.Host, strconv.Itoa(int(stateConfig.Port))),
		Path: "/" + stateConfig.Database, RawQuery: "sslmode=disable",
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	command := func(operation string, extra ...string) *exec.Cmd {
		args := append([]string{operation, "-ref", "osdjqwpzgyzwfaxyufze", "-key", key}, extra...)
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env = append(slices.Clone(env), "POOLTUNE_PROJECT_PASSWORD="+password,
			"POOLTUNE_PROJECT_CA_FILE="+os.Getenv("POOLTUNE_TEST_ROOKERY_CA"),
			"POOLTUNE_STAGING_PRIVATE_STATE=0", "POOLTUNE_WORKER_STATE_DSN="+stateURL.String())
		cmd.Stderr = os.Stderr
		return cmd
	}
	start := time.Now()
	output, err := command("connect").Output()
	var enrollment Enrollment
	if err != nil || json.Unmarshal(output, &enrollment) != nil || len(enrollment.Queries) != 2 {
		t.Fatalf("verify CLI connect result: %v", err)
	}
	t.Logf("CLI connect verified two query identities in %.3f s", time.Since(start).Seconds())
	watch := command("run", "-interval", "1s", "-window", "1s")
	stdout, err := watch.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	if err := watch.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = watch.Process.Kill()
		_ = watch.Wait()
	}()
	scan := bufio.NewScanner(stdout)
	var detected, healthy bool
	for i := 0; i < 8 && !detected && scan.Scan(); i++ {
		var result PollResult
		if err := json.Unmarshal(scan.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		for _, window := range result.Windows {
			if window.Query == fastID && window.Decision == NoChange {
				healthy = true
			}
		}
		if len(result.Jobs) > 0 {
			if len(result.Jobs) != 1 || result.Jobs[0].Query != slowID {
				t.Fatalf("unexpected CLI investigation jobs: %+v", result.Jobs)
			}
			detected = true
			t.Logf("CLI run queued the slow statement in %.3f s", time.Since(start).Seconds())
		}
		traffic()
	}
	if !detected || !healthy {
		t.Fatalf("CLI did not observe expected traffic: detected=%v healthy=%v scanner=%v", detected, healthy, scan.Err())
	}
	start = time.Now()
	if err := command("disconnect").Run(); err != nil {
		t.Fatal(err)
	}
	if scan.Scan() || scan.Err() != nil {
		t.Fatal("CLI run emitted more polls after disconnect")
	}
	if err := watch.Wait(); err != nil {
		t.Fatal(err)
	}
	t.Logf("CLI disconnect and monitor exit took %.3f s", time.Since(start).Seconds())
}

func openMonitorConn(t *testing.T, ctx context.Context, cfg *pgx.ConnConfig) *pgx.Conn {
	t.Helper()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal("open test database session", monitorSQLState(err))
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

func mustMonitorExec(t *testing.T, ctx context.Context, conn *pgx.Conn, sql string) {
	t.Helper()
	if _, err := conn.Exec(ctx, sql); err != nil {
		t.Fatalf("owned database operation failed (SQLSTATE %s)", monitorSQLState(err))
	}
}

func monitorSQLState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func monitorRandom(t *testing.T, length int) string {
	t.Helper()
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(bytes)
}
