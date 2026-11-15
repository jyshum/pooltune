//go:build integration

package hostedapp

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"pooltune/internal/hostedquery"
	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAccountDashboardAndProjectOwnership(t *testing.T) {
	ctx, state, _ := testpg.Start(t)
	cfg, err := pgxpool.ParseConfig(state.ConnString())
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig = state
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	const origin = "https://preview.example.com"
	const code = "private-local-test-invite"
	const password = "one exact test passphrase"
	app, err := New(ctx, pool, Config{Origin: origin, InviteCode: code, Key: bytes.Repeat([]byte{23}, 32),
		Sampling: hostedquery.SamplingPolicy{Interval: time.Minute, Window: 5 * time.Minute}, QueueInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path string, fields url.Values, cookie *http.Cookie, from string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, origin+path, strings.NewReader(fields.Encode()))
		if cookie != nil {
			r.AddCookie(cookie)
		}
		if method == http.MethodPost {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", from)
		}
		app.ServeHTTP(w, r)
		return w
	}
	if w := request(http.MethodGet, "/app", nil, nil, ""); w.Code != 303 || w.Header().Get("Location") != "/signin" {
		t.Fatal("dashboard allowed anonymous access")
	}
	for _, path := range []string{"/", "/signup", "/signin", "/assets/app.css", "/assets/app.js"} {
		if got := request(http.MethodGet, path, nil, nil, "").Code; got != 200 {
			t.Fatalf("%s returned %d", path, got)
		}
	}
	register := func(email string) *http.Cookie {
		t.Helper()
		values := url.Values{"email": {email}, "password": {password}, "invite_code": {code}}
		w := request(http.MethodPost, "/signup", values, nil, origin)
		if w.Code != 303 || w.Header().Get("Location") != "/app" {
			t.Fatalf("signup returned %d", w.Code)
		}
		cookies := w.Result().Cookies()
		if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].Path != "/" {
			t.Fatal("missing secure session cookie")
		}
		return cookies[0]
	}
	owner, other := register("pilot@example.com"), register("other@example.com")
	if w := request(http.MethodPost, "/app/projects/new", nil, owner, "https://another.example.com"); w.Code != 403 {
		t.Fatal("cross-site project write accepted")
	}
	values := url.Values{"name": {"Rookery fixture"}, "ref": {"abcdefghijklmnopqrst"}}
	created := request(http.MethodPost, "/app/projects/new", values, owner, origin)
	if created.Code != 303 {
		t.Fatalf("create project returned %d", created.Code)
	}
	projectPath := created.Header().Get("Location")
	if !strings.HasPrefix(projectPath, "/app/projects/") {
		t.Fatal("missing project redirect")
	}
	if w := request(http.MethodGet, "/app", nil, owner, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "Rookery fixture") {
		t.Fatal("owner dashboard did not show the project")
	}
	if w := request(http.MethodGet, "/app", nil, other, ""); w.Code != 200 || strings.Contains(w.Body.String(), "Rookery fixture") {
		t.Fatal("project visible to another account")
	}
	if w := request(http.MethodGet, projectPath, nil, owner, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "CREATE ROLE pt_monitor_") {
		t.Fatal("owner setup did not render generated SQL")
	}
	for _, route := range []struct{ method, path string }{{"GET", ""}, {"GET", "/activity"}, {"POST", "/connect"}, {"POST", "/disconnect"}} {
		if w := request(route.method, projectPath+route.path, nil, other, origin); w.Code != 404 {
			t.Fatalf("cross-account %s %s allowed: %d", route.method, route.path, w.Code)
		}
	}
	if w := request(http.MethodPost, projectPath+"/connect", url.Values{"password": {"test secret"}}, owner, origin); w.Code != 400 || strings.Contains(w.Body.String(), "test secret") {
		t.Fatal("invalid connection accepted or credential echoed")
	}
	ownerUser, err := app.accounts.UserBySession(ctx, owner.Value)
	if err != nil {
		t.Fatal(err)
	}
	projectID := strings.TrimPrefix(projectPath, "/app/projects/")
	row, err := app.projects.find(ctx, ownerUser.ID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	// The live test exercises the actual target verification. Here we exercise
	// the local account/worker registration transaction and provider budget.
	if err := app.projects.activate(ctx, row, []byte("test ciphertext")); err != nil {
		t.Fatal(err)
	}
	row, err = app.projects.find(ctx, ownerUser.ID, projectID)
	if err != nil || row.Epoch <= 0 {
		t.Fatalf("activation did not persist: %v", err)
	}
	if err := app.projects.admitModel(ctx, projectID, row.Epoch+1); !errors.Is(err, hostedquery.ErrProjectModelBudget) {
		t.Fatalf("stale epoch reserved a provider call: %v", err)
	}
	for n := 0; n < 8; n++ {
		if err := app.projects.admitModel(ctx, projectID, row.Epoch); err != nil {
			t.Fatalf("model admission %d: %v", n, err)
		}
	}
	if err := (projectStore{pool}).admitModel(ctx, projectID, row.Epoch); !errors.Is(err, hostedquery.ErrProjectModelBudget) {
		t.Fatalf("budget did not survive store reconnect: %v", err)
	}
	if w := request(http.MethodPost, projectPath+"/disconnect", nil, owner, origin); w.Code != 303 {
		t.Fatalf("disconnect returned %d", w.Code)
	}
	if stopped, err := app.projects.find(ctx, ownerUser.ID, projectID); err != nil || stopped.Configured || stopped.Epoch >= 0 {
		t.Fatalf("disconnect kept the credentials or an active epoch: %v", err)
	}
	if w := request(http.MethodPost, "/signout", nil, owner, origin); w.Code != 303 {
		t.Fatalf("sign out returned %d", w.Code)
	}
	if w := request(http.MethodGet, projectPath, nil, owner, ""); w.Code != 303 {
		t.Fatal("old account cookie was still usable")
	}
	if w := request(http.MethodPost, "/signin", url.Values{"email": {"pilot@example.com"}, "password": {password}}, nil, origin); w.Code != 303 {
		t.Fatalf("sign in returned %d", w.Code)
	}

	user, err := app.accounts.UserBySession(context.Background(), other.Value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.projects.find(ctx, user.ID, strings.TrimPrefix(projectPath, "/app/projects/")); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("store did not enforce the account boundary")
	}
}
