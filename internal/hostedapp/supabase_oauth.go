package hostedapp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/hostedapp/auth"
	"pooltune/internal/supabaseproject"
)

var errSupabaseReconnect = errors.New("Supabase authorization expired or was revoked. Connect Supabase again")
var errSupabaseProjectAccess = errors.New("Supabase did not authorize this project. Check organization access and reconnect")
var errSupabaseUnavailable = errors.New("Supabase is temporarily unavailable. Retry in a moment")

type supabaseOAuth struct {
	id, secret, redirect, endpoint string
	client                         *http.Client
}
type supabaseGrant struct {
	Access    string `json:"access_token"`
	Refresh   string `json:"refresh_token"`
	ExpiresIn int64  `json:"expires_in"`
	Type      string `json:"token_type"`
}
type SupabaseProject struct {
	Ref              string `json:"ref"`
	Name             string `json:"name"`
	Organization     string `json:"organization_slug"`
	Status           string `json:"status"`
	OrganizationName string
	Database         struct {
		Host string `json:"host"`
	} `json:"database"`
}

func newSupabaseOAuth(id, secret, origin string) (*supabaseOAuth, error) {
	if id == "" && secret == "" {
		return nil, nil
	}
	if strings.TrimSpace(id) == "" || strings.TrimSpace(secret) == "" {
		return nil, errors.New("set both POOLTUNE_SUPABASE_CLIENT_ID and POOLTUNE_SUPABASE_CLIENT_SECRET")
	}
	return &supabaseOAuth{id: id, secret: secret, redirect: origin + "/auth/supabase/callback", endpoint: "https://api.supabase.com", client: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func randomSupabaseState() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func digestBytes(s string) []byte { v := sha256.Sum256([]byte(s)); return v[:] }
func validSupabaseToken(s string) bool {
	return len(s) > 0 && len(s) <= 16384 && !strings.ContainsAny(s, " \t\r\n\x00")
}
func (s *supabaseOAuth) request(ctx context.Context, method, path, token, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, s.endpoint+path, body)
	if err != nil {
		return errSupabaseUnavailable
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else {
		req.SetBasicAuth(s.id, s.secret)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return errSupabaseUnavailable
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return errSupabaseUnavailable
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if path == "/v1/oauth/token" {
			var denial struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(data, &denial)
			if denial.Error == "invalid_grant" || resp.StatusCode == 401 || resp.StatusCode == 403 {
				return errSupabaseReconnect
			}
		} else if resp.StatusCode == 401 || resp.StatusCode == 403 {
			return errSupabaseProjectAccess
		}
		return errSupabaseUnavailable
	}
	if json.Unmarshal(data, out) != nil {
		return errSupabaseUnavailable
	}
	return nil
}
func (s *supabaseOAuth) exchange(ctx context.Context, values url.Values) (supabaseGrant, error) {
	var g supabaseGrant
	err := s.request(ctx, "POST", "/v1/oauth/token", "", "application/x-www-form-urlencoded", strings.NewReader(values.Encode()), &g)
	if err != nil {
		return g, err
	}
	if !validSupabaseToken(g.Access) || !validSupabaseToken(g.Refresh) || !strings.EqualFold(g.Type, "Bearer") || g.ExpiresIn < 1 || g.ExpiresIn > 86400*30 {
		return g, errSupabaseUnavailable
	}
	return g, nil
}
func (s *supabaseOAuth) projects(ctx context.Context, token string) ([]SupabaseProject, error) {
	var projects []SupabaseProject
	if err := s.request(ctx, "GET", "/v1/projects", token, "", nil, &projects); err != nil {
		return nil, err
	}
	var orgs []struct{ Slug, Name string }
	if err := s.request(ctx, "GET", "/v1/organizations", token, "", nil, &orgs); err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, o := range orgs {
		if len(o.Slug) > 256 || len(o.Name) > 256 {
			return nil, errSupabaseUnavailable
		}
		names[o.Slug] = o.Name
	}
	seen := map[string]bool{}
	for i, p := range projects {
		if _, err := supabaseproject.New(p.Ref, "0123456789abcdef"); err != nil || seen[p.Ref] || len(p.Name) == 0 || len(p.Name) > 100 || p.Organization == "" || len(p.Organization) > 256 || p.Database.Host != "db."+p.Ref+".supabase.co" || names[p.Organization] == "" {
			return nil, errSupabaseUnavailable
		}
		seen[p.Ref] = true
		projects[i].OrganizationName = names[p.Organization]
	}
	return projects, nil
}
func (a *App) prepareSupabase(ctx context.Context) error {
	return pgx.BeginFunc(ctx, a.projects.db, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(761994345012);CREATE TABLE IF NOT EXISTS pooltune_app.supabase_flows(state bytea PRIMARY KEY, browser bytea NOT NULL, session bytea NOT NULL, owner_id bigint NOT NULL REFERENCES pooltune_app.users(id), verifier text NOT NULL, expires_at timestamptz NOT NULL);
 CREATE TABLE IF NOT EXISTS pooltune_app.supabase_connections(id text PRIMARY KEY,owner_id bigint NOT NULL REFERENCES pooltune_app.users(id),secret bytea,expires_at timestamptz NOT NULL,revoked boolean NOT NULL DEFAULT false,created_at timestamptz NOT NULL DEFAULT now(),UNIQUE(id,owner_id));
 ALTER TABLE pooltune_app.projects ADD COLUMN IF NOT EXISTS supabase_connection text;
 ALTER TABLE pooltune_app.projects ADD COLUMN IF NOT EXISTS supabase_org text NOT NULL DEFAULT '';
 ALTER TABLE pooltune_app.projects ADD COLUMN IF NOT EXISTS supabase_blocked boolean NOT NULL DEFAULT false;
 ALTER TABLE pooltune_app.projects ADD COLUMN IF NOT EXISTS setup_capabilities text[] NOT NULL DEFAULT ARRAY[]::text[];
 CREATE TABLE IF NOT EXISTS pooltune_app.supabase_cli_credentials(ref text PRIMARY KEY,secret bytea NOT NULL,expires_at timestamptz NOT NULL);`)
		return err
	})
}
func supabaseBinding(owner int64, id string) []byte {
	return []byte(fmt.Sprintf("pooltune/supabase/v1/%d/%s", owner, id))
}
func (a *App) saveSupabaseGrant(ctx context.Context, owner int64, g supabaseGrant) (string, error) {
	id := randomSupabaseState()
	plain, err := json.Marshal(g)
	if err != nil {
		return "", err
	}
	sealed := a.vault.Seal(nil, nil, plain, supabaseBinding(owner, id))
	_, err = a.projects.db.Exec(ctx, `INSERT INTO pooltune_app.supabase_connections(id,owner_id,secret,expires_at) VALUES($1,$2,$3,$4)`, id, owner, sealed, time.Now().Add(time.Duration(g.ExpiresIn)*time.Second))
	return id, err
}

// A setup has an 18-second work budget. Only local persistence of an already
// completed exchange may use this reserve, leaving response time before 22 seconds.
const supabaseRefreshPersistenceReserve = 2 * time.Second

func (a *App) supabaseToken(ctx context.Context, owner int64, id string) (string, error) {
	if a.supabase == nil {
		return "", errSupabaseReconnect
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Caller cancellation cannot discard an in-flight rotation, but external
	// work must still finish by the earlier caller deadline or 12-second limit.
	deadline := time.Now().Add(12 * time.Second)
	if parentDeadline, ok := ctx.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	work, cancelWork := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	defer cancelWork()
	settlement, cancelSettlement := context.WithDeadline(context.WithoutCancel(ctx), deadline.Add(supabaseRefreshPersistenceReserve))
	defer cancelSettlement()
	tx, err := a.projects.db.Begin(work)
	if err != nil {
		return "", errSupabaseUnavailable
	}
	defer func() { _ = tx.Rollback(settlement) }()
	var sealed []byte
	var expiry time.Time
	var revoked bool
	if err := tx.QueryRow(work, `SELECT secret,expires_at,revoked FROM pooltune_app.supabase_connections WHERE id=$1 AND owner_id=$2 FOR UPDATE`, id, owner).Scan(&sealed, &expiry, &revoked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", errSupabaseReconnect
		}
		return "", errSupabaseUnavailable
	}
	if revoked {
		return "", errSupabaseReconnect
	}
	plain, err := a.vault.Open(nil, nil, sealed, supabaseBinding(owner, id))
	if err != nil {
		return "", errSupabaseReconnect
	}
	var g supabaseGrant
	if json.Unmarshal(plain, &g) != nil {
		return "", errSupabaseReconnect
	}
	if time.Now().Add(time.Minute).Before(expiry) {
		if err := tx.Commit(work); err != nil {
			return "", errSupabaseUnavailable
		}
		return g.Access, nil
	}
	g, err = a.supabase.exchange(work, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {g.Refresh}})
	terminal := errors.Is(err, errSupabaseReconnect)
	if err != nil && !terminal {
		return "", err
	}
	// The provider has answered. Keep the row lock until its replacement token
	// (or terminal revocation) is durable, even if the request deadline just passed.
	// This context is never used for HTTP, and UPDATE plus COMMIT share its budget.
	persist, cancelPersist := context.WithTimeout(settlement, supabaseRefreshPersistenceReserve)
	defer cancelPersist()
	if terminal {
		_, err = tx.Exec(persist, `UPDATE pooltune_app.supabase_connections SET revoked=true,secret=NULL WHERE id=$1 AND owner_id=$2`, id, owner)
	} else {
		plain, err = json.Marshal(g)
		if err != nil {
			return "", errSupabaseUnavailable
		}
		sealed = a.vault.Seal(nil, nil, plain, supabaseBinding(owner, id))
		_, err = tx.Exec(persist, `UPDATE pooltune_app.supabase_connections SET secret=$3,expires_at=$4 WHERE id=$1 AND owner_id=$2`, id, owner, sealed, time.Now().Add(time.Duration(g.ExpiresIn)*time.Second))
	}
	if err != nil {
		return "", errSupabaseUnavailable
	}
	if err := tx.Commit(persist); err != nil {
		return "", errSupabaseUnavailable
	}
	if terminal {
		return "", errSupabaseReconnect
	}
	return g.Access, nil
}
func (a *App) supabaseCookie(w http.ResponseWriter, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: a.cookie + "_supabase", Value: value, Path: "/", Secure: a.origin.Scheme == "https", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: age})
}
func (a *App) supabaseFailure(w http.ResponseWriter, user auth.User, status int, err error) {
	a.render(w, status, "supabase-connect", Page{Title: "Connect Supabase", User: user.Email, Error: err.Error()})
}
func (a *App) supabaseStart(w http.ResponseWriter, r *http.Request, user auth.User) {
	if a.supabase == nil {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Origin") != a.origin.String() {
		http.Error(w, "unrecognized origin", 403)
		return
	}
	if !a.authQuota.take() {
		http.Error(w, "Try again in a minute.", 429)
		return
	}
	session, _ := r.Cookie(a.cookie)
	state, browser, verifier := randomSupabaseState(), randomSupabaseState(), randomSupabaseState()
	_, err := a.projects.db.Exec(r.Context(), `WITH expired AS(DELETE FROM pooltune_app.supabase_flows WHERE expires_at<now()) INSERT INTO pooltune_app.supabase_flows VALUES($1,$2,$3,$4,$5,now()+interval '10 minutes')`, digestBytes(state), digestBytes(browser), digestBytes(session.Value), user.ID, verifier)
	if err != nil {
		a.supabaseFailure(w, user, 503, errSupabaseUnavailable)
		return
	}
	a.supabaseCookie(w, browser, 600)
	q := url.Values{"client_id": {a.supabase.id}, "redirect_uri": {a.supabase.redirect}, "response_type": {"code"}, "state": {state}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(digestBytes(verifier))}}
	http.Redirect(w, r, "https://api.supabase.com/v1/oauth/authorize?"+q.Encode(), 303)
}
func (a *App) supabaseCallback(w http.ResponseWriter, r *http.Request, user auth.User) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	if a.supabase == nil {
		http.NotFound(w, r)
		return
	}
	browser, err := r.Cookie(a.cookie + "_supabase")
	if err != nil {
		a.supabaseFailure(w, user, 400, errSupabaseReconnect)
		return
	}
	session, _ := r.Cookie(a.cookie)
	var verifier string
	err = a.projects.db.QueryRow(r.Context(), `DELETE FROM pooltune_app.supabase_flows WHERE state=$1 AND browser=$2 RETURNING CASE WHEN owner_id=$3 AND session=$4 AND expires_at>now() THEN verifier ELSE '' END`, digestBytes(r.URL.Query().Get("state")), digestBytes(browser.Value), user.ID, digestBytes(session.Value)).Scan(&verifier)
	a.supabaseCookie(w, "", -1)
	code := r.URL.Query().Get("code")
	if err != nil || verifier == "" || code == "" || len(code) > 4096 || r.URL.Query().Get("error") != "" {
		a.supabaseFailure(w, user, 400, errSupabaseReconnect)
		return
	}
	grant, err := a.supabase.exchange(r.Context(), url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {a.supabase.redirect}})
	if err != nil {
		a.supabaseFailure(w, user, 400, err)
		return
	}
	// Revalidate the live account session after the network exchange as well.
	fresh, err := a.session(r)
	if err != nil || fresh.ID != user.ID {
		a.supabaseFailure(w, user, 400, errSupabaseReconnect)
		return
	}
	id, err := a.saveSupabaseGrant(r.Context(), user.ID, grant)
	if err != nil {
		a.supabaseFailure(w, user, 503, errSupabaseUnavailable)
		return
	}
	http.Redirect(w, r, "/app/supabase/projects?connection="+url.QueryEscape(id), 303)
}
func (a *App) authorizedSupabaseProjects(ctx context.Context, owner int64, id string) ([]SupabaseProject, error) {
	token, err := a.supabaseToken(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	return a.supabase.projects(ctx, token)
}
func (a *App) supabasePicker(w http.ResponseWriter, r *http.Request, user auth.User) {
	id := r.URL.Query().Get("connection")
	projects, err := a.authorizedSupabaseProjects(r.Context(), user.ID, id)
	if err != nil {
		a.supabaseFailure(w, user, 400, err)
		return
	}
	a.render(w, 200, "supabase-picker", Page{Title: "Choose a Supabase project", User: user.Email, SupabaseProjects: projects, SupabaseConnection: id})
}
func (a *App) selectSupabaseProject(w http.ResponseWriter, r *http.Request, user auth.User) {
	if r.ParseForm() != nil {
		a.supabaseFailure(w, user, 400, errSupabaseProjectAccess)
		return
	}
	id := r.PostForm.Get("connection")
	projects, err := a.authorizedSupabaseProjects(r.Context(), user.ID, id)
	if err != nil {
		a.supabaseFailure(w, user, 400, err)
		return
	}
	var selected *SupabaseProject
	for _, p := range projects {
		if p.Ref == r.PostForm.Get("ref") && p.Status == "ACTIVE_HEALTHY" {
			selected = &p
			break
		}
	}
	if selected == nil {
		a.supabaseFailure(w, user, 400, errSupabaseProjectAccess)
		return
	}
	// Project metadata comes only from the freshly authorized provider response.
	projectID, err := a.projects.create(r.Context(), user.ID, selected.Name, selected.Ref)
	if err != nil {
		var existing projectRecord
		err = a.projects.db.QueryRow(r.Context(), `SELECT id,epoch,supabase_org,supabase_blocked FROM pooltune_app.projects WHERE owner_id=$1 AND ref=$2`, user.ID, selected.Ref).Scan(&existing.ID, &existing.Epoch, &existing.SupabaseOrg, &existing.SupabaseBlocked)
		if err != nil || existing.Epoch > 0 && !existing.SupabaseBlocked || existing.SupabaseOrg != "" && existing.SupabaseOrg != selected.Organization {
			a.supabaseFailure(w, user, 400, errors.New("This project is already connected. Open it from your dashboard"))
			return
		}
		projectID = existing.ID
		if existing.Epoch > 0 {
			existing.Owner = user.ID
			existing.Ref = selected.Ref
			identity, _ := existing.identity()
			err = pgx.BeginFunc(r.Context(), a.projects.db, func(tx pgx.Tx) error {
				tag, err := tx.Exec(r.Context(), `UPDATE pooltune_app.projects SET supabase_connection=$3,supabase_blocked=false,epoch=epoch+1 WHERE id=$1 AND owner_id=$2 AND epoch=$4 AND supabase_org=$5 AND supabase_blocked`, projectID, user.ID, id, existing.Epoch, selected.Organization)
				if err != nil {
					return err
				}
				if tag.RowsAffected() != 1 {
					return errSupabaseProjectAccess
				}
				tag, err = tx.Exec(r.Context(), `UPDATE pooltune_worker.projects SET monitor_epoch=monitor_epoch+1 WHERE project_id=$1 AND monitor_epoch=$2`, identity.ID(), existing.Epoch)
				if err != nil {
					return err
				}
				if tag.RowsAffected() != 1 {
					return errSupabaseProjectAccess
				}
				return nil
			})
			if err != nil {
				a.supabaseFailure(w, user, 400, errSupabaseProjectAccess)
				return
			}
			http.Redirect(w, r, "/app/projects/"+projectID, 303)
			return
		}
	}
	tag, err := a.projects.db.Exec(r.Context(), `UPDATE pooltune_app.projects SET supabase_connection=$3,supabase_org=$4,supabase_blocked=false WHERE id=$1 AND owner_id=$2 AND epoch<=0`, projectID, user.ID, id, selected.Organization)
	if err != nil || tag.RowsAffected() != 1 {
		a.supabaseFailure(w, user, 503, errSupabaseUnavailable)
		return
	}
	http.Redirect(w, r, "/app/projects/"+projectID, 303)
}
