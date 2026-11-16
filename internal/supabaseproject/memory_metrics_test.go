package supabaseproject

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMemoryHeadroomRequiresFreshExactDatabaseMetrics(t *testing.T) {
	ref := "abcdefghijklmnopqrst"
	at := time.Now().UTC()
	body := fmt.Sprintf("node_memory_MemAvailable_bytes{supabase_project_ref=%q,service_type=\"db\"} 8.05306368e+08\nnode_memory_MemTotal_bytes{supabase_project_ref=%q,service_type=\"db\"} 1073741824\n", ref, ref)
	got, err := parseMemoryHeadroom(body, ref, at)
	if err != nil || got.MemoryAllowanceSlots(at) != 3 {
		t.Fatalf("valid snapshot %+v %v", got, err)
	}
	for _, bad := range []string{strings.ReplaceAll(body, ref, "differentprojectrefxx"), body + strings.Split(body, "\n")[0], strings.Replace(body, "8.05306368e+08", "NaN", 1), strings.Replace(body, "8.05306368e+08", "2147483648", 1), strings.ReplaceAll(body, "service_type=\"db\"", "service_type=\"gotrue\"")} {
		if _, err := parseMemoryHeadroom(bad, ref, at); err == nil {
			t.Fatal("invalid metrics admitted")
		}
	}
	got.AvailableBytes = 128 << 20
	if got.MemoryAllowanceSlots(at) != 0 {
		t.Fatal("low memory admitted")
	}
	got.AvailableBytes = 768 << 20
	if got.MemoryAllowanceSlots(at.Add(time.Minute)) != 0 {
		t.Fatal("stale memory admitted")
	}
}

func TestMemoryMetricsReaderBindsTokenAndRejectsStaleResponses(t *testing.T) {
	ref := "abcdefghijklmnopqrst"
	calls := 0
	status := 200
	date := time.Now().UTC()
	age := "0"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer sbp_fc_test_only" {
			t.Error("missing scoped management token")
		}
		if r.Header.Get("Cache-Control") != "no-cache" {
			t.Error("missing freshness request")
		}
		w.Header().Set("Date", date.Format(http.TimeFormat))
		w.Header().Set("Age", age)
		w.WriteHeader(status)
		fmt.Fprintf(w, "node_memory_MemAvailable_bytes{supabase_project_ref=%q,service_type=\"db\"} 805306368\nnode_memory_MemTotal_bytes{supabase_project_ref=%q,service_type=\"db\"} 1073741824\n", ref, ref)
	}))
	defer server.Close()
	reader, err := NewMemoryMetricsReader(ref, "sbp_fc_test_only")
	if err != nil {
		t.Fatal(err)
	}
	reader.endpoint = server.URL
	if _, err := reader.Read(context.Background(), "zyxwvutsrqponmlkjihg"); err == nil || calls != 0 {
		t.Fatal("mismatched project made a credentialed request")
	}
	got, err := reader.Read(context.Background(), ref)
	if err != nil || got.MemoryAllowanceSlots(time.Now()) != 3 {
		t.Fatalf("provider headroom %+v %v", got, err)
	}
	date = time.Now().Add(-time.Minute)
	if _, err := reader.Read(context.Background(), ref); err == nil {
		t.Fatal("stale response admitted")
	}
	date = time.Now()
	age = "60"
	if _, err := reader.Read(context.Background(), ref); err == nil {
		t.Fatal("cached response admitted")
	}
	age = "0"
	status = 403
	if _, err := reader.Read(context.Background(), ref); err == nil {
		t.Fatal("denied token supplied capacity")
	}
}
