package supabaseproject

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOAuthMetricsResolvesFreshTokenAndBindsProject(t *testing.T) {
	calls := 0
	resolves := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != fmt.Sprintf("Bearer token-%d", calls) {
			t.Error("stale token")
		}
		w.WriteHeader(503)
	}))
	defer server.Close()
	r := NewOAuthMemoryMetricsReader("abcdefghijklmnopqrst", func(context.Context) (string, error) { resolves++; return fmt.Sprintf("token-%d", resolves), nil })
	r.endpoint = server.URL
	for range 2 {
		_, _ = r.Read(context.Background(), "abcdefghijklmnopqrst")
	}
	_, _ = r.Read(context.Background(), "zzzzzzzzzzzzzzzzzzzz")
	if calls != 2 || resolves != 2 {
		t.Fatal("resolver did not remain project bound")
	}
}
