package provider

import (
	"fmt"
	"testing"
	"time"
)

func TestGooglePressureRequiresExactInstanceAndSourceTime(t *testing.T) {
	now := time.Now().UTC()
	stamp := now.Add(-time.Minute).Format(time.RFC3339Nano)
	raw := []byte(fmt.Sprintf(`{"timeSeries":[{"metric":{"type":"cloudsql.googleapis.com/database/cpu/utilization"},"resource":{"type":"cloudsql_database","labels":{"project_id":"p","database_id":"p:i"}},"points":[{"interval":{"endTime":%q},"value":{"doubleValue":0.2}}]},{"metric":{"type":"cloudsql.googleapis.com/database/memory/utilization"},"resource":{"type":"cloudsql_database","labels":{"project_id":"p","database_id":"p:i"}},"points":[{"interval":{"endTime":%q},"value":{"doubleValue":0.3}}]}]}`, stamp, stamp))
	p, e := parseGooglePressure(raw, "p", "i", now)
	if e != nil || p.CPU != .2 || p.Memory != .3 || p.At.Format(time.RFC3339Nano) != stamp {
		t.Fatalf("pressure %+v %v", p, e)
	}
	// 60s sampling + up to165s publication delay can produce a valid210s-old sample.
	if _, e = parseGooglePressure(raw, "p", "i", now.Add(150*time.Second)); e != nil {
		t.Fatal("documented publication delay rejected", e)
	}
	if _, e = parseGooglePressure(raw, "p", "other", now); e == nil {
		t.Fatal("wrong instance accepted")
	}
	if _, e = parseGooglePressure(raw, "p", "i", now.Add(4*time.Minute)); e == nil {
		t.Fatal("stale source accepted")
	}
}
