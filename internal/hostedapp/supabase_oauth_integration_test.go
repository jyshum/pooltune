//go:build integration

package hostedapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"pooltune/internal/hostedquery"
	"pooltune/internal/testpg"
)

func TestSupabaseOAuthBindingPickerAndRefresh(t *testing.T) {
	ctx, state, _ := testpg.Start(t)
	ctx, cancelTest := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
	defer cancelTest()
	pc, _ := pgxpool.ParseConfig(state.ConnString())
	pc.ConnConfig = state
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	const origin = "https://preview.example.com"
	app, err := New(ctx, pool, Config{Origin: origin, InviteCode: "private-local-test-invite", Key: bytes.Repeat([]byte{23}, 32), Sampling: hostedquery.SamplingPolicy{Interval: time.Minute, Window: 5 * time.Minute}, QueueInterval: time.Second, SupabaseClientID: "client", SupabaseClientSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	user, session, err := app.accounts.Register(ctx, "oauth@example.com", "one exact test passphrase")
	if err != nil {
		t.Fatal(err)
	}
	otherUser, other, err := app.accounts.Register(ctx, "other@example.com", "one exact test passphrase")
	if err != nil {
		t.Fatal(err)
	}
	var exchanges, refreshes, issuances atomic.Int32
	var denied atomic.Bool
	var writeDenied atomic.Bool
	var probeStatus atomic.Int32
	var delayedSetup atomic.Bool
	var malformed atomic.Bool
	var expectedChallenge string
	var revoked atomic.Bool
	var cancelNextRefresh atomic.Bool
	refreshContext, cancelRefresh := context.WithCancel(ctx)
	defer cancelRefresh()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if delayedSetup.Load() && (r.URL.Path == "/v1/projects" || r.URL.Path == "/v1/organizations" || strings.HasSuffix(r.URL.Path, "/database/query")) {
			delay := 6 * time.Second
			if strings.HasSuffix(r.URL.Path, "/database/query") {
				delay = 7 * time.Second
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(delay):
			}
		}

		switch r.URL.Path {
		case "/v1/oauth/token":
			id, secret, ok := r.BasicAuth()
			if !ok || id != "client" || secret != "secret" {
				t.Error("missing basic client authentication")
				w.WriteHeader(401)
				return
			}
			_ = r.ParseForm()
			if r.Form.Get("grant_type") == "refresh_token" {
				number := refreshes.Add(1)
				expectedRefresh := "refresh-one"
				if number > 1 {
					expectedRefresh = "refresh-two"
				}
				if cancelNextRefresh.Load() {
					cancelRefresh()
				}
				if !revoked.Load() && r.Form.Get("refresh_token") != expectedRefresh {
					t.Error("rotating refresh token reused")
				}
				if revoked.Load() {
					w.WriteHeader(400)
					fmt.Fprint(w, `{"error":"invalid_grant"}`)
					return
				}
			} else {
				exchanges.Add(1)
				digest := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
				if base64.RawURLEncoding.EncodeToString(digest[:]) != expectedChallenge || r.Form.Get("redirect_uri") != origin+"/auth/supabase/callback" {
					t.Error("PKCE or callback not bound")
				}
			}
			refresh := "refresh-one"
			if r.Form.Get("grant_type") == "refresh_token" {
				refresh = "refresh-two"
				if refreshes.Load() > 1 {
					refresh = "refresh-three"
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": strings.Repeat("a", 2048), "refresh_token": refresh, "expires_in": 3600, "token_type": "Bearer", "project_refs": nil})
		case "/v1/projects":
			if denied.Load() {
				w.WriteHeader(403)
				return
			}
			fmt.Fprint(w, `[{"id":"abcdefghijklmnopqrst","ref":"abcdefghijklmnopqrst","name":"Allowed project","organization_id":"org-id","organization_slug":"our-org","region":"us-west-1","created_at":"2026-01-01","status":"ACTIVE_HEALTHY","database":{"host":"db.abcdefghijklmnopqrst.supabase.co","version":"17","postgres_engine":"postgres","release_channel":"ga"}}]`)
		case "/v1/projects/abcdefghijklmnopqrst/database/query":
			var body struct {
				Query          string `json:"query"`
				ReadOnly       *bool  `json:"read_only"`
				DisableTimeout *bool  `json:"disable_statement_timeout"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ReadOnly == nil || *body.ReadOnly || body.DisableTimeout == nil || *body.DisableTimeout || !strings.Contains(body.Query, "current_user='postgres'") {
				t.Error("write authority was not checked with fixed owner identity and bounded execution")
			}
			if code := int(probeStatus.Load()); code != 0 {
				w.WriteHeader(code)
				return
			}
			if delayedSetup.Load() || writeDenied.Load() || r.Header.Get("Authorization") == "Bearer read-only-access" {
				fmt.Fprint(w, `[{"authorized":false}]`)
				return
			}
			fmt.Fprint(w, `[{"authorized":true}]`)
		case "/v1/projects/abcdefghijklmnopqrst/cli/login-role":
			issuances.Add(1)
			role := "cli_login_postgres"
			if malformed.Load() {
				role = "postgres"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"role": role, "password": fmt.Sprintf("temporary-%d", issuances.Load()), "ttl_seconds": 120})
		case "/v1/organizations":
			fmt.Fprint(w, `[{"id":"org-id","slug":"our-org","name":"Our organization"}]`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer provider.Close()
	app.supabase.endpoint = provider.URL
	request := func(method, path, session string, flow *http.Cookie, fields url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, origin+path, strings.NewReader(fields.Encode()))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: app.cookie, Value: session})
		if flow != nil {
			r.AddCookie(flow)
		}
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		return w
	}
	start := func() (string, *http.Cookie) {
		w := request("POST", "/auth/supabase/start", session, nil, nil)
		if w.Code != 303 {
			t.Fatalf("start %d %s", w.Code, w.Body.String())
		}
		u, _ := url.Parse(w.Header().Get("Location"))
		q := u.Query()
		if u.Host != "api.supabase.com" || q.Get("code_challenge_method") != "S256" {
			t.Fatal("unsafe authorization URL")
		}
		expectedChallenge = q.Get("code_challenge")
		return q.Get("state"), w.Result().Cookies()[0]
	}
	s, c := start()
	if w := request("GET", "/auth/supabase/callback?code=code&state="+s, other, c, nil); w.Code == 303 {
		t.Fatal("account swap accepted")
	}
	if exchanges.Load() != 0 {
		t.Fatal("exchanged code before session check")
	}
	_, sameUserSession, err := app.accounts.SignIn(ctx, "oauth@example.com", "one exact test passphrase")
	if err != nil {
		t.Fatal(err)
	}
	s, c = start()
	if w := request("GET", "/auth/supabase/callback?code=code&state="+s, sameUserSession, c, nil); w.Code == 303 {
		t.Fatal("same-user session swap accepted")
	}
	s, c = start()
	if _, err = pool.Exec(ctx, `UPDATE pooltune_app.supabase_flows SET expires_at=now()-interval '1 minute' WHERE state=$1`, digestBytes(s)); err != nil {
		t.Fatal(err)
	}
	if w := request("GET", "/auth/supabase/callback?code=code&state="+s, session, c, nil); w.Code == 303 {
		t.Fatal("expired flow accepted")
	}
	s, c = start()
	if w := request("GET", "/auth/supabase/callback?error=access_denied&state="+s, session, c, nil); w.Code == 303 {
		t.Fatal("denied consent accepted")
	}
	if exchanges.Load() != 0 {
		t.Fatal("invalid callback reached exchange")
	}
	s, c = start()
	w := request("GET", "/auth/supabase/callback?code=code&state="+s, session, c, nil)
	if w.Code != 303 {
		t.Fatalf("callback %d %s", w.Code, w.Body.String())
	}
	picker := w.Header().Get("Location")
	if w := request("GET", "/auth/supabase/callback?code=code&state="+s, session, c, nil); w.Code == 303 {
		t.Fatal("replay accepted")
	}
	w = request("GET", picker, session, nil, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Allowed project") || !strings.Contains(w.Body.String(), "Our organization") {
		t.Fatalf("picker %d %s", w.Code, w.Body.String())
	}
	u, _ := url.Parse(picker)
	connection := u.Query().Get("connection")
	if w := request("POST", "/app/supabase/projects", other, nil, url.Values{"connection": {connection}, "ref": {"abcdefghijklmnopqrst"}}); w.Code == 303 {
		t.Fatal("cross-owner connection accepted")
	}
	if w := request("POST", "/app/supabase/projects", session, nil, url.Values{"connection": {connection}, "ref": {"zzzzzzzzzzzzzzzzzzzz"}}); w.Code == 303 {
		t.Fatal("unauthorized project accepted")
	}
	w = request("POST", "/app/supabase/projects", session, nil, url.Values{"connection": {connection}, "ref": {"abcdefghijklmnopqrst"}, "name": {"forged"}, "host": {"evil.example"}})
	if w.Code != 303 {
		t.Fatalf("select %d %s", w.Code, w.Body.String())
	}
	projectPath := w.Header().Get("Location")
	w = request("GET", projectPath, session, nil, nil)
	for _, field := range []string{`name="setup_token"`, `name="ca"`, `name="repair_password"`, `name="ref"`} {
		if strings.Contains(w.Body.String(), field) {
			t.Fatalf("normal setup exposes %s", field)
		}
	}
	var sealed []byte
	if err := pool.QueryRow(ctx, `SELECT secret FROM pooltune_app.supabase_connections WHERE id=$1`, connection).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("refresh-one")) || bytes.Contains(sealed, []byte(strings.Repeat("a", 100))) {
		t.Fatal("grant stored in plaintext")
	}
	if _, err = pool.Exec(ctx, `UPDATE pooltune_app.supabase_connections SET expires_at=now()-interval '1 minute' WHERE id=$1`, connection); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			token, err := app.supabaseToken(context.Background(), user.ID, connection)
			if err != nil || len(token) != 2048 {
				t.Errorf("refresh: %v", err)
			}
		})
	}
	wg.Wait()
	if refreshes.Load() != 1 {
		t.Fatalf("refreshes %d", refreshes.Load())
	}
	_, _ = pool.Exec(ctx, `UPDATE pooltune_app.supabase_connections SET expires_at=now()-interval '1 minute' WHERE id=$1`, connection)
	cancelNextRefresh.Store(true)
	if _, err = app.supabaseToken(refreshContext, user.ID, connection); err != nil {
		t.Fatalf("caller cancellation lost rotated grant: %v", err)
	}
	cancelNextRefresh.Store(false)
	if _, err = app.supabaseToken(ctx, user.ID, connection); err != nil || refreshes.Load() != 2 {
		t.Fatal("rotated token did not persist after caller cancellation")
	}
	t.Run("refresh lock respects setup deadline", func(t *testing.T) {
		blocker, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := blocker.Exec(ctx, `SELECT id FROM pooltune_app.supabase_connections WHERE id=$1 FOR UPDATE`, connection); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() { time.Sleep(150 * time.Millisecond); _ = blocker.Rollback(context.Background()); close(done) }()
		defer func() { <-done }()
		limited, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err = app.supabaseToken(limited, user.ID, connection)
		if err != errSupabaseUnavailable || time.Since(start) > 120*time.Millisecond {
			t.Fatalf("detached refresh exceeded setup deadline: %v, %v", time.Since(start), err)
		}
	})
	if _, err = app.supabaseToken(ctx, user.ID+1, connection); err == nil {
		t.Fatal("cross-owner token access")
	}
	if w := request("GET", "/app/projects/new", session, nil, nil); w.Code != 303 || !strings.Contains(w.Header().Get("Location"), "/app/supabase/projects?") {
		t.Fatal("adding another project requires repeat consent")
	}
	row, err := app.projects.find(ctx, user.ID, strings.TrimPrefix(projectPath, "/app/projects/"))
	if err != nil {
		t.Fatal(err)
	}
	var cached string
	for range 8 {
		wg.Go(func() {
			login, err := app.temporaryLogin(ctx, row, "")
			if err != nil || login.Role != "cli_login_postgres" {
				t.Errorf("temporary access: %v", err)
			}
		})
	}
	wg.Wait()
	if issuances.Load() != 1 {
		t.Fatalf("parallel issuance rotated shared login: %d", issuances.Load())
	}
	login, err := app.temporaryLogin(ctx, row, "")
	if err != nil {
		t.Fatal(err)
	}
	cached = login.Password
	for range 8 {
		wg.Go(func() {
			_, err := app.temporaryLogin(ctx, row, cached)
			if err != nil {
				t.Errorf("stale refresh: %v", err)
			}
		})
	}
	wg.Wait()
	if issuances.Load() != 2 {
		t.Fatalf("stale retry reminted more than once: %d", issuances.Load())
	}
	writeDenied.Store(true)
	if _, err := app.temporaryLogin(ctx, row, ""); err != errSupabaseProjectAccess {
		t.Fatalf("cached native login bypassed database-write downgrade: %v", err)
	}
	writeDenied.Store(false)
	for _, status := range []int32{429, 503} {
		probeStatus.Store(status)
		if _, err := app.temporaryLogin(ctx, row, ""); err != errSupabaseUnavailable {
			t.Fatalf("transient write probe accepted or revoked grant: %v", err)
		}
	}
	probeStatus.Store(0)
	readOnlyGrant, err := app.saveSupabaseGrant(ctx, otherUser.ID, supabaseGrant{Access: "read-only-access", Refresh: "read-only-refresh", ExpiresIn: 3600, Type: "Bearer"})
	if err != nil {
		t.Fatal(err)
	}
	readOnlyID, err := app.projects.create(ctx, otherUser.ID, "Same project, another owner", row.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE pooltune_app.projects SET supabase_connection=$2,supabase_org=$3 WHERE id=$1`, readOnlyID, readOnlyGrant, row.SupabaseOrg); err != nil {
		t.Fatal(err)
	}
	readOnlyRow, err := app.projects.find(ctx, otherUser.ID, readOnlyID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.temporaryLogin(ctx, readOnlyRow, ""); err != errSupabaseProjectAccess {
		t.Fatalf("another account borrowed shared cache without write authority: %v", err)
	}
	if _, err := app.temporaryLogin(ctx, row, ""); err != nil || issuances.Load() != 2 {
		t.Fatalf("denied account destroyed healthy credential cache: %v", err)
	}
	denied.Store(true)
	if _, err = app.temporaryLogin(ctx, row, ""); err == nil {
		t.Fatal("cached native login bypassed provider denial")
	}
	denied.Store(false)
	if _, err = app.supabaseToken(ctx, user.ID, connection); err != nil {
		t.Fatal("single project denial destroyed shared grant")
	}
	_, _ = pool.Exec(ctx, `DELETE FROM pooltune_app.supabase_cli_credentials`)
	malformed.Store(true)
	if _, err = app.temporaryLogin(ctx, row, ""); err == nil {
		t.Fatal("forged provider role accepted")
	}
	malformed.Store(false)
	// Installation epoch changes deny stale connector authority before any issuance.
	staleRow := row
	staleRow.Epoch--
	if _, err = app.temporaryLogin(ctx, staleRow, ""); err == nil {
		t.Fatal("stale installation issued credentials")
	}
	t.Run("slow setup returns retry before response deadline", func(t *testing.T) {
		delayedSetup.Store(true)
		started := time.Now()
		failed := request("POST", projectPath+"/start", session, nil, url.Values{"capability": {"memory"}})
		elapsed := time.Since(started)
		delayedSetup.Store(false)
		if elapsed > 18750*time.Millisecond {
			t.Fatalf("setup exceeded whole-request budget: %v", elapsed)
		}
		if failed.Code != 400 || !strings.Contains(failed.Body.String(), `value="memory"`) {
			t.Fatalf("slow setup lost retry screen: %d", failed.Code)
		}
		writeDenied.Store(true)
		retry := request("POST", projectPath+"/start", session, nil, url.Values{"capability": {"memory"}})
		writeDenied.Store(false)
		if retry.Code != 400 || !strings.Contains(retry.Body.String(), `value="memory"`) || strings.Contains(retry.Body.String(), `name="setup_token"`) {
			t.Fatal("retry lost choices or exposed manual setup")
		}
	})
	// A failed automatic start retains repair choices and never reveals manual fields.
	denied.Store(true)
	failed := request("POST", projectPath+"/start", session, nil, url.Values{"capability": {"memory"}})
	if failed.Code != 400 || !strings.Contains(failed.Body.String(), `value="memory"`) {
		t.Fatal("failed setup lost choices")
	}
	for _, field := range []string{`name="setup_token"`, `name="ca"`, `name="repair_password"`} {
		if strings.Contains(failed.Body.String(), field) {
			t.Fatal("error path exposed manual setup")
		}
	}
	reload := request("GET", projectPath, session, nil, nil)
	if !regexp.MustCompile(`name="capability" value="memory"[^>]* checked`).MatchString(reload.Body.String()) {
		t.Fatal("repair choices lost when retry page reloads")
	}
	denied.Store(false)
	// Disconnect one selected installation without unlinking its shared account grant.
	secret := projectSecrets{Password: "monitor-secret", CA: "encrypted-fixture-ca", Automatic: true, Capabilities: []string{"memory"}}
	sealedProject, err := app.vault.seal(row, secret)
	if err != nil {
		t.Fatal(err)
	}
	if err = app.projects.activate(ctx, row, sealedProject); err != nil {
		t.Fatal(err)
	}
	row, err = app.projects.find(ctx, user.ID, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := app.projects.create(ctx, user.ID, "Other selected project", "bbbbbbbbbbbbbbbbbbbb")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE pooltune_app.projects SET supabase_connection=$2,supabase_org='our-org' WHERE id=$1`, otherID, connection); err != nil {
		t.Fatal(err)
	}
	otherRow, err := app.projects.find(ctx, user.ID, otherID)
	if err != nil {
		t.Fatal(err)
	}
	otherSealed, err := app.vault.seal(otherRow, secret)
	if err != nil {
		t.Fatal(err)
	}
	if err = app.projects.activate(ctx, otherRow, otherSealed); err != nil {
		t.Fatal(err)
	}
	// A blocked connection preserves the installation secret and choices, and explicit selection rebinds it.
	if err = app.blockSupabaseProject(ctx, row); err != nil {
		t.Fatal(err)
	}
	renewed, err := app.saveSupabaseGrant(ctx, user.ID, supabaseGrant{Access: "renewed", Refresh: "renewed-refresh", ExpiresIn: 3600, Type: "Bearer"})
	if err != nil {
		t.Fatal(err)
	}
	reconnect := request("POST", "/app/supabase/projects", session, nil, url.Values{"connection": {renewed}, "ref": {row.Ref}})
	if reconnect.Code != 303 {
		t.Fatalf("explicit reconnect failed: %d %s", reconnect.Code, reconnect.Body.String())
	}
	reconnected, err := app.projects.find(ctx, user.ID, row.ID)
	if err != nil || reconnected.Epoch != row.Epoch+1 || reconnected.SupabaseBlocked || !bytes.Equal(row.Secret, reconnected.Secret) {
		t.Fatalf("reconnect lost state: %+v %v", reconnected, err)
	}
	if err = app.disconnectAfterCleanup(ctx, reconnected, func(context.Context, projectRecord, projectSecrets) error { return nil }); err != nil {
		t.Fatal(err)
	}
	disconnected, err := app.projects.find(ctx, user.ID, row.ID)
	if err != nil || disconnected.Configured || len(disconnected.Secret) > 0 {
		t.Fatal("disconnect kept project credentials")
	}
	activeOther, err := app.projects.find(ctx, user.ID, otherID)
	if err != nil || !activeOther.Configured {
		t.Fatal("disconnect stopped another installation")
	}
	if _, err = app.supabaseToken(ctx, user.ID, connection); err != nil {
		t.Fatal("disconnect revoked shared grant")
	}
	// Terminal refresh denial clears only the rejected grant and leaves recovery state intact.
	secondConnection, err := app.saveSupabaseGrant(ctx, user.ID, supabaseGrant{Access: "another-access", Refresh: "another-refresh", ExpiresIn: 3600, Type: "Bearer"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = pool.Exec(ctx, `UPDATE pooltune_app.supabase_connections SET expires_at=now()-interval '1 minute' WHERE id=$1`, connection)
	// The latest rotated refresh token must be used on the terminal request.
	revoked.Store(true)
	if _, err = app.supabaseToken(ctx, user.ID, connection); err == nil {
		t.Fatal("terminal denial accepted")
	}
	if _, err = app.supabaseToken(ctx, user.ID, secondConnection); err != nil {
		t.Fatal("unrelated grant revoked")
	}
	var removed bool
	if err = pool.QueryRow(ctx, `SELECT revoked AND secret IS NULL FROM pooltune_app.supabase_connections WHERE id=$1`, connection).Scan(&removed); err != nil || !removed {
		t.Fatal("terminal token retained")
	}
}

func TestSupabaseWriteAuthorityCatalogIdentity(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	db, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	check := func(want bool) {
		t.Helper()
		var allowed bool
		if err := db.QueryRow(ctx, supabaseWriteAuthoritySQL).Scan(&allowed); err != nil || allowed != want {
			t.Fatalf("write authority=%v, want %v: %v", allowed, want, err)
		}
	}
	check(false) // A superuser is never the supported provider identity.
	if _, err := db.Exec(ctx, `CREATE ROLE authority_control LOGIN SUPERUSER PASSWORD 'authority-control'`); err != nil {
		t.Fatal(err)
	}
	controlConfig := config.Copy()
	controlConfig.User = "authority_control"
	controlConfig.Password = "authority-control"
	control, err := pgx.ConnectConfig(ctx, controlConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close(ctx)
	db = control
	if _, err := db.Exec(ctx, `ALTER ROLE postgres RENAME TO bootstrap_admin; CREATE ROLE postgres LOGIN; CREATE ROLE supabase_read_only_user; SET ROLE supabase_read_only_user`, pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatal(err)
	}
	check(false)
	if _, err := db.Exec(ctx, `RESET ROLE;SET ROLE postgres`, pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatal(err)
	}
	check(true)
	tx, err := db.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	var allowed bool
	if err := tx.QueryRow(ctx, supabaseWriteAuthoritySQL).Scan(&allowed); err != nil || allowed {
		t.Fatalf("read-only owner accepted: %v", err)
	}
	_ = tx.Rollback(ctx)
}
