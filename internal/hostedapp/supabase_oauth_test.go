package hostedapp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSupabaseProviderRejectsMalformedAuthorityAndRedactsErrors(t *testing.T) {
	raw := `[{"ref":"abcdefghijklmnopqrst","name":"Project","organization_slug":"org","status":"ACTIVE_HEALTHY","database":{"host":"db.abcdefghijklmnopqrst.supabase.co"}}]`
	status := 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/organizations" {
			fmt.Fprint(w, `[{"slug":"org","name":"Organization"}]`)
			return
		}
		w.WriteHeader(status)
		fmt.Fprint(w, raw)
	}))
	defer server.Close()
	client, _ := newSupabaseOAuth("client", "secret", "https://app.example")
	client.endpoint = server.URL
	if _, err := client.projects(context.Background(), "access"); err != nil {
		t.Fatal(err)
	}
	raw = strings.Replace(raw, "db.abcdefghijklmnopqrst.supabase.co", "attacker.example", 1)
	if _, err := client.projects(context.Background(), "access"); err == nil {
		t.Fatal("provider-supplied foreign database host accepted")
	}
	status = 503
	raw = `{"error":"private-access-token-do-not-echo"}`
	if _, err := client.projects(context.Background(), "access"); err == nil || strings.Contains(err.Error(), "private-access-token") {
		t.Fatal("provider error body escaped")
	}
	status = 200
	for _, body := range []string{`{"access_token":"a","refresh_token":"b","expires_in":-1,"token_type":"Bearer"}`, `{"access_token":"a","refresh_token":"b","expires_in":3600,"token_type":"MAC"}`, `{"access_token":"a","expires_in":3600,"token_type":"Bearer"}`} {
		raw = body
		if _, err := client.exchange(context.Background(), url.Values{"grant_type": {"authorization_code"}, "code": {"test"}}); err == nil {
			t.Fatal("invalid grant metadata accepted")
		}
	}
	status = 429
	raw = `{"error":"slow down"}`
	if _, err := client.exchange(context.Background(), url.Values{}); err != errSupabaseUnavailable {
		t.Fatal("rate limiting treated as revoked grant")
	}
}
