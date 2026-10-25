package agent

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"pooltune/internal/gateway"
	"testing"
	"time"
)

type noActionModel struct{}

func (noActionModel) Infer(context.Context, gateway.ModelRequest) (gateway.ModelResponse, error) {
	return gateway.ModelResponse{Done: true, Usage: gateway.Usage{InputTokens: 1, OutputTokens: 1, Known: true}}, nil
}
func TestActualGatewayHTTPRoundTrip(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	store, e := gateway.Open(filepath.Join(dir, "gateway.db"), gateway.Limits{GlobalMicros: 100, MaxConcurrent: 1, MaxAccountConcurrent: 1, RequestsPerMinute: 10, MaxRunSteps: 8, RunTTL: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	token, e := store.Approve("fixture", 100, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	profile := gateway.Profile{ID: "fixture", Model: "fixture", Verified: true, MaxInputTokens: 32768, MaxOutputTokens: 100, MaxPayloadBytes: 8192, InputMicrosPerMillion: 1, OutputMicrosPerMillion: 1, Timeout: time.Second}
	handler, e := gateway.NewHandler(&gateway.Service{Store: store, Model: noActionModel{}, Profile: profile}, true)
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client, e := NewClient(server.URL, func() (string, error) { return token, nil }, time.Second, true)
	if e != nil {
		t.Fatal(e)
	}
	r, _, _ := agentFixture(t)
	r.gateway = client
	out, e := r.Run(t.Context(), Trigger{Kind: "slo"}, Approval{})
	if e != nil || out.Outcome != "no_action" {
		t.Fatalf("actual HTTP loop %+v %v", out, e)
	}
	usage, e := store.Usage("fixture")
	if e != nil || usage.SpentMicros != 1 || usage.ReservedMicros != 0 {
		t.Fatal("step not accounted")
	}
}
