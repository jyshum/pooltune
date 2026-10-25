package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPRejectsModelOverrideBeforeDispatch(t *testing.T) {
	s, token := budgetFixture(t, 100, 100)
	m := &fixtureModel{}
	service := &Service{Store: s, Model: m, Profile: testProfile()}
	h, e := NewHandler(service, true)
	if e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("POST", "http://gateway/v1/step", strings.NewReader(`{"model":"expensive-override","run":"x","step":0}`))
	r.RemoteAddr = "127.0.0.1:1000"
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || m.calls.Load() != 0 {
		t.Fatal("arbitrary model accepted")
	}
}
func TestClaudeFixtureUsesFixedToolsAndCountsBeforeInference(t *testing.T) {
	order := []string{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		order = append(order, r.URL.Path)
		if r.Header.Get("x-api-key") != "fixture-private-key" {
			t.Error("missing hosted key")
		}
		if r.URL.Path == "/v1/messages/count_tokens" {
			w.Write([]byte(`{"input_tokens":10}`))
			return
		}
		w.Write([]byte(`{"stop_reason":"tool_use","content":[{"type":"tool_use","name":"ReadState","input":{}}],"usage":{"input_tokens":10,"output_tokens":3}}`))
	}))
	defer api.Close()
	model, e := NewClaude(testProfile(), func() (string, error) { return "fixture-private-key", nil }, api.URL, true)
	if e != nil {
		t.Fatal(e)
	}
	out, e := model.Infer(t.Context(), ModelRequest{Operation: "priority"})
	if e != nil || out.Tool == nil || out.Tool.Name != "ReadState" || !out.Usage.Known {
		t.Fatalf("tool response %+v %v", out, e)
	}
	if len(order) != 2 || order[0] != "/v1/messages/count_tokens" || order[1] != "/v1/messages" {
		t.Fatal("token preflight missing")
	}
}

func TestClaudeRefusesMissingOrOversizedTokenEstimate(t *testing.T) {
	for _, estimate := range []string{`{}`, `{"input_tokens":null}`, `{"input_tokens":40000}`} {
		t.Run(estimate, func(t *testing.T) {
			messages := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/messages/count_tokens" {
					w.Write([]byte(estimate))
					return
				}
				messages++
				w.Write([]byte(`{}`))
			}))
			defer api.Close()
			model, e := NewClaude(testProfile(), func() (string, error) { return "fixture-key", nil }, api.URL, true)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = model.Infer(t.Context(), ModelRequest{Operation: "priority"}); e == nil || messages != 0 {
				t.Fatalf("unbounded inference: calls=%d err=%v", messages, e)
			}
		})
	}
}
