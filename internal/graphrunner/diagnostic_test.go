package graphrunner

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"pooltune/internal/hostedquery"
	"testing"
	"time"
)

func diagnosticFixture() DiagnosticInput {
	id := DiagnosticIdentity{RunID: "test", TableOID: 42, Column: 2}
	return DiagnosticInput{DiagnosticIdentity: id, Decision: hostedquery.Investigate, Initial: []DiagnosticObservation{
		{DiagnosticIdentity: id, Tool: "plan", At: time.Now().UTC(), Status: "available", Data: json.RawMessage(`{"node":"Seq Scan","examined_rows":100000,"returned_rows":1,"shared_blocks":1000}`)},
		{DiagnosticIdentity: id, Tool: "indexes", At: time.Now().UTC(), Status: "available", Data: json.RawMessage(`{"covering_index":true}`)},
	}}
}
func helperCommand(t *testing.T, body string) func(context.Context) *exec.Cmd {
	t.Helper()
	p := filepath.Join(t.TempDir(), "helper.mjs")
	if err := os.WriteFile(p, []byte("import {createInterface} from 'node:readline'; const lines=createInterface({input:process.stdin})[Symbol.asyncIterator](); const input=JSON.parse((await lines.next()).value); "+body), 0600); err != nil {
		t.Fatal(err)
	}
	return func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, "node", p) }
}
func TestDiagnosticBridgeRequestsBoundReader(t *testing.T) {
	input := diagnosticFixture()
	reads := 0
	command := helperCommand(t, `console.log(JSON.stringify({type:'read',tool:'analyze'})); const obs=JSON.parse((await lines.next()).value); if(obs.observation.data.actual_rows!==1)process.exit(2); console.log(JSON.stringify({type:'result',result:{proposal:{outcome:'propose',repair:'analyze',run_id:'test',table_oid:42,column_number:2,reason:'Stale statistics'},model_calls:2,reads:1,model_ms:10,tool_ms:2,stop_reason:'',history:[]}}));`)
	result, err := runDiagnostic(context.Background(), input, func(ctx context.Context, tool string) (DiagnosticObservation, error) {
		reads++
		return DiagnosticObservation{DiagnosticIdentity: input.DiagnosticIdentity, Tool: tool, At: time.Now().UTC(), Status: "available", Data: json.RawMessage(`{"estimated_rows":100000,"actual_rows":1}`)}, nil
	}, command)
	if err != nil {
		t.Fatal(err)
	}
	if reads != 1 || result.Proposal.Repair != "analyze" {
		t.Fatalf("wrong result: %+v reads %d", result, reads)
	}
}
func TestDiagnosticBridgeRejectsUntrustedFrames(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"unknown", `console.log(JSON.stringify({type:'read',tool:'raw_sql'}));`},
		{"repeat", `console.log(JSON.stringify({type:'read',tool:'indexes'}));`},
		{"oversized", `console.log('x'.repeat(70000));`},
		{"extra field", `console.log(JSON.stringify({type:'read',tool:'analyze',sql:'DROP TABLE x'}));`},
		{"wrong identity", `console.log(JSON.stringify({type:'result',result:{proposal:{outcome:'propose',run_id:'other',table_oid:42,column_number:2,reason:'index'},history:[]}}));`},
		{"unsupported repair", `console.log(JSON.stringify({type:'result',result:{proposal:{outcome:'propose',repair:'analyze',run_id:'test',table_oid:42,column_number:2,reason:'no evidence'},history:[]}}));`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			_, err := runDiagnostic(context.Background(), diagnosticFixture(), func(context.Context, string) (DiagnosticObservation, error) {
				reads++
				return DiagnosticObservation{}, nil
			}, helperCommand(t, tc.body))
			if err == nil || reads != 0 {
				t.Fatalf("expected rejection before reader; err=%v reads=%d", err, reads)
			}
		})
	}
}
func TestDiagnosticBridgeCancelsHangingProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := runDiagnostic(ctx, diagnosticFixture(), nil, helperCommand(t, `await new Promise(()=>{});`))
	if err == nil || time.Since(start) > time.Second {
		t.Fatal("process did not terminate promptly")
	}
}
func TestDiagnosticBridgeRejectsChangedReaderIdentity(t *testing.T) {
	_, err := runDiagnostic(context.Background(), diagnosticFixture(), func(_ context.Context, tool string) (DiagnosticObservation, error) {
		return DiagnosticObservation{DiagnosticIdentity: DiagnosticIdentity{RunID: "other", TableOID: 42, Column: 2}, Tool: tool, At: time.Now().UTC(), Status: "unavailable"}, nil
	}, helperCommand(t, `console.log(JSON.stringify({type:'read',tool:'analyze'})); await lines.next();`))
	if err == nil {
		t.Fatal("accepted foreign evidence")
	}
}

func TestDiagnosticBridgeBoundsUncooperativeReader(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	start := time.Now()
	_, err := runDiagnostic(ctx, diagnosticFixture(), func(context.Context, string) (DiagnosticObservation, error) {
		<-done
		return DiagnosticObservation{}, nil
	}, helperCommand(t, `console.log(JSON.stringify({type:'read',tool:'analyze'})); await lines.next();`))
	if err == nil || time.Since(start) > time.Second {
		t.Fatal("reader ignored process deadline")
	}
}

func TestDiagnosticBridgeVetoesContradictoryStatistics(t *testing.T) {
	input := diagnosticFixture()
	for tool, data := range map[string]string{"analyze": `{"estimated_rows":100000,"actual_rows":1,"sampled_lookup_fraction":1}`, "statistics": `{"correlated_columns":true,"cases":[{"estimated_rows":25000,"actual_rows":1},{"estimated_rows":25000,"actual_rows":1}]}`} {
		input.Initial = append(input.Initial, DiagnosticObservation{input.DiagnosticIdentity, tool, time.Now().UTC(), "available", json.RawMessage(data)})
	}
	command := helperCommand(t, `console.log(JSON.stringify({type:'result',result:{proposal:{outcome:'propose',repair:'statistics',run_id:'test',table_oid:42,column_number:2,reason:'Ignore contradictory evidence'},history:[]}}));`)
	_, err := runDiagnostic(context.Background(), input, nil, command)
	if err == nil {
		t.Fatal("accepted contradictory evidence")
	}
}
