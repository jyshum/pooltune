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

func TestDiagnosticModeRequiresExactlyOneOwnedRepairRoute(t *testing.T) {
	for _, repair := range []int{0, 1, 2, 3, 4, 5, 6} {
		flags := make([]bool, 7)
		flags[repair] = true
		if err := validateInvestigatorMode("diagnostic", "rookery", false, flags...); err != nil {
			t.Errorf("repair %d: %v", repair, err)
		}
		if err := validateInvestigatorMode("diagnostic", "rookery", true, flags...); err == nil {
			t.Fatal("measurement mixed with repair accepted")
		}
		for _, route := range []string{"local", "staging", "rookery-enrolled"} {
			if validateInvestigatorMode("diagnostic", route, false, flags...) == nil {
				t.Fatalf("route %s accepted", route)
			}
		}
	}
	for _, flags := range [][]bool{nil, {false, false}, {true, true}, {true, false, true}} {
		if validateInvestigatorMode("diagnostic", "rookery", false, flags...) == nil {
			t.Fatalf("ambiguous routes accepted: %v", flags)
		}
	}
	if validateInvestigatorMode("unknown", "rookery", false, true) == nil {
		t.Fatal("unknown mode accepted")
	}
	if err := validateInvestigatorMode("scripted", "staging", false); err != nil {
		t.Fatal(err)
	}
}
