package hostedquery

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTrafficPlanProjection(t *testing.T) {
	now := time.UnixMilli(1794729425828).UTC()
	query := trafficPlanQuery("abcdefghijklmnopqrst", "fixture_reader", -1234567890123456789, now.Add(-time.Minute), now)
	for _, part := range []string{"'db-abcdefghijklmnopqrst'", "'fixture_reader'", "'-1234567890123456789'", "'17212176183586094827'", "'SELECT'", "'postgres'", "'client backend'", "'LOG'", "'Actual Loops'", "LIMIT 4"} {
		if !strings.Contains(query, part) {
			t.Errorf("provider query missing %s", part)
		}
	}
	if strings.Contains(query, "Query Text") || strings.Contains(query, "Query Parameters") {
		t.Fatal("projected statement data")
	}
}

func TestReadTrafficPlanRejectsUnusableSamples(t *testing.T) {
	now := time.UnixMilli(1794729425828).UTC()
	good := `{"captured_at_ms":1794729425827,"node":"Limit","child":"Sort","estimated_rows":"5","actual_rows":"5","loops":"1","filtered_rows":"","shared_hit_blocks":"222","shared_read_blocks":"0","execution_ms":"7.174"}`
	cases := []struct {
		name, row string
		valid     bool
	}{
		{"good", good, true},
		{"old", strings.Replace(good, "1794729425827", "1794729305827", 1), false},
		{"future", strings.Replace(good, "1794729425827", "1794729425829", 1), false},
		{"partial", strings.Replace(good, `"loops":"1"`, `"loops":"2"`, 1), false},
		{"unmeasured", strings.Replace(good, `"actual_rows":"5"`, `"actual_rows":""`, 1), false},
		{"unknown child", strings.Replace(good, `"Sort"`, `"sensitive expression"`, 1), false},
		{"raw", strings.Replace(good, `"node":`, `"query_text":"private","node":`, 1), false},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-token" || r.URL.Path != "/v1/projects/abcdefghijklmnopqrst/analytics/endpoints/logs" {
					t.Error("wrong endpoint or authorization")
				}
				if !strings.Contains(r.URL.Query().Get("sql"), "'fixture_reader'") || r.URL.Query().Get("iso_timestamp_start") == "" || r.URL.Query().Get("iso_timestamp_end") == "" {
					t.Error("unbounded log request")
				}
				fmt.Fprintln(w, `{"result":[`+item.row+`]}`)
			}))
			defer server.Close()
			reader := TrafficPlanReader{client: server.Client(), endpoint: server.URL, token: "test-token"}
			got := reader.read(context.Background(), "abcdefghijklmnopqrst", "fixture_reader", 17, now.Add(-time.Minute), now)
			if (got != nil) != item.valid {
				t.Fatalf("got %#v, want usable=%v", got, item.valid)
			}
			if got != nil && (got.Node != "Limit" || got.Child != "Sort" || got.ActualRows != 5 || got.FilteredRows != nil || got.SharedHits != 222) {
				t.Fatalf("wrong measured plan %#v", got)
			}
		})
	}
}
