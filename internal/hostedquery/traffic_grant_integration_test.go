//go:build integration

package hostedquery

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"pooltune/internal/supabaseproject"
	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
)

func TestTrafficPlanGrantLocal(t *testing.T) {
	ctx, config, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	admin := openMonitorConn(t, ctx, config)
	project, err := supabaseproject.New("abcdefghijklmnopqrst", "1234567890abcdef")
	if err != nil {
		t.Fatal(err)
	}
	owner := project.Role()
	mustMonitorExec(t, ctx, admin, "CREATE EXTENSION pg_stat_statements")
	mustMonitorExec(t, ctx, admin, project.InstallSQL())
	mustMonitorExec(t, ctx, admin, project.TrafficPlanSQL())
	mustMonitorExec(t, ctx, admin, `CREATE ROLE fixture_reader LOGIN PASSWORD 'isolated-test-password'; CREATE TABLE items (x int, y int); GRANT SELECT ON items TO fixture_reader; INSERT INTO items VALUES (1,2); ALTER ROLE `+owner+` LOGIN PASSWORD 'isolated-test-password';`)
	loginConfig := config.Copy()
	loginConfig.User, loginConfig.Password = "fixture_reader", "isolated-test-password"
	app := openMonitorConn(t, ctx, loginConfig)
	mustMonitorExec(t, ctx, app, "SELECT x FROM items WHERE x=1 AND y>1")
	var role, db uint32
	var query int64
	if err := admin.QueryRow(ctx, `SELECT dbid,userid,queryid FROM pg_stat_statements(false) WHERE userid='fixture_reader'::regrole AND toplevel`).Scan(&db, &role, &query); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO `+owner+`.enrolled_queries VALUES (1,$1,$2,1)`, role, query); err != nil {
		t.Fatal(err)
	}
	monitorConfig := config.Copy()
	monitorConfig.User, monitorConfig.Password = owner, "isolated-test-password"
	monitor := openMonitorConn(t, ctx, monitorConfig)
	snapshot, err := readProjectTrafficGrant(ctx, monitor, project, investigationClaim{InvestigationJob: InvestigationJob{Database: db, Role: role, Query: query}}, 1)
	if err != nil || snapshot != nil {
		t.Fatalf("without grant: %v %v", snapshot, err)
	}
	insert := `INSERT INTO ` + owner + `.traffic_plan_grants(role_oid,query_id,context_table,context_column) VALUES ($1,$2,'items'::regclass,1)`
	if _, err := monitor.Exec(ctx, insert, role, query); monitorSQLState(err) != "42501" {
		t.Fatal("monitor inserted scope", err)
	}
	if _, err := admin.Exec(ctx, insert, role, query); err != nil {
		t.Fatal(err)
	}
	if err := monitor.QueryRow(ctx, `SELECT grant_id FROM `+owner+`.read_traffic_plan_grant($1::oid,$2::bigint)`, role, query+1).Scan(new(int64)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("wrong statement admitted", err)
	}
	var claim investigationClaim
	claim.Database, claim.Role, claim.Query = db, role, query
	if err := monitor.QueryRow(ctx, `SELECT ARRAY[boot,reset_at,entry_since] FROM `+owner+`.snapshot()`).Scan(&claim.Generation); err != nil {
		t.Fatal(err)
	}
	snapshot, err = readProjectTrafficGrant(ctx, monitor, project, claim, 1)
	if err != nil || snapshot == nil || snapshot.login != "fixture_reader" || snapshot.tableOID == 0 {
		t.Fatalf("granted: %v %v", snapshot, err)
	}
	mustMonitorExec(t, ctx, admin, `CREATE ROLE switchable; GRANT switchable TO fixture_reader`)
	if got, err := readProjectTrafficGrant(ctx, monitor, project, claim, 1); got != nil || err != nil {
		t.Fatalf("login allowed to SET ROLE disclosed evidence scope: %v %v", got, err)
	}
	mustMonitorExec(t, ctx, admin, `REVOKE switchable FROM fixture_reader`)
	wrong := claim
	wrong.Database++
	if got, err := readProjectTrafficGrant(ctx, monitor, project, wrong, 1); got != nil || err != nil {
		t.Fatalf("wrong database admitted: %v %v", got, err)
	}
	missing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"result":[]}`)
	}))
	defer missing.Close()
	missingLogs := &TrafficPlanReader{client: missing.Client(), endpoint: missing.URL, token: "test-token"}
	unexpectedModel := func(context.Context, string, uint32, int16, time.Time, LookupEvidence, func(context.Context) error) (*LookupDiagnosis, error) {
		t.Fatal("called model without plan evidence")
		return nil, nil
	}
	for _, parent := range []int64{0, 77} {
		attempt := claim
		attempt.ParentID = parent
		result := DispatchResult{Outcome: "insufficient_evidence"}
		err := diagnoseProjectClaim(ctx, nil, monitor, project, 1, attempt,
			&ObservedLookupLatency{ObjectiveMS: 1, Through: time.Now()}, unexpectedModel, missingLogs, &result)
		if err != nil || (result.continuationAt != nil) != (parent == 0) ||
			(result.Outcome == "awaiting_evidence") != (parent == 0) {
			t.Fatalf("expected a single delayed plan read: parent=%d outcome=%s deferred=%v error=%v", parent, result.Outcome, result.continuationAt != nil, err)
		}
	}

	var requested atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		_, err := admin.Exec(ctx, `DELETE FROM `+owner+`.traffic_plan_grants`)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		values := request.URL.Query()
		start, startErr := time.Parse(time.RFC3339Nano, values.Get("iso_timestamp_start"))
		end, endErr := time.Parse(time.RFC3339Nano, values.Get("iso_timestamp_end"))
		capture := end.Truncate(time.Millisecond)
		if startErr != nil || endErr != nil || capture.Before(start) {
			t.Error("test provider could not return a sample inside the requested window")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		requested.Store(true)
		fmt.Fprintf(w, `{"result":[{"captured_at_ms":%d,"node":"Seq Scan","estimated_rows":"1",
			"actual_rows":"1","loops":"1","shared_hit_blocks":"1","shared_read_blocks":"0","execution_ms":"1.500"}]}`, capture.UnixMilli())
	}))
	defer server.Close()
	logs := &TrafficPlanReader{client: server.Client(), endpoint: server.URL, token: "test-token"}
	investigate := func(context.Context, string, uint32, int16, time.Time, LookupEvidence, func(context.Context) error) (*LookupDiagnosis, error) {
		t.Fatal("revoked response reached the graph")
		return nil, nil
	}
	// Provider timestamps have millisecond resolution.
	time.Sleep(2 * time.Millisecond)
	if err := diagnoseProjectClaim(ctx, nil, monitor, project, 1, claim,
		&ObservedLookupLatency{ObjectiveMS: 1, Through: time.Now()}, investigate, logs, &DispatchResult{}); err != nil || !requested.Load() {
		t.Fatalf("expected a revoked capture attempt: %v, requested %v", err, requested.Load())
	}
	if _, err := admin.Exec(ctx, insert, role, query); err != nil {
		t.Fatal(err)
	}
	mustMonitorExec(t, ctx, admin, `DELETE FROM `+owner+`.enrolled_queries`)
	if got, err := readProjectTrafficGrant(ctx, monitor, project, claim, 1); got != nil || err != nil {
		t.Fatalf("withdrawn: %v %v", got, err)
	}
}
