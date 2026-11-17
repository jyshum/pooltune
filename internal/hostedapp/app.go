// Package hostedapp serves the account-owned Pooltune monitoring preview.
package hostedapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"pooltune/internal/hostedapp/auth"
	"pooltune/internal/hostedquery"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed ui/*.html ui/*.css ui/*.js
var frontend embed.FS

type Config struct {
	Origin, InviteCode                     string
	GoogleClientID, GoogleClientSecret     string
	SupabaseClientID, SupabaseClientSecret string
	Key                                    []byte
	Sampling                               hostedquery.SamplingPolicy
	QueueInterval                          time.Duration
	Investigate                            hostedquery.EnrolledLookupInvestigator
	InvestigateRepair                      hostedquery.IndexInvestigator
	OwnedIndex                             *hostedquery.OwnedProjectIndex
	OwnedRepair                            *hostedquery.OwnedProjectRepair
	OwnedCron                              *hostedquery.OwnedProjectCron
}

type App struct {
	projects          projectStore
	accounts          *auth.Store
	google            *googleOIDC
	supabase          *supabaseOAuth
	vault             vault
	policy            hostedquery.SamplingPolicy
	queueInterval     time.Duration
	investigate       hostedquery.EnrolledLookupInvestigator
	investigateRepair hostedquery.IndexInvestigator
	ownedIndex        *hostedquery.OwnedProjectIndex
	ownedRepair       *hostedquery.OwnedProjectRepair
	ownedCron         *hostedquery.OwnedProjectCron
	origin            *url.URL
	invite            [32]byte
	cookie            string
	templates         *template.Template
	handler           http.Handler
	authQuota         requestQuota
	workerMu          sync.Mutex
	workerStops       map[string]context.CancelFunc
}

type requestQuota struct {
	mu    sync.Mutex
	until time.Time
	used  int
}

func (q *requestQuota) take() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	if !now.Before(q.until) {
		q.until, q.used = now.Add(time.Minute), 0
	}
	if q.used >= 40 {
		return false
	}
	q.used++
	return true
}

func New(ctx context.Context, db *pgxpool.Pool, cfg Config) (*App, error) {
	origin, err := url.Parse(cfg.Origin)
	if err != nil || origin.Hostname() == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, errors.New("POOLTUNE_APP_ORIGIN must be the dashboard origin without a path")
	}
	host := origin.Hostname()
	local := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if origin.Scheme != "https" && !(local && origin.Scheme == "http") {
		return nil, errors.New("the dashboard requires HTTPS outside loopback")
	}
	if len(cfg.InviteCode) < 16 || len(cfg.InviteCode) > 256 {
		return nil, errors.New("set POOLTUNE_SIGNUP_CODE to a private code of 16 to 256 bytes")
	}
	if cfg.Sampling.Interval <= 0 || cfg.Sampling.Window <= 0 || cfg.QueueInterval < 500*time.Millisecond {
		return nil, errors.New("invalid preview monitoring intervals")
	}
	vault, err := newVault(cfg.Key)
	if err != nil {
		return nil, err
	}
	templates, err := template.ParseFS(frontend, "ui/*.html")
	if err != nil {
		return nil, err
	}
	conn, err := db.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	err = hostedquery.PrepareWorkerState(ctx, conn.Conn())
	if err == nil {
		err = hostedquery.PrepareManagedCronState(ctx, conn.Conn())
	}
	if err == nil && cfg.OwnedCron != nil {
		err = hostedquery.PrepareOwnedCronCheckpoints(ctx, conn.Conn())
	}
	conn.Release()
	if err != nil {
		return nil, err
	}
	google, err := newGoogleOIDC(cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.Origin)
	if err != nil {
		return nil, err
	}
	supabase, err := newSupabaseOAuth(cfg.SupabaseClientID, cfg.SupabaseClientSecret, cfg.Origin)
	if err != nil {
		return nil, err
	}
	accounts, err := auth.New(ctx, db)
	if err != nil {
		return nil, err
	}
	a := &App{
		projects: projectStore{db}, accounts: accounts, google: google, supabase: supabase, vault: vault, policy: cfg.Sampling,
		queueInterval: cfg.QueueInterval, investigate: cfg.Investigate,
		investigateRepair: cfg.InvestigateRepair,
		ownedIndex:        cfg.OwnedIndex, ownedRepair: cfg.OwnedRepair, ownedCron: cfg.OwnedCron,
		origin: origin, invite: sha256.Sum256([]byte(cfg.InviteCode)), templates: templates, cookie: "__Host-pooltune",
	}
	if origin.Scheme == "http" {
		a.cookie = "pooltune_preview"
	}
	if err := a.projects.prepare(ctx); err != nil {
		return nil, err
	}
	if err := a.prepareSupabase(ctx); err != nil {
		return nil, err
	}
	a.handler = http.NewCrossOriginProtection().Handler(a.routes())
	return a, nil
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if a.projects.db.Ping(ctx) != nil {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if !strings.EqualFold(r.Host, a.origin.Host) {
		http.Error(w, "unrecognized host", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; script-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	if a.google != nil {
		w.Header().Set("Content-Security-Policy", strings.Replace(w.Header().Get("Content-Security-Policy"), "form-action 'self'", "form-action 'self' https://accounts.google.com", 1))
	}
	if a.supabase != nil {
		w.Header().Set("Content-Security-Policy", strings.Replace(w.Header().Get("Content-Security-Policy"), "form-action 'self'", "form-action 'self' https://api.supabase.com https://supabase.com", 1))
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	if a.origin.Scheme == "https" {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
	}
	if !strings.HasPrefix(r.URL.Path, "/assets/") {
		w.Header().Set("Cache-Control", "no-store")
	}
	if r.Method == http.MethodPost {
		if origin := r.Header.Get("Origin"); origin != "" && origin != a.origin.String() {
			http.Error(w, "unrecognized origin", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	}
	a.handler.ServeHTTP(w, r)
}

func (a *App) routes() *http.ServeMux {
	mux := http.NewServeMux()
	assets, _ := fs.Sub(frontend, "ui")
	files := http.StripPrefix("/assets/", http.FileServerFS(assets))
	mux.Handle("GET /assets/app.css", files)
	mux.Handle("GET /assets/app.js", files)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		user, _ := a.session(r)
		a.render(w, http.StatusOK, "home", Page{Title: "Pooltune", User: user.Email})
	})
	mux.HandleFunc("GET /signup", func(w http.ResponseWriter, r *http.Request) { a.accountPage(w, r, "signup") })
	mux.HandleFunc("POST /signup", func(w http.ResponseWriter, r *http.Request) { a.signIn(w, r, true) })
	mux.HandleFunc("GET /signin", func(w http.ResponseWriter, r *http.Request) { a.accountPage(w, r, "signin") })
	mux.HandleFunc("POST /signin", func(w http.ResponseWriter, r *http.Request) { a.signIn(w, r, false) })
	mux.HandleFunc("POST /signout", a.signOut)
	mux.HandleFunc("POST /auth/google/start", a.googleStart)
	mux.HandleFunc("GET /auth/google/callback", a.googleCallback)
	mux.HandleFunc("POST /auth/supabase/start", a.requireAccount(a.supabaseStart))
	mux.HandleFunc("GET /auth/supabase/callback", a.requireAccount(a.supabaseCallback))
	mux.HandleFunc("GET /app/supabase/projects", a.requireAccount(a.supabasePicker))
	mux.HandleFunc("POST /app/supabase/projects", a.requireAccount(a.selectSupabaseProject))
	mux.HandleFunc("GET /app/projects/manual", a.requireAccount(func(w http.ResponseWriter, r *http.Request, u auth.User) {
		a.render(w, 200, "new-project", Page{Title: "Manual connection", User: u.Email})
	}))
	mux.HandleFunc("GET /app/account", a.requireAccount(a.accountSettings))
	mux.HandleFunc("GET /app", a.requireAccount(a.dashboard))
	mux.HandleFunc("GET /app/projects/new", a.requireAccount(a.newProject))
	mux.HandleFunc("POST /app/projects/new", a.requireAccount(a.createProject))
	mux.HandleFunc("GET /app/projects/{id}", a.requireAccount(a.project))
	mux.HandleFunc("GET /app/projects/{id}/activity", a.requireAccount(a.downloadActivity))
	mux.HandleFunc("POST /app/projects/{id}/connect", a.requireAccount(a.connectProject))
	mux.HandleFunc("POST /app/projects/{id}/start", a.requireAccount(a.startProject))
	mux.HandleFunc("POST /app/projects/{id}/monitoring-token", a.requireAccount(a.replaceMonitoringToken))
	mux.HandleFunc("POST /app/projects/{id}/repair-mode", a.requireAccount(a.changeRepairMode))
	mux.HandleFunc("GET /app/projects/{id}/repair-enrollment", a.requireAccount(a.repairEnrollmentPage))
	mux.HandleFunc("POST /app/projects/{id}/repair-enrollment", a.requireAccount(a.changeRepairEnrollment))
	mux.HandleFunc("POST /app/projects/{id}/repair-worker-key", a.requireAccount(a.changeRepairWorkerKey))
	mux.HandleFunc("GET /api/projects/{id}/repair-worker", a.repairWorker)
	mux.HandleFunc("POST /api/projects/{id}/repair-worker", a.repairWorker)
	mux.HandleFunc("POST /app/projects/{id}/disconnect", a.requireAccount(a.disconnectProject))
	return mux
}

func (a *App) render(w http.ResponseWriter, status int, name string, data Page) {
	data.GoogleEnabled = a.google != nil
	var body bytes.Buffer
	if err := a.templates.ExecuteTemplate(&body, name, data); err != nil {
		http.Error(w, "This page cannot be rendered right now.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = body.WriteTo(w)
}

func (a *App) session(r *http.Request) (auth.User, error) {
	cookie, err := r.Cookie(a.cookie)
	if err != nil {
		return auth.User{}, auth.ErrInvalidCredentials
	}
	return a.accounts.UserBySession(r.Context(), cookie.Value)
}

func (a *App) requireAccount(handler func(http.ResponseWriter, *http.Request, auth.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := a.session(r)
		if err != nil {
			http.Redirect(w, r, "/signin", http.StatusSeeOther)
			return
		}
		handler(w, r, user)
	}
}

func (a *App) accountPage(w http.ResponseWriter, r *http.Request, page string) {
	if _, err := a.session(r); err == nil {
		http.Redirect(w, r, "/app", http.StatusSeeOther)
		return
	}
	a.render(w, http.StatusOK, page, accountPage(page, ""))
}

func accountPage(page, failure string) Page {
	title := "Sign in"
	if page == "signup" {
		title = "Create an account"
	}
	return Page{Title: title, Invite: page == "signup", Error: failure}
}

func (a *App) setSession(w http.ResponseWriter, session string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: a.cookie, Value: session, Path: "/", SameSite: http.SameSiteLaxMode,
		Secure: a.origin.Scheme == "https", HttpOnly: true, MaxAge: maxAge,
	})
}

func (a *App) signIn(w http.ResponseWriter, r *http.Request, creating bool) {
	page := "signin"
	if creating {
		page = "signup"
	}
	if r.ParseForm() != nil {
		a.render(w, http.StatusBadRequest, page, accountPage(page, "Check the form and try again."))
		return
	}
	if !a.authQuota.take() {
		a.render(w, http.StatusTooManyRequests, page, accountPage(page, "Try again in a minute."))
		return
	}
	email, password := r.PostForm.Get("email"), r.PostForm.Get("password")
	var session string
	var err error
	if creating {
		invite := sha256.Sum256([]byte(r.PostForm.Get("invite_code")))
		if subtle.ConstantTimeCompare(invite[:], a.invite[:]) != 1 {
			a.render(w, http.StatusForbidden, page, accountPage(page, "Enter a valid preview invite code."))
			return
		}
		_, session, err = a.accounts.Register(r.Context(), email, password)
	} else {
		_, session, err = a.accounts.SignIn(r.Context(), email, password)
	}
	if err != nil {
		message := "The email or password could not be verified. Try again."
		if creating {
			message = "Use a valid email address and a password with at least 15 characters. Sign in if you already have an account."
		}
		a.render(w, http.StatusBadRequest, page, accountPage(page, message))
		return
	}
	if existing, err := r.Cookie(a.cookie); err == nil {
		_ = a.accounts.SignOut(r.Context(), existing.Value)
	}
	a.setSession(w, session, int(auth.SessionTTL/time.Second))
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

func (a *App) signOut(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(a.cookie); err == nil {
		if err := a.accounts.SignOut(r.Context(), cookie.Value); err != nil {
			http.Error(w, "Could not close the server session. Try again.", http.StatusServiceUnavailable)
			return
		}
	}
	a.setSession(w, "", -1)
	http.Redirect(w, r, "/signin", http.StatusSeeOther)
}

func (a *App) newProject(w http.ResponseWriter, r *http.Request, user auth.User) {
	if a.supabase != nil {
		var connection string
		if a.projects.db.QueryRow(r.Context(), `SELECT id FROM pooltune_app.supabase_connections WHERE owner_id=$1 AND NOT revoked ORDER BY created_at DESC LIMIT 1`, user.ID).Scan(&connection) == nil {
			http.Redirect(w, r, "/app/supabase/projects?connection="+url.QueryEscape(connection), 303)
			return
		}
		a.render(w, 200, "supabase-connect", Page{Title: "Connect Supabase", User: user.Email})
		return
	}
	a.render(w, http.StatusOK, "new-project", Page{Title: "Connect a project", User: user.Email})
}

func (a *App) createProject(w http.ResponseWriter, r *http.Request, user auth.User) {
	if r.ParseForm() != nil {
		a.render(w, http.StatusBadRequest, "new-project", Page{Title: "Connect a project", User: user.Email, Error: "Check the form and try again."})
		return
	}
	id, err := a.projects.create(r.Context(), user.ID, r.PostForm.Get("name"), r.PostForm.Get("ref"))
	if err != nil {
		message := "Could not create this project. Try again."
		var pgErr *pgconn.PgError
		switch {
		case errors.As(err, &pgErr) && pgErr.Code == "23505":
			message = "This Supabase project may already be in your dashboard."
		case errors.Is(err, errProjectCapacity), errors.Is(err, errProjectName), errors.Is(err, errProjectRef):
			message = err.Error()
		}
		a.render(w, http.StatusBadRequest, "new-project", Page{Title: "Connect a project", User: user.Email, Error: message})
		return
	}
	http.Redirect(w, r, "/app/projects/"+id, http.StatusSeeOther)
}

func (a *App) ownedProject(w http.ResponseWriter, r *http.Request, user auth.User) (projectRecord, bool) {
	row, err := a.projects.find(r.Context(), user.ID, r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return row, false
	}
	if err != nil {
		http.Error(w, "Could not load this project.", http.StatusServiceUnavailable)
		return row, false
	}
	return row, true
}

func (a *App) connectProject(w http.ResponseWriter, r *http.Request, user auth.User) {
	row, ok := a.ownedProject(w, r, user)
	if !ok {
		return
	}
	if row.Configured {
		http.Redirect(w, r, "/app/projects/"+row.ID, http.StatusSeeOther)
		return
	}
	if r.ParseForm() != nil {
		a.showProject(w, r, user, row, "Check the form and try again.")
		return
	}
	var selected []string
	if project, err := row.identity(); err == nil {
		operation := ""
		if a.ownedIndex.Matches(project) && !a.ownedIndex.Discover {
			operation = "index"
		} else if a.ownedRepair.Matches(project) && !a.ownedRepair.Automatic {
			operation = a.ownedRepair.Operation
		} else if a.ownedCron.Matches(project) && a.ownedCron.QueryID != 0 {
			operation = "cron"
		}
		if operation != "" {
			requested, err := capabilityChoices(r.PostForm)
			if err != nil {
				a.showProject(w, r, user, row, err.Error())
				return
			}
			if slices.Contains(requested, operation) {
				selected = []string{operation}
			}
		}
	}
	credentials := projectSecrets{
		Password: r.PostForm.Get("password"), CA: strings.TrimSpace(r.PostForm.Get("ca")), LogsToken: strings.TrimSpace(r.PostForm.Get("logs_token")),
		Capabilities: selected,
	}
	if err := checkProject(r.Context(), row, credentials, a.policy, false); err != nil {
		a.showProject(w, r, user, row, err.Error())
		return
	}
	sealed, err := a.vault.seal(row, credentials)
	if err == nil {
		err = a.projects.activate(r.Context(), row, sealed)
	}
	if err != nil {
		message := "Could not activate this project. Try again."
		if errors.Is(err, errProjectCapacity) || errors.Is(err, errProjectActive) {
			message = err.Error()
		}
		a.showProject(w, r, user, row, message)
		return
	}
	http.Redirect(w, r, "/app/projects/"+row.ID, http.StatusSeeOther)
}

func (a *App) disconnectProject(w http.ResponseWriter, r *http.Request, user auth.User) {
	row, ok := a.ownedProject(w, r, user)
	if !ok {
		return
	}
	if err := a.disconnectAfterCleanup(r.Context(), row, a.cleanupProjectWitnesses); err != nil {
		fresh, lookupErr := a.projects.find(r.Context(), user.ID, row.ID)
		if lookupErr == nil {
			row = fresh
		}
		a.showProject(w, r, user, row, "Repairs are stopped. Temporary scheduler cleanup is still pending. Retry disconnect to finish removing it; the encrypted credential is retained only for cleanup.")
		return
	}
	http.Redirect(w, r, "/app/projects/"+row.ID, http.StatusSeeOther)
}
