package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadLookupEnrollment(t *testing.T) {
	valid := `{"project":"staging/rookery/test","lookup":{"schema":"fixture","table":"lookup_test","column":"lookup","table_oid":12,"database_oid":1,"role_oid":2,"query_id":"-9223372036854775807"},"input":0}`
	path := filepath.Join(t.TempDir(), "lookup.json")
	for _, test := range []struct {
		name, input string
		valid       bool
	}{
		{"integer OIDs and exact statement ID", valid, true},
		{"quoted relation ID", strings.Replace(valid, `"table_oid":12`, `"table_oid":"12"`, 1), false},
		{"missing sample value", strings.Replace(valid, `,"input":0`, "", 1), false},
		{"unexpected SQL", strings.Replace(valid, `"input":0`, `"input":0,"sql":"SELECT 1"`, 1), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(test.input), 0600); err != nil {
				t.Fatal(err)
			}
			enrollment, err := readLookupEnrollment(path)
			if test.valid {
				if err != nil || enrollment.Lookup.Query != -9223372036854775807 || *enrollment.Input != 0 {
					t.Fatalf("enrollment=%+v err=%v", enrollment, err)
				}
			} else if err == nil {
				t.Fatal("invalid enrollment was accepted")
			}
		})
	}
}

func TestDiagnosticModeRequiresTheQualifiedIndexRoute(t *testing.T) {
	for _, tc := range []struct {
		mode, route  string
		index, other bool
		ok           bool
	}{
		{"diagnostic", "rookery", true, false, true},
		{"diagnostic", "rookery", false, false, false},
		{"diagnostic", "rookery", true, true, false},
		{"diagnostic", "rookery-enrolled", true, false, false},
		{"diagnostic", "local", true, false, false},
		{"unknown", "rookery", true, false, false},
		{"anthropic", "rookery", true, false, true},
		{"scripted", "staging", false, false, true},
	} {
		if err := validateInvestigatorMode(tc.mode, tc.route, tc.index, tc.other); (err == nil) != tc.ok {
			t.Errorf("%+v: %v", tc, err)
		}
	}
}
