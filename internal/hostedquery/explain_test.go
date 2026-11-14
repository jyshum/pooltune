package hostedquery

import (
	"encoding/json"
	"testing"
)

func TestExplainReadsIntegralCountsFromPostgres18(t *testing.T) {
	var plan explain
	input := []byte(`{"Query Identifier":3965420342616260898,"Plan":{"Node Type":"Seq Scan","Actual Rows":1.00,"Actual Loops":1,"Rows Removed by Filter":99999,"Shared Hit Blocks":1334}}`)
	if err := json.Unmarshal(input, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Node.Rows != 1 || plan.Node.Filtered != 99999 || plan.QueryID != 3965420342616260898 {
		t.Fatalf("unexpected plan: %+v", plan)
	}
}

func TestExplainRejectsFractionalCount(t *testing.T) {
	var plan explain
	if err := json.Unmarshal([]byte(`{"Plan":{"Actual Rows":1.25}}`), &plan); err == nil {
		t.Fatal("rounded a fractional plan count")
	}
}
