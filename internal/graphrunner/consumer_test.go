package graphrunner

import (
	"context"
	"encoding/json"
	"errors"
	"pooltune/internal/hostedquery"
	"testing"
	"time"
)

// Dropping supplied evidence or rebinding a reader response must prevent repair.
func TestConsumerDiagnosticReusesEvidenceAndRecordsRequestedReads(t *testing.T) {
	e := hostedquery.IndexEvidence{RunID: "worker-9-1", At: time.Now().UTC(), Decision: hostedquery.Investigate, TableOID: 31, Column: 2, Label: "lookup", Plan: hostedquery.PlanEvidence{Node: "Seq Scan", ExaminedRows: 100000, ReturnedRows: 1, SharedBlocks: 1300}}
	graph := func(ctx context.Context, in DiagnosticInput, read DiagnosticReader) (DiagnosticResult, error) {
		if len(in.Initial) != 2 || in.Initial[0].Tool != "plan" || in.Initial[1].Tool != "indexes" {
			t.Fatal("supplied evidence was hidden")
		}
		o, err := read(ctx, "analyze")
		if err != nil {
			return DiagnosticResult{}, err
		}
		var a hostedquery.AnalyzeEvidence
		if json.Unmarshal(o.Data, &a) != nil || a.EstimatedRows != 1 || o.TableOID != 31 {
			t.Fatal("wrong reader observation")
		}
		u, err := read(ctx, "cron")
		if err != nil || u.Status != "unavailable" || len(u.Data) != 0 {
			t.Fatal("unavailable evidence became a negative measurement")
		}
		return DiagnosticResult{Proposal: hostedquery.IndexProposal{RunID: in.RunID, TableOID: in.TableOID, Column: in.Column, Outcome: "propose", Reason: "selective scan without index"}, ModelCalls: 2, ModelAttempts: 2, Reads: 2, ModelMS: 8000, ToolMS: 10, TotalMS: 8300, StopReason: "proposed", History: []json.RawMessage{json.RawMessage(`{"action":"propose"}`)}}, nil
	}
	read := func(ctx context.Context, tool string, base hostedquery.IndexEvidence) (hostedquery.IndexEvidence, bool, error) {
		if tool != "analyze" {
			return hostedquery.IndexEvidence{}, false, nil
		}
		base.Analyze = &hostedquery.AnalyzeEvidence{EstimatedRows: 1, ActualRows: 1}
		return base, true, nil
	}
	p, err := AdaptDiagnostic(graph, read)(context.Background(), e)
	if err != nil || p.Outcome != "propose" || p.Diagnostic == nil || p.Diagnostic.ModelAttempts != 2 || p.Diagnostic.Reads != 2 || p.Diagnostic.ModelMS != 8000 || len(p.Diagnostic.History) != 1 {
		t.Fatalf("lost diagnostic result: %+v %v", p, err)
	}
}

func TestConsumerDiagnosticRejectsChangedReaderIdentity(t *testing.T) {
	e := hostedquery.IndexEvidence{RunID: "worker-9-1", At: time.Now().UTC(), Decision: hostedquery.Investigate, TableOID: 31, Column: 2}
	graph := func(ctx context.Context, in DiagnosticInput, read DiagnosticReader) (DiagnosticResult, error) {
		_, err := read(ctx, "analyze")
		return DiagnosticResult{}, err
	}
	read := func(ctx context.Context, tool string, base hostedquery.IndexEvidence) (hostedquery.IndexEvidence, bool, error) {
		base.TableOID++
		base.Analyze = &hostedquery.AnalyzeEvidence{EstimatedRows: 1, ActualRows: 1}
		return base, true, nil
	}
	if _, err := AdaptDiagnostic(graph, read)(context.Background(), e); err == nil {
		t.Fatal("changed table accepted")
	}
}
func TestConsumerDiagnosticPreservesFailures(t *testing.T) {
	want := errors.New("evidence denied")
	graph := func(context.Context, DiagnosticInput, DiagnosticReader) (DiagnosticResult, error) {
		return DiagnosticResult{}, want
	}
	if _, err := AdaptDiagnostic(graph, nil)(context.Background(), hostedquery.IndexEvidence{}); !errors.Is(err, want) {
		t.Fatalf("lost failure: %v", err)
	}
}
