package provider

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func metricsFixture(stamp int64, idle, busy float64) []byte {
	s := fmt.Sprintf("node_time_seconds{supabase_project_ref=\"project\",supabase_identifier=\"project\",service_type=\"db\"} %d\nnode_memory_MemTotal_bytes{supabase_project_ref=\"project\",supabase_identifier=\"project\",service_type=\"db\"} 1000\nnode_memory_MemAvailable_bytes{supabase_project_ref=\"project\",supabase_identifier=\"project\",service_type=\"db\"} 600\n", stamp)
	for _, mode := range []string{"idle", "iowait", "irq", "nice", "softirq", "steal", "system", "user"} {
		v := 0.0
		if mode == "idle" {
			v = idle
		}
		if mode == "user" {
			v = busy
		}
		s += fmt.Sprintf("node_cpu_seconds_total{supabase_project_ref=\"project\",supabase_identifier=\"project\",service_type=\"db\",cpu=\"0\",mode=\"%s\"} %g\n", mode, v)
	}
	return []byte(s)
}

// A reader using HTTP receipt time or laptop CPU would wrongly pass stale cloud evidence.
func TestSupabasePressureKeepsSourceTimeAndRequiresTwoCompleteSnapshots(t *testing.T) {
	now := time.Unix(1000, 0)
	var p pressureHistory
	if _, e := p.supabase(metricsFixture(930, 100, 100), "project", now); e == nil {
		t.Fatal("single counter snapshot was sufficient")
	}
	r, e := p.supabase(metricsFixture(990, 148, 112), "project", now)
	if e != nil || r.CPU < .199 || r.CPU > .201 || r.Memory != .4 || !r.At.Equal(time.Unix(990, 0)) {
		t.Fatalf("bad real deltas/source time: %+v %v", r, e)
	}
	if _, e = p.supabase(metricsFixture(990, 148, 112), "project", now.Add(90*time.Second)); e == nil {
		t.Fatal("cached source became fresh on receipt")
	}
}
func TestSupabasePressureRejectsWrongScopeMissingModeAndCounterReset(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, bad := range [][]byte{[]byte(strings.ReplaceAll(string(metricsFixture(990, 148, 112)), `supabase_identifier="project"`, `supabase_identifier="replica"`)), []byte(strings.ReplaceAll(string(metricsFixture(990, 148, 112)), `mode="steal"`, `mode="other"`)), metricsFixture(990, 90, 112)} {
		var p pressureHistory
		p.supabase(metricsFixture(930, 100, 100), "project", now)
		if _, e := p.supabase(bad, "project", now); e == nil {
			t.Fatal("unsafe metrics accepted")
		}
	}
}
func TestAzurePressureUsesOldestSourceTimestampAndRejectsMissingMetric(t *testing.T) {
	now := time.Date(2026, 10, 25, 1, 2, 0, 0, time.UTC)
	raw := []byte(`{"value":[{"name":{"value":"cpu_percent"},"timeseries":[{"data":[{"timeStamp":"2026-10-25T01:01:00Z","maximum":20}]}]},{"name":{"value":"memory_percent"},"timeseries":[{"data":[{"timeStamp":"2026-10-25T01:00:00Z","maximum":30}]}]}]}`)
	r, e := parseAzurePressure(raw, now)
	if e != nil || r.CPU != .2 || r.Memory != .3 || !r.At.Equal(now.Add(-2*time.Minute)) {
		t.Fatalf("wrong pressure: %+v %v", r, e)
	}
	if _, e = parseAzurePressure(raw, now.Add(16*time.Second)); e == nil {
		t.Fatal("stale pressure accepted")
	}
	if _, e = parseAzurePressure([]byte(`{"value":[]}`), now); e == nil {
		t.Fatal("missing metrics treated as zero")
	}
}

func TestAzurePressureAllowsPublicationAndPollButStillExpires(t *testing.T) {
	now := time.Date(2026, 10, 25, 9, 39, 5, 0, time.UTC)
	raw := []byte(`{"value":[{"name":{"value":"cpu_percent"},"timeseries":[{"data":[{"timeStamp":"2026-10-25T09:37:00Z","maximum":20}]}]},{"name":{"value":"memory_percent"},"timeseries":[{"data":[{"timeStamp":"2026-10-25T09:37:00Z","maximum":30}]}]}]}`)
	p, e := parseAzurePressure(raw, now)
	if e != nil || !p.At.Equal(now.Add(-125*time.Second)) {
		t.Fatalf("normal publication/poll delay rejected or restamped: %+v %v", p, e)
	}
	if _, e = parseAzurePressure(raw, now.Add(11*time.Second)); e == nil {
		t.Fatal("source beyond bounded 135s window accepted")
	}
}
