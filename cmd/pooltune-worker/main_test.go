package main

import (
	"strings"
	"testing"
)

func TestEnrollmentPreservesLargeQueryIDs(t *testing.T) {
	raw := `{"project":"test","queries":[{"database_oid":1,"role_oid":2,"query_id":"-9223372036854775807","mean_execution_ms":1}]}`
	enrollment, err := readEnrollment(strings.NewReader(raw))
	if err != nil || enrollment.Queries[0].Query != -9223372036854775807 {
		t.Fatalf("enrollment = %+v, error = %v", enrollment, err)
	}
	for _, invalid := range []string{raw + " {}", strings.Replace(raw, "mean_execution_ms", "p95", 1), strings.Repeat(" ", 33<<10) + raw} {
		if _, err := readEnrollment(strings.NewReader(invalid)); err == nil {
			t.Fatal("invalid or oversized enrollment was accepted")
		}
	}
}
