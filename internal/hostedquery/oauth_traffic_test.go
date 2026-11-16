package hostedquery

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOAuthTrafficReaderResolvesTokenPerRequestAndBindsProject(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.Header.Get("Authorization"); got != fmt.Sprintf("Bearer token-%d", calls) {
			t.Errorf("stale token: %s", got)
		}
		fmt.Fprint(w, `{"result":[]}`)
	}))
	defer server.Close()
	resolves := 0
	reader := NewOAuthTrafficPlanReader("abcdefghijklmnopqrst", func(context.Context) (string, error) { resolves++; return fmt.Sprintf("token-%d", resolves), nil })
	reader.endpoint = server.URL
	now := time.Now()
	for range 2 {
		reader.read(context.Background(), "abcdefghijklmnopqrst", "app", 42, now.Add(-time.Minute), now)
	}
	reader.read(context.Background(), "zzzzzzzzzzzzzzzzzzzz", "app", 42, now.Add(-time.Minute), now)
	if calls != 2 || resolves != 2 {
		t.Fatalf("requests escaped project binding: %d %d", calls, resolves)
	}
}
