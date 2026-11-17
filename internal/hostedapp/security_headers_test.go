package hostedapp

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestOAuthFormActionAllowsOnlyEnabledProviderDestinations(t *testing.T) {
	const origin = "https://pooltune.example"
	for _, tc := range []struct {
		name             string
		google, supabase bool
		destinations     string
	}{
		{name: "disabled", destinations: "'self'"},
		{name: "google only", google: true, destinations: "'self' https://accounts.google.com"},
		{name: "supabase only", supabase: true, destinations: "'self' https://api.supabase.com https://supabase.com"},
		{name: "both providers", google: true, supabase: true, destinations: "'self' https://api.supabase.com https://supabase.com https://accounts.google.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, _ := url.Parse(origin)
			app := &App{origin: parsed, handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })}
			if tc.google {
				app.google = &googleOIDC{}
			}
			if tc.supabase {
				app.supabase = &supabaseOAuth{}
			}
			response := httptest.NewRecorder()
			app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, origin+"/signin", nil))
			want := "default-src 'none'; style-src 'self'; script-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action " + tc.destinations + "; frame-ancestors 'none'"
			if got := response.Header().Get("Content-Security-Policy"); got != want {
				t.Fatalf("CSP=%q, want %q", got, want)
			}
		})
	}
}
