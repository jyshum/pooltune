//go:build integration

package hostedquery

import (
	"context"
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
)

func TestProjectLookupGrantLocal(t *testing.T) {
	ctx, config, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	admin := openMonitorConn(t, ctx, config)
	mustMonitorExec(t, ctx, admin, "CREATE EXTENSION pg_stat_statements")
	testProjectLookupGrant(t, ctx, admin, config, false)
}

func TestProjectLookupSonnetRookery(t *testing.T) {
	password := os.Getenv("POOLTUNE_TEST_ROOKERY_PASSWORD")
	if password == "" || os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("Rookery and Sonnet qualification credentials unavailable")
	}
	ca, err := os.ReadFile(os.Getenv("POOLTUNE_TEST_ROOKERY_CA"))
	if err != nil {
		t.Fatal("Rookery CA unavailable")
	}
	config, err := supabaseproject.DirectConfig("osdjqwpzgyzwfaxyufze", "postgres", password, ca)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin := openMonitorConn(t, ctx, config)
	testProjectLookupGrant(t, ctx, admin, config, true)
}

func testProjectLookupGrant(t *testing.T, ctx context.Context, admin *pgx.Conn, config *pgx.ConnConfig, remote bool) {
	t.Helper()
	project, err := supabaseproject.New("osdjqwpzgyzwfaxyufze", monitorRandom(t, 8))
	if err != nil {
		t.Fatal(err)
	}
	testLookupFlow(t, ctx, project, admin, config, remote)
}

func testLookupFlow(t *testing.T, ctx context.Context, project supabaseproject.Project, admin *pgx.Conn, config *pgx.ConnConfig, remote bool) {
	t.Helper()
	key := strings.TrimPrefix(project.Role(), "pt_monitor_")
	appRole := "pt_lookup_app_" + key
	app := pgx.Identifier{appRole}.Sanitize()
	scope := pgx.Identifier{project.Role()}.Sanitize()
	table := app + ".items"
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		conn, err := pgx.ConnectConfig(cleanup, config)
		if err != nil {
			t.Error("own lookup cleanup connection failed")
			return
		}
		defer conn.Close(cleanup)
		_, err = conn.Exec(cleanup, fmt.Sprintf(`
			DROP FUNCTION IF EXISTS %s.read_lookup(oid,bigint);
			DROP FUNCTION IF EXISTS %s.allow_lookup(oid,bigint,regclass,name);
			DROP TABLE IF EXISTS %s.lookup_grants;
			DROP FUNCTION IF EXISTS %s.snapshot();
			DROP TABLE IF EXISTS %s.enrolled_queries;
			DROP SCHEMA IF EXISTS %s;
			DROP TABLE IF EXISTS %s;
			DROP SCHEMA IF EXISTS %s;
			DO $$ BEGIN IF EXISTS(SELECT FROM pg_roles WHERE rolname='%s')
				THEN REVOKE CONNECT ON DATABASE postgres FROM %s; END IF; END $$;
			DROP ROLE IF EXISTS %s;
			DROP ROLE IF EXISTS %s;`,
			scope, scope, scope, scope, scope, scope, table, app, project.Role(), scope, scope, app))
		if err != nil {
			t.Error("owned lookup cleanup", monitorSQLState(err))
		}
		var remains bool
		if err := conn.QueryRow(cleanup, "SELECT to_regnamespace($1) IS NOT NULL OR to_regnamespace($2) IS NOT NULL OR EXISTS(SELECT FROM pg_roles WHERE rolname IN ($1,$2))", project.Role(), appRole).Scan(&remains); err != nil || remains {
			t.Error("lookup fixture cleanup readback failed")
		}
	})

	mustMonitorExec(t, ctx, admin, project.InstallSQL())
	mustMonitorExec(t, ctx, admin, project.LookupEvidenceSQL())
	mustMonitorExec(t, ctx, admin, fmt.Sprintf(`
		CREATE ROLE %s NOLOGIN;
		GRANT %s TO CURRENT_USER;
		CREATE SCHEMA %s AUTHORIZATION %s;
		SET ROLE %s;
		CREATE TABLE %s (lookup integer NOT NULL);
		INSERT INTO %s SELECT n FROM generate_series(1,100000)n;
		ANALYZE %s;
		RESET ROLE;`, app, app, app, app, app, table, table, table))

	traffic := openMonitorConn(t, ctx, config)
	mustMonitorExec(t, ctx, traffic, "SET ROLE "+app)
	// Use the exact supported spelling. The SQL identifier names are generated
	// simple names, so PostgreSQL's format(%I) normalizes them without quotes.
	query := "SELECT lookup FROM " + appRole + ".items WHERE lookup = $1"
	executeLookup := func(input int32) {
		t.Helper()
		var got int32
		if err := traffic.QueryRow(ctx, query, input).Scan(&got); err != nil || got != input {
			t.Fatalf("owned app lookup failed: %v", err)
		}
	}
	executeLookup(17)
	mustMonitorExec(t, ctx, traffic, "SELECT count(*) FROM "+appRole+".items")
	var role, database uint32
	var statsSchema string
	if err := admin.QueryRow(ctx, `SELECT r.oid,d.oid,e.extnamespace::regnamespace::text
		FROM pg_roles r,pg_database d,pg_extension e WHERE r.rolname=$1 AND d.datname=current_database() AND e.extname='pg_stat_statements'`, appRole).Scan(&role, &database, &statsSchema); err != nil {
		t.Fatal(err)
	}
	stats := pgx.Identifier{statsSchema, "pg_stat_statements"}.Sanitize()
	find := func(statement string) int64 {
		t.Helper()
		var id int64
		if err := admin.QueryRow(ctx, "SELECT queryid FROM "+stats+" WHERE dbid=$1 AND userid=$2 AND query=$3 AND toplevel", database, role, statement).Scan(&id); err != nil {
			t.Fatal("discover enrolled identity", err)
		}
		return id
	}
	queryID := find(query)
	otherID := find("SELECT count(*) FROM " + appRole + ".items")
	mustMonitorExec(t, ctx, admin, fmt.Sprintf("INSERT INTO %s.enrolled_queries VALUES (1,%d,%d,0.1),(2,%d,%d,100)", scope, role, queryID, role, otherID))
	grant := "SELECT " + scope + ".allow_lookup($1::oid,$2::bigint,$3::regclass,'lookup')"
	if _, err := admin.Exec(ctx, grant, role, otherID, appRole+".items"); monitorSQLState(err) != "P0001" {
		t.Fatal("allowed a different statement shape", monitorSQLState(err))
	}
	password := monitorRandom(t, 24)
	verifier, err := fixtureSCRAMVerifier(password, []byte(key))
	if err != nil {
		t.Fatal(err)
	}
	mustMonitorExec(t, ctx, admin, "ALTER ROLE "+scope+" LOGIN PASSWORD '"+verifier+"'")
	monitorConfig := config.Copy()
	if remote {
		ca, err := os.ReadFile(os.Getenv("POOLTUNE_TEST_ROOKERY_CA"))
		if err != nil {
			t.Fatal("Rookery CA unavailable")
		}
		monitorConfig, err = project.Config(password, ca)
		if err != nil {
			t.Fatal(err)
		}
	}
	monitorConfig.User, monitorConfig.Password = project.Role(), password
	monitor := openMonitorConn(t, ctx, monitorConfig)
	read := "SELECT grant_id FROM " + scope + ".read_lookup($1::oid,$2::bigint)"
	var grantID int64
	if err := monitor.QueryRow(ctx, read, role, queryID).Scan(&grantID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("lookup data available without its explicit grant", monitorSQLState(err))
	}
	if _, err := monitor.Exec(ctx, grant, role, queryID, appRole+".items"); monitorSQLState(err) != "42501" {
		t.Fatal("monitor could grant its own diagnosis", monitorSQLState(err))
	}
	if _, err := admin.Exec(ctx, grant, role, queryID, appRole+".items"); err != nil {
		t.Fatal("approve lookup", err)
	}
	if err := monitor.QueryRow(ctx, read, role, queryID).Scan(&grantID); err != nil || grantID <= 0 {
		t.Fatal("grant was not visible to monitor", err)
	}

	_, stateConfig, _ := testpg.Start(t)
	state := openMonitorConn(t, ctx, stateConfig)
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	policy := SamplingPolicy{Interval: time.Second, Window: time.Second}
	worker, err := NewProjectMonitor(monitor, state, project, policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-8 * time.Second)
	var jobID int64
	for n := 0; n <= 5; n++ {
		executeLookup(int32(n + 100))
		result, err := worker.poll(ctx, at.Add(time.Duration(n)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Jobs) > 0 {
			if n != 5 || len(result.Jobs) != 1 || result.Jobs[0].Query != queryID {
				t.Fatal("unexpected managed jobs", result.Jobs)
			}
			jobID = result.Jobs[0].ID
		}
	}
	if jobID == 0 {
		t.Fatal("detector did not queue the owned lookup")
	}
	if claim, err := claimInvestigation(ctx, state, project.ID()); err != nil || claim != nil {
		t.Fatal("fixture route claimed the enrolled lookup", err)
	}
	if !remote {
		var attempts int
		result, err := DiagnoseProjectLookup(ctx, state, monitor, project, worker.epoch,
			func(ctx context.Context, _ string, _ uint32, _ int16, _ time.Time, evidence LookupEvidence, admit func(context.Context) error) (*LookupDiagnosis, error) {
				attempts++
				if evidence.LeadingBTree || evidence.TablePages < 300 || len(evidence.SharedBlockAccesses) != 5 {
					t.Fatalf("unexpected approved lookup evidence: %+v", evidence)
				}
				if err := admit(ctx); err != nil {
					return nil, err
				}
				mustMonitorExec(t, ctx, admin, fmt.Sprintf("DELETE FROM %s.lookup_grants WHERE grant_id=%d", scope, grantID))
				return &LookupDiagnosis{Reason: "source revoked after model admission"}, nil
			})
		if err != nil || attempts != 1 || result == nil || result.Outcome != "insufficient_evidence" || result.LookupDiagnosis != nil || result.ThreadID != "" {
			t.Fatalf("revoked result should not publish: attempts=%d result=%+v err=%v", attempts, result, err)
		}
		return
	}
	readCalls := func() int64 {
		t.Helper()
		var calls int64
		if err := admin.QueryRow(ctx, "SELECT calls FROM "+stats+"(false) WHERE dbid=$1 AND userid=$2 AND queryid=$3 AND toplevel", database, role, queryID).Scan(&calls); err != nil {
			t.Fatal(err)
		}
		return calls
	}
	before := readCalls()
	result := runLookupDiagnosisCLI(t, ctx, stateConfig, key, password)
	if result.JobID != jobID || result.Outcome != "diagnosed" || result.LookupDiagnosis == nil || result.Proposal != nil || result.ObservedLatency == nil {
		t.Fatalf("unexpected live diagnosis: %+v", result)
	}
	var causes []struct{ Cause, Assessment string }
	if err := json.Unmarshal(result.LookupDiagnosis.Assessments, &causes); err != nil || !slices.ContainsFunc(causes, func(c struct{ Cause, Assessment string }) bool {
		return c.Cause == "index" && c.Assessment == "supported"
	}) {
		t.Fatalf("Sonnet did not identify missing index as the leading hypothesis: %+v", result.LookupDiagnosis)
	}
	if readCalls() != before {
		t.Fatal("diagnosis replayed the application statement")
	}
	var saved string
	if err := state.QueryRow(ctx, "SELECT result->>'outcome' FROM pooltune_worker.investigations WHERE id=$1", jobID).Scan(&saved); err != nil || saved != "diagnosed" {
		t.Fatal("diagnosis was not saved", err)
	}
	t.Logf("Sonnet %s took %.3f ms; diagnosis %.3f ms; calls=%d; workload means=%v; page accesses=%v; reason=%s",
		result.LookupDiagnosis.Model, result.LookupDiagnosis.ModelMS, result.ElapsedMS, before,
		result.ObservedLatency.WindowMeansMS, result.ObservedLatency.SharedBlockAccesses, result.LookupDiagnosis.Reason)
}

func runLookupDiagnosisCLI(t *testing.T, ctx context.Context, state *pgx.ConnConfig, key, password string) DispatchResult {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "pooltune-project")
	env := slices.DeleteFunc(os.Environ(), func(entry string) bool { return strings.HasPrefix(entry, "POOLTUNE_TEST_ROOKERY_PASSWORD=") })
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../../cmd/pooltune-project")
	build.Env = env
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build operator command: %v %s", err, output)
	}
	stateURL := url.URL{Scheme: "postgres", User: url.UserPassword(state.User, state.Password), Host: net.JoinHostPort(state.Host, strconv.Itoa(int(state.Port))), Path: "/" + state.Database, RawQuery: "sslmode=disable"}
	cmd := exec.CommandContext(ctx, binary, "diagnose", "-ref", "osdjqwpzgyzwfaxyufze", "-key", key)
	cmd.Dir = "../.."
	cmd.Env = append(env, "POOLTUNE_PROJECT_PASSWORD="+password, "POOLTUNE_PROJECT_CA_FILE="+os.Getenv("POOLTUNE_TEST_ROOKERY_CA"),
		"POOLTUNE_STAGING_PRIVATE_STATE=0", "POOLTUNE_WORKER_STATE_DSN="+stateURL.String())
	cmd.Stderr = os.Stderr
	output, err := cmd.Output()
	var result DispatchResult
	if err != nil || json.Unmarshal(output, &result) != nil {
		t.Fatalf("operator diagnosis failed: %v", err)
	}
	return result
}

func TestLookupEvidenceRejectsUnsafeSequenceDefaults(t *testing.T) {
	ctx, config, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	admin := openMonitorConn(t, ctx, config)
	mustMonitorExec(t, ctx, admin, "CREATE EXTENSION pg_stat_statements")
	project, err := supabaseproject.New("osdjqwpzgyzwfaxyufze", monitorRandom(t, 8))
	if err != nil {
		t.Fatal(err)
	}
	mustMonitorExec(t, ctx, admin, project.InstallSQL())
	mustMonitorExec(t, ctx, admin, "ALTER DEFAULT PRIVILEGES GRANT UPDATE ON SEQUENCES TO PUBLIC")
	if _, err := admin.Exec(ctx, project.LookupEvidenceSQL()); monitorSQLState(err) != "P0001" {
		t.Fatalf("unsafe sequence privileges should prevent installation: %v", err)
	}
}
