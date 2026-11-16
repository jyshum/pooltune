//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"pooltune/internal/hostedquery"
	"pooltune/internal/labdb"
	"pooltune/internal/supabaseproject"
	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
)

func TestRepairWorkerReportsNativePermissionAndRevocation(t *testing.T) {
	ctx, adminConfig, _ := testpg.Start(t)
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer labdb.Close(admin)
	project, err := supabaseproject.New("abcdefghijklmnopqrst", "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	marker := "Pooltune managed monitor v1 " + project.ID()
	if _, err := admin.Exec(ctx, `CREATE ROLE `+project.Role()+` LOGIN NOINHERIT CONNECTION LIMIT 2;
		CREATE SCHEMA `+project.Role()+`;
		COMMENT ON ROLE `+project.Role()+` IS '`+marker+`';
		COMMENT ON SCHEMA `+project.Role()+` IS '`+marker+`';
		CREATE SCHEMA repair_test;
		CREATE TABLE repair_test.orders (id int);
		GRANT USAGE ON SCHEMA repair_test TO fixture_app;
		GRANT MAINTAIN ON repair_test.orders TO fixture_app;`); err != nil {
		t.Fatal(err)
	}
	target := &hostedquery.ProjectIndexTarget{Schema: "repair_test", Relation: "orders"}
	if err := admin.QueryRow(ctx, `SELECT oid, 'repair_test.orders'::regclass::oid
		FROM pg_database WHERE datname=current_database()`).Scan(&target.DatabaseOID, &target.TableOID); err != nil {
		t.Fatal(err)
	}

	reports := make(chan repairWorkerReport, 2)
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testWorkerKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(repairWorkerPolicy{Project: project.ID(), Epoch: 1, Capabilities: []string{"index", "analyze"}})
			return
		}
		var report repairWorkerReport
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&report); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		reports <- report
		w.WriteHeader(http.StatusNoContent)
	}))
	defer control.Close()
	writer := adminConfig.Copy()
	writer.User = "fixture_app"
	worker, err := newRepairWorker(project, control.URL, testWorkerKey, func(ctx context.Context, scope hostedquery.ProjectRepairAccessScope) (hostedquery.ProjectRepairAccessReport, error) {
		return queryRepairAccess(ctx, project, writer, scope)
	})
	if err != nil {
		t.Fatal(err)
	}
	scope := hostedquery.ProjectRepairAccessScope{Project: project.ID(), Capabilities: []string{"index", "analyze"}, Table: target}
	check := func(analyzeStatus string) {
		t.Helper()
		if state, err := worker.poll(ctx, scope); err != nil || state.Reported != 2 {
			t.Fatalf("worker did not report both checks: %+v, %v", state, err)
		}
		got := <-reports
		want := repairWorkerReport{Epoch: 1, Operations: []repairWorkerOperation{
			{Operation: "index", Status: "database_permission_missing"},
			{Operation: "analyze", Status: analyzeStatus},
		}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	}
	check("granted")
	if _, err := admin.Exec(ctx, "REVOKE MAINTAIN ON repair_test.orders FROM fixture_app"); err != nil {
		t.Fatal(err)
	}
	check("database_permission_missing")
}
