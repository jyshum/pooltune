package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"pooltune/internal/hostedquery"
	"pooltune/internal/supabaseproject"
)

const testWorkerKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestRepairWorkerReportsOnlyMutuallySelectedCapabilities(t *testing.T) {
	project, _ := supabaseproject.New("osdjqwpzgyzwfaxyufze", "123456789abcdef0")
	t.Setenv("POOLTUNE_REPAIR_DB_PASSWORD", "owner-password-must-not-travel")
	var received string
	var path, authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, authorization = r.URL.Path, r.Header.Get("Authorization")
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"project": project.ID(), "epoch": 3, "capabilities": []string{"index", "cron"}, "paused": false})
			return
		}
		body, _ := io.ReadAll(r.Body)
		received = string(body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	worker, err := newRepairWorker(project, server.URL, testWorkerKey, func(_ context.Context, scope hostedquery.ProjectRepairAccessScope) (hostedquery.ProjectRepairAccessReport, error) {
		if !reflect.DeepEqual(scope.Capabilities, []string{"index"}) || scope.Table == nil || scope.Table.Relation != "private_table" {
			t.Fatalf("checker received unexpected scope: %+v", scope)
		}
		return hostedquery.ProjectRepairAccessReport{Project: project.ID(), Operations: []hostedquery.ProjectRepairAccess{
			{Operation: "index", Capability: "index", Requirement: "unused local data", Status: "granted"},
			{Operation: "analyze", Capability: "analyze", Status: "not_selected"},
			{Operation: "cron", Capability: "cron", Status: "not_selected"},
		}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	scope := hostedquery.ProjectRepairAccessScope{Project: project.ID(), Capabilities: []string{"index", "analyze"}, Table: &hostedquery.ProjectIndexTarget{Relation: "private_table"}}
	if _, err := worker.poll(context.Background(), scope); err != nil {
		t.Fatal(err)
	}
	if path != "/api/projects/123456789abcdef0/repair-worker" || authorization != "Bearer "+testWorkerKey {
		t.Fatalf("unexpected request destination or auth: %q", path)
	}
	if received != `{"epoch":3,"operations":[{"operation":"index","status":"granted"}]}` {
		t.Fatalf("unexpected report body: %s", received)
	}
	if !reflect.DeepEqual(scope.Capabilities, []string{"index", "analyze"}) {
		t.Fatal("worker changed its configured local capability list")
	}
}

func TestRepairWorkerRejectsInvalidOriginsAndRedirects(t *testing.T) {
	project, _ := supabaseproject.New("osdjqwpzgyzwfaxyufze", "123456789abcdef0")
	for _, origin := range []string{"http://example.com", "//example.com", "https://login@example.com", "https://example.com/path", "https://example.com?next=other", "https://example.com/#fragment"} {
		if _, err := newRepairWorker(project, origin, testWorkerKey, nil); err == nil {
			t.Errorf("accepted unsafe origin %q", origin)
		}
	}
	var redirected atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	worker, err := newRepairWorker(project, server.URL, testWorkerKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.poll(context.Background(), hostedquery.ProjectRepairAccessScope{Project: project.ID()}); err == nil || redirected.Load() != 0 {
		t.Fatalf("unexpected redirect behavior: %v, %d requests", err, redirected.Load())
	}
}

func TestRepairWorkerRechecksPolicyAfterConflictAndSkipsPausedDatabase(t *testing.T) {
	project, _ := supabaseproject.New("osdjqwpzgyzwfaxyufze", "123456789abcdef0")
	var reads, posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			epoch := reads.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"project": project.ID(), "epoch": epoch, "capabilities": []string{"index"}, "paused": epoch == 2})
			return
		}
		posts.Add(1)
		w.WriteHeader(http.StatusConflict)
	}))
	defer server.Close()
	checks := 0
	worker, err := newRepairWorker(project, server.URL, testWorkerKey, func(_ context.Context, _ hostedquery.ProjectRepairAccessScope) (hostedquery.ProjectRepairAccessReport, error) {
		checks++
		return hostedquery.ProjectRepairAccessReport{Project: project.ID()}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	scope := hostedquery.ProjectRepairAccessScope{Project: project.ID(), Capabilities: []string{"index"}}
	if _, err := worker.poll(context.Background(), scope); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("expected policy change, got %v", err)
	}
	if state, err := worker.poll(context.Background(), scope); err != nil || !state.Paused || state.Epoch != 2 {
		t.Fatalf("expected fresh paused state, got %+v, %v", state, err)
	}
	if checks != 1 || reads.Load() != 2 || posts.Load() != 1 {
		t.Fatalf("checks=%d, reads=%d, posts=%d", checks, reads.Load(), posts.Load())
	}
}

func TestRepairWorkerRejectsAnotherInstallationPolicy(t *testing.T) {
	project, _ := supabaseproject.New("osdjqwpzgyzwfaxyufze", "123456789abcdef0")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"project": "other-installation", "epoch": 1, "capabilities": []string{"index"}, "paused": false})
	}))
	defer server.Close()
	worker, err := newRepairWorker(project, server.URL, testWorkerKey, func(_ context.Context, _ hostedquery.ProjectRepairAccessScope) (hostedquery.ProjectRepairAccessReport, error) {
		t.Fatal("unexpected database check for a different installation")
		return hostedquery.ProjectRepairAccessReport{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.poll(context.Background(), hostedquery.ProjectRepairAccessScope{Project: project.ID()}); err == nil {
		t.Fatal("admitted policy for a different installation")
	}
}

func TestRepairWorkerReportsEmptyIntersectionAndStopsOnRevocation(t *testing.T) {
	project, _ := supabaseproject.New("osdjqwpzgyzwfaxyufze", "123456789abcdef0")
	var revoked atomic.Bool
	bodies := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if revoked.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"project": project.ID(), "epoch": 2, "capabilities": []string{"cron"}, "paused": false})
			return
		}
		body, _ := io.ReadAll(r.Body)
		bodies <- string(body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	worker, err := newRepairWorker(project, server.URL, testWorkerKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := hostedquery.ProjectRepairAccessScope{Project: project.ID(), Capabilities: []string{"index"}}
	if state, err := worker.poll(context.Background(), scope); err != nil || state.Reported != 0 || state.Paused {
		t.Fatalf("unexpected empty intersection: %+v, %v", state, err)
	}
	if body := <-bodies; body != `{"epoch":2,"operations":[]}` {
		t.Fatalf("unexpected report body: %s", body)
	}
	revoked.Store(true)
	if _, err := worker.poll(context.Background(), scope); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("expected key rejection, got %v", err)
	}
}
