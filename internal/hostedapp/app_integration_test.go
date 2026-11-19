//go:build integration

package hostedapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
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
	firstPage := request(http.MethodGet, "/app", nil, owner, "")
	if firstPage.Code != 200 || strings.Count(firstPage.Body.String(), `name="capability"`) != len(projectCapabilities) {
		t.Fatal("a new workspace did not open the six repair choices")
	}
	draftPage := request(http.MethodPost, "/app/setup", url.Values{"capability": {"index", "cron"}}, owner, origin)
	draftURL, err := url.Parse(draftPage.Header().Get("Location"))
	if err != nil || draftPage.Code != 303 || draftURL.Path != "/app/projects/new" || draftURL.Query().Get("draft") == "" {
		t.Fatal("could not continue to connect a database")
	}
	values := url.Values{"name": {"Rookery fixture"}, "ref": {"abcdefghijklmnopqrst"}}
	// A second tab chooses a different set before the first tab selects its project.
	if w := request(http.MethodPost, "/app/setup", url.Values{"capability": {"memory"}}, owner, origin); w.Code != 303 {
		t.Fatal("could not start a second setup")
	}
	values.Set("draft", draftURL.Query().Get("draft"))
	if w := request(http.MethodPost, "/app/projects/new", values, other, origin); w.Code != 400 {
		t.Fatal("another account reused the setup draft")
	}
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
	setupPage := request(http.MethodGet, projectPath, nil, owner, "")
	if setupPage.Code != 200 || !strings.Contains(setupPage.Body.String(), "CREATE ROLE pt_monitor_") {
		t.Fatal("owner setup did not render generated SQL")
	}
	for _, capability := range []string{"index", "cron"} {
		if !strings.Contains(setupPage.Body.String(), `aria-describedby="capability-`+capability+`-help" checked`) {
			t.Errorf("project lost draft permission %s", capability)
		}
	}
	for _, requirement := range []string{"Requires table-owner access", "direct MAINTAIN grant", "Requires function-owner access", "Requires job-owner access and an approved schedule window"} {
		if !strings.Contains(setupPage.Body.String(), requirement) {
			t.Errorf("owner setup did not disclose %q", requirement)
		}
	}
	for _, route := range []struct{ method, path string }{{"GET", ""}, {"GET", "/activity"}, {"POST", "/connect"}, {"POST", "/start"}, {"POST", "/repair-mode"}, {"POST", "/disconnect"}} {
		if w := request(route.method, projectPath+route.path, nil, other, origin); w.Code != 404 {
			t.Fatalf("cross-account %s %s allowed: %d", route.method, route.path, w.Code)
		}
	}
	if w := request(http.MethodPost, projectPath+"/connect", url.Values{"password": {"test secret"}}, owner, origin); w.Code != 400 || strings.Contains(w.Body.String(), "test secret") {
		t.Fatal("invalid connection accepted or credential echoed")
	}
	start := url.Values{"setup_token": {"test-secret-token"}, "ca": {"invalid certificate"}, "capability": {"index", "cron"}}
	if w := request(http.MethodPost, projectPath+"/start", start, owner, origin); w.Code != 400 || strings.Contains(w.Body.String(), "test-secret-token") {
		t.Fatal("invalid automatic setup accepted or token echoed")
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
	sealed, err := app.vault.seal(row, projectSecrets{Automatic: true, Capabilities: []string{"index", "analyze"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.projects.activate(ctx, row, sealed); err != nil {
		t.Fatal(err)
	}
	row, err = app.projects.find(ctx, ownerUser.ID, projectID)
	if err != nil || row.Epoch <= 0 {
		t.Fatalf("activation did not persist: %v", err)
	}
	monitorOnlyPage := request(http.MethodGet, projectPath+"?section=permissions", nil, owner, "")
	if monitorOnlyPage.Code != 200 || !strings.Contains(monitorOnlyPage.Body.String(), "Connect repair access") || strings.Contains(monitorOnlyPage.Body.String(), "Create worker key") {
		t.Fatal("automatic monitoring-only connection did not explain the managed credential route")
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
	keyPath := projectPath + "/repair-worker-key"
	apiPath := "/api/projects/" + projectID + "/repair-worker"
	workerRequest := func(method, path, key, payload string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, origin+path, strings.NewReader(payload))
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		r.Header.Set("Content-Type", "application/json")
		app.ServeHTTP(w, r)
		return w
	}
	issueKey := func() string {
		t.Helper()
		w := request(http.MethodPost, keyPath, url.Values{"mode": {"issue"}}, owner, origin)
		key := regexp.MustCompile(`[0-9a-f]{64}`).FindString(w.Body.String())
		if w.Code != 200 || key == "" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("worker key was not issued privately: %d", w.Code)
		}
		return key
	}
	if w := request(http.MethodPost, keyPath, url.Values{"mode": {"issue"}}, other, origin); w.Code != 404 {
		t.Fatalf("another account could issue a key: %d", w.Code)
	}
	if w := workerRequest(http.MethodGet, apiPath, "", ""); w.Code != 401 {
		t.Fatalf("worker API was usable without a key: %d", w.Code)
	}
	key := issueKey()
	if w := workerRequest(http.MethodGet, "/api/projects/aaaaaaaaaaaaaaaa/repair-worker", key, ""); w.Code != 401 {
		t.Fatalf("worker key was accepted on another project: %d", w.Code)
	}
	var policy struct {
		Project      string
		Epoch        int64
		Capabilities []string
		Paused       bool
	}
	w := workerRequest(http.MethodGet, apiPath, key, "")
	identity, err := row.identity()
	if err != nil || w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &policy) != nil || policy.Project != identity.ID() ||
		policy.Epoch != row.Epoch || strings.Join(policy.Capabilities, ",") != "index,analyze" || policy.Paused {
		t.Fatalf("worker did not receive the current permission selection: %d %+v", w.Code, policy)
	}
	report := func(epoch int64, operation string) string {
		return fmt.Sprintf(`{"epoch":%d,"operations":[{"operation":%q,"status":"granted"}]}`, epoch, operation)
	}
	if w := workerRequest(http.MethodPost, apiPath, key, report(row.Epoch, "cron")); w.Code != 400 {
		t.Fatalf("worker reported a disabled repair: %d", w.Code)
	}
	if w := workerRequest(http.MethodPost, apiPath, key, report(row.Epoch, "analyze")); w.Code != 204 {
		t.Fatalf("worker could not submit valid permission report: %d", w.Code)
	}
	if w := request(http.MethodGet, projectPath+"?section=permissions", nil, owner, ""); w.Code != 200 ||
		!strings.Contains(w.Body.String(), "Database access reported") || strings.Contains(w.Body.String(), key) {
		t.Fatal("the dashboard did not show the report privately")
	}
	if got := request(http.MethodPost, projectPath+"/repair-mode", url.Values{"mode": {"pause"}}, owner, origin).Code; got != 303 {
		t.Fatalf("pause repairs returned %d", got)
	}
	paused, err := app.projects.find(ctx, ownerUser.ID, projectID)
	if err != nil || paused.Epoch <= row.Epoch || !paused.Configured {
		t.Fatalf("pause did not retain monitoring with a new permission epoch: %v", err)
	}
	options, err := app.vault.open(paused, paused.Secret)
	if err != nil || !options.RepairsPaused {
		t.Fatalf("pause preference was not persisted: %v", err)
	}
	if w := workerRequest(http.MethodPost, apiPath, key, report(row.Epoch, "analyze")); w.Code != 409 {
		t.Fatalf("stale worker report passed pause fence: %d", w.Code)
	}
	if w := workerRequest(http.MethodPost, apiPath, key, report(paused.Epoch, "analyze")); w.Code != 409 {
		t.Fatalf("paused worker reported a new permission: %d", w.Code)
	}
	if got := request(http.MethodPost, projectPath+"/repair-mode", url.Values{"mode": {"resume"}}, owner, origin).Code; got != 303 {
		t.Fatalf("resume repairs returned %d", got)
	}
	resumed, err := app.projects.find(ctx, ownerUser.ID, projectID)
	if err != nil || resumed.Epoch <= paused.Epoch {
		t.Fatalf("resume did not update permission epoch: %v", err)
	}
	options, err = app.vault.open(resumed, resumed.Secret)
	if err != nil || options.RepairsPaused {
		t.Fatalf("resume preference was not persisted: %v", err)
	}
	rotated := issueKey()
	if rotated == key || workerRequest(http.MethodGet, apiPath, key, "").Code != 401 {
		t.Fatal("replacing the key left old access usable")
	}
	if w := request(http.MethodPost, projectPath+"/repair-mode", url.Values{"mode": {"choices"}, "capability": {"analyze"}}, owner, origin); w.Code != 303 {
		t.Fatalf("changing choices on an existing connection returned %d", w.Code)
	}
	if w := workerRequest(http.MethodGet, apiPath, rotated, ""); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &policy) != nil ||
		policy.Epoch <= resumed.Epoch || strings.Join(policy.Capabilities, ",") != "analyze" {
		t.Fatalf("the connected worker did not receive the updated choices: %d %+v", w.Code, policy)
	}
	if w := workerRequest(http.MethodPost, apiPath, rotated, report(resumed.Epoch, "index")); w.Code != 409 {
		t.Fatalf("a report from before the checkbox was cleared should refresh policy: %d", w.Code)
	}
	if w := request(http.MethodPost, keyPath, url.Values{"mode": {"revoke"}}, owner, origin); w.Code != 303 ||
		workerRequest(http.MethodGet, apiPath, rotated, "").Code != 401 {
		t.Fatal("owner revocation did not stop the worker")
	}
	linked := issueKey()
	if w := request(http.MethodPost, projectPath+"/disconnect", nil, owner, origin); w.Code != 409 {
		t.Fatalf("disconnect without a confirmation epoch returned %d", w.Code)
	}
	confirmed := url.Values{"epoch": {fmt.Sprint(policy.Epoch)}}
	if w := request(http.MethodPost, projectPath+"/disconnect", confirmed, owner, origin); w.Code != 303 {
		t.Fatalf("disconnect returned %d", w.Code)
	}
	if stopped, err := app.projects.find(ctx, ownerUser.ID, projectID); err != nil || stopped.Configured || stopped.Epoch >= 0 {
		t.Fatalf("disconnect kept the credentials or an active epoch: %v", err)
	}
	if w := request(http.MethodPost, projectPath+"/disconnect", confirmed, owner, origin); w.Code != 303 {
		t.Fatalf("disconnect retry returned %d", w.Code)
	}
	if w := workerRequest(http.MethodGet, apiPath, linked, ""); w.Code != 401 {
		t.Fatalf("disconnect preserved the worker key: %d", w.Code)
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
