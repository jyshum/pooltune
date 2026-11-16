//go:build integration

package hostedapp

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/jackc/pgx/v5/pgxpool"
	"pooltune/internal/hostedquery"
	"pooltune/internal/testpg"
)

func TestGoogleHTTPFlowSecurity(t *testing.T) {
	ctx, state, _ := testpg.Start(t)
	cfg, _ := pgxpool.ParseConfig(state.ConnString())
	cfg.ConnConfig = state
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	const origin = "https://preview.example.com"
	const invite = "private-local-test-invite"
	app, err := New(ctx, pool, Config{Origin: origin, InviteCode: invite, Key: bytes.Repeat([]byte{23}, 32), Sampling: hostedquery.SamplingPolicy{Interval: time.Minute, Window: 5 * time.Minute}, QueueInterval: time.Second, GoogleClientID: "test-client", GoogleClientSecret: "test-secret"})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	app.google.verifier = oidc.NewVerifier("https://accounts.google.com", &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}}, &oidc.Config{ClientID: "test-client"})
	var claims map[string]any
	var expectedVerifier string
	var tokenFailure string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("code_verifier") != expectedVerifier || r.Form.Get("redirect_uri") != origin+"/auth/google/callback" {
			t.Error("wrong PKCE verifier or callback")
		}
		if tokenFailure == "exchange" {
			http.Error(w, "denied", http.StatusBadRequest)
			return
		}
		payload, _ := json.Marshal(claims)
		signed, _ := signer.Sign(payload)
		raw, _ := signed.CompactSerialize()
		if tokenFailure == "signature" {
			parts := strings.Split(raw, ".")
			parts[2] = strings.Repeat("A", len(parts[2]))
			raw = strings.Join(parts, ".")
		}
		if tokenFailure == "missing" {
			raw = ""
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access", "token_type": "Bearer", "id_token": raw})
	}))
	defer provider.Close()
	app.google.oauth.Endpoint.TokenURL = provider.URL
	request := func(method, path string, fields url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, origin+path, strings.NewReader(fields.Encode()))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, c := range cookies {
			if c != nil {
				r.AddCookie(c)
			}
		}
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		return w
	}
	start := func(mode string, session *http.Cookie) (string, *http.Cookie) {
		t.Helper()
		w := request("POST", "/auth/google/start", url.Values{"mode": {mode}, "invite_code": {invite}}, session)
		if w.Code != 303 {
			t.Fatalf("start %s: %d %s", mode, w.Code, w.Body.String())
		}
		location, _ := url.Parse(w.Header().Get("Location"))
		q := location.Query()
		if location.Host != "accounts.google.com" || q.Get("code_challenge_method") != "S256" || q.Get("scope") != "openid email" || q.Get("nonce") == "" {
			t.Fatalf("unsafe authorization request: %s", location)
		}
		digestQuery := `SELECT verifier FROM pooltune_app.google_flows WHERE nonce=$1`
		if err := pool.QueryRow(ctx, digestQuery, q.Get("nonce")).Scan(&expectedVerifier); err != nil {
			t.Fatal(err)
		}
		tokenFailure = ""
		claims = map[string]any{"iss": "https://accounts.google.com", "aud": "test-client", "sub": "google-subject", "email": "new@example.com", "email_verified": true, "nonce": q.Get("nonce"), "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix()}
		return "/auth/google/callback?state=" + url.QueryEscape(q.Get("state")) + "&code=code", w.Result().Cookies()[0]
	}
	if w := request("POST", "/auth/google/start", url.Values{"mode": {"signup"}}); w.Code != 403 {
		t.Fatal("signup without invite accepted")
	}
	if w := request("GET", "/signin", nil); !strings.Contains(w.Body.String(), "Continue with Google") {
		t.Fatal("Google sign-in missing")
	}
	crossSite := httptest.NewRequest("POST", origin+"/auth/google/start", strings.NewReader("mode=signin"))
	crossSite.Header.Set("Origin", "https://attacker.example")
	crossSite.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	blocked := httptest.NewRecorder()
	app.ServeHTTP(blocked, crossSite)
	if blocked.Code != 403 {
		t.Fatal("cross-site Google start accepted")
	}
	path, browser := start("signup", nil)
	if w := request("GET", path, nil); w.Code != 400 {
		t.Fatal("unbound callback accepted")
	}
	w := request("GET", path, nil, browser)
	if w.Code != 303 || w.Header().Get("Location") != "/app" {
		t.Fatalf("signup callback: %d %s", w.Code, w.Body.String())
	}
	var session *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == app.cookie {
			session = c
		}
	}
	if session == nil || !session.HttpOnly || !session.Secure {
		t.Fatal("missing session")
	}
	if w := request("GET", "/app", nil, session); w.Code != 200 {
		t.Fatal("Google session cannot open dashboard")
	}
	if w := request("GET", path, nil, browser); w.Code != 400 {
		t.Fatal("callback replay accepted")
	}
	for _, tc := range []struct {
		name, key string
		value     any
	}{{"nonce", "nonce", "wrong"}, {"audience", "aud", "wrong"}, {"issuer", "iss", "https://wrong.example"}, {"expired", "exp", time.Now().Add(-time.Hour).Unix()}, {"unverified", "email_verified", false}, {"empty subject", "sub", ""}} {
		path, browser = start("signin", nil)
		claims[tc.key] = tc.value
		if w := request("GET", path, nil, browser); w.Code != 400 {
			t.Errorf("%s token accepted: %d", tc.name, w.Code)
		}
	}
	for _, failure := range []string{"exchange", "signature", "missing"} {
		path, browser = start("signin", nil)
		tokenFailure = failure
		if w := request("GET", path, nil, browser); w.Code != 400 {
			t.Errorf("bad token %s accepted", failure)
		}
	}
	path, browser = start("signin", nil)
	claims["sub"] = "unknown"
	claims["email"] = "unknown@example.com"
	if w := request("GET", path, nil, browser); w.Code != 400 || !strings.Contains(w.Body.String(), "Create an account") {
		t.Fatal("unknown signin lacked signup path")
	}
	path, browser = start("signin", nil)
	if w := request("GET", strings.Replace(path, "&code=code", "&error=access_denied", 1), nil, browser); w.Code != 400 || !strings.Contains(w.Body.String(), "cancelled") {
		t.Fatal("cancel feedback missing")
	}
	path, browser = start("signin", nil)
	if w := request("GET", path, nil, browser); w.Code != 303 {
		t.Fatal("existing Google sign-in failed")
	}
	_, token, err := app.accounts.Register(ctx, "owner@example.com", "existing long passphrase")
	if err != nil {
		t.Fatal(err)
	}
	owner := &http.Cookie{Name: app.cookie, Value: token}
	path, browser = start("signup", nil)
	claims["sub"] = "owner-sub"
	claims["email"] = "owner@example.com"
	if w := request("GET", path, nil, browser); w.Code != 400 || !strings.Contains(w.Body.String(), "existing account") {
		t.Fatal("password account collision not protected")
	}
	path, browser = start("link", owner)
	claims["sub"] = "owner-sub"
	claims["email"] = "owner@example.com"
	if w := request("GET", path, nil, browser); w.Code != 400 {
		t.Fatal("link without session accepted")
	}
	path, browser = start("link", owner)
	_, replacement, err := app.accounts.SignIn(ctx, "owner@example.com", "existing long passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if w := request("GET", path, nil, browser, &http.Cookie{Name: app.cookie, Value: replacement}); w.Code != 400 {
		t.Fatal("link accepted a different session")
	}
	path, browser = start("link", owner)
	if err := app.accounts.SignOut(ctx, owner.Value); err != nil {
		t.Fatal(err)
	}
	claims["sub"] = "owner-sub"
	claims["email"] = "owner@example.com"
	if w := request("GET", path, nil, browser, owner); w.Code != 400 {
		t.Fatal("revoked link session accepted")
	}
	owner.Value = replacement
	path, browser = start("link", owner)
	claims["sub"] = "owner-sub"
	claims["email"] = "owner@example.com"
	if w := request("GET", path, nil, browser, owner); w.Code != 303 || w.Header().Get("Location") != "/app/account?google=linked" {
		t.Fatalf("explicit linking failed: %d %s", w.Code, w.Body.String())
	}
}
