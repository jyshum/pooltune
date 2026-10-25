package admin

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	Origin    string
	LocalHTTP bool
}
type SetupDraft struct {
	Provider      string `json:"provider"`
	Path          string `json:"path"`
	TargetID      string `json:"target_id"`
	ConnectionRef string `json:"connection_ref"`
}
type GroupChoice struct {
	WorkloadID string `json:"workload_id"`
	Class      string `json:"class"`
	Locked     bool   `json:"locked"`
}
type Command struct {
	ExpectedRevision    string        `json:"expected_revision"`
	IdempotencyKey      string        `json:"idempotency_key"`
	Setup               *SetupDraft   `json:"setup,omitempty"`
	Groups              []GroupChoice `json:"groups,omitempty"`
	Revision            string        `json:"revision,omitempty"`
	RuleID              string        `json:"rule_id,omitempty"`
	Locked              bool          `json:"locked,omitempty"`
	SemanticPreviewHash string        `json:"semantic_preview_hash,omitempty"`
}

// Backend returns redacted owner-facing DTOs only. Execute must atomically check
// expected revision, persist intent and apply through the shared coordinator.
// Authentication alone never authorizes a stale edit or direct runtime mutation.
type Backend interface {
	Read(context.Context, string) (any, error)
	Execute(context.Context, string, Command) (any, error)
}

func strict(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(&struct{}{}) != io.EOF {
		return ErrDenied
	}
	return nil
}
func NewHandler(cfg Config, a *Auth, backend Backend, assets fs.FS) (http.Handler, error) {
	origin, e := url.Parse(cfg.Origin)
	if e != nil || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || a == nil || backend == nil {
		return nil, ErrDenied
	}
	if cfg.LocalHTTP {
		ip := net.ParseIP(origin.Hostname())
		if origin.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return nil, ErrDenied
		}
	} else if origin.Scheme != "https" {
		return nil, ErrDenied
	}
	slots := make(chan struct{}, 16)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.Host != origin.Host {
			http.Error(w, "invalid host", 403)
			return
		}
		if cfg.LocalHTTP {
			host, _, e := net.SplitHostPort(r.RemoteAddr)
			ip := net.ParseIP(host)
			if e != nil || ip == nil || !ip.IsLoopback() {
				http.Error(w, "private access required", 403)
				return
			}
		} else if r.TLS == nil {
			http.Error(w, "HTTPS required", 403)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			if r.Method != "GET" || assets == nil {
				http.NotFound(w, r)
				return
			}
			http.FileServerFS(assets).ServeHTTP(w, r)
			return
		}
		if r.Method != "GET" && r.Method != "POST" {
			http.Error(w, "method not allowed", 405)
			return
		}
		if value := r.Header.Get("Origin"); value != "" && value != cfg.Origin {
			http.Error(w, "origin denied", 403)
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			http.Error(w, "origin denied", 403)
			return
		}
		if r.Method == "POST" && (r.Header.Get("Origin") != cfg.Origin || r.Header.Get("Content-Type") != "application/json") {
			http.Error(w, "origin or content type denied", 403)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "administrator service busy", 503)
			return
		}
		http.NewResponseController(w).SetReadDeadline(time.Now().Add(5 * time.Second))
		http.NewResponseController(w).SetWriteDeadline(time.Now().Add(35 * time.Second))
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		send := func(value any) {
			raw, e := json.Marshal(value)
			if e != nil || len(raw) > 1<<20 {
				http.Error(w, "response unavailable", 503)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(raw)
		}
		read := func(dst any) bool {
			raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
			if e != nil || strict(raw, dst) != nil {
				http.Error(w, "invalid request", 400)
				return false
			}
			return true
		}
		if r.URL.Path == "/api/setup/status" && r.Method == "GET" {
			claimed, e := a.Claimed()
			if e != nil {
				http.Error(w, "setup unavailable", 503)
				return
			}
			send(map[string]bool{"claimed": claimed})
			return
		}
		if r.Method == "POST" && (r.URL.Path == "/api/setup/claim" || r.URL.Path == "/api/login") {
			var input struct {
				Claim    string `json:"claim_token"`
				Password string `json:"password"`
			}
			if !read(&input) {
				return
			}
			var session Session
			var err error
			if r.URL.Path == "/api/setup/claim" {
				session, err = a.Claim(input.Claim, input.Password)
			} else {
				session, err = a.Login(input.Password)
			}
			if err != nil {
				http.Error(w, "credentials rejected or rate limited", 401)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "pooltune_admin", Value: session.Token, Path: "/", HttpOnly: true, Secure: !cfg.LocalHTTP, SameSite: http.SameSiteStrictMode, Expires: session.Expires})
			send(map[string]any{"csrf": session.CSRF, "expires": session.Expires})
			return
		}
		cookie, e := r.Cookie("pooltune_admin")
		if e != nil {
			http.Error(w, "login required", 401)
			return
		}
		session, e := a.Session(cookie.Value)
		if e != nil {
			http.Error(w, "login required", 401)
			return
		}
		if r.Method == "GET" {
			if r.URL.Path == "/api/session" {
				send(map[string]any{"csrf": session.CSRF, "expires": session.Expires})
				return
			}
			switch r.URL.Path {
			case "/api/state", "/api/revisions", "/api/runs", "/api/telemetry":
				value, e := backend.Read(ctx, strings.TrimPrefix(r.URL.Path, "/api/"))
				if e != nil {
					http.Error(w, "state unavailable", 503)
					return
				}
				send(value)
			default:
				http.NotFound(w, r)
			}
			return
		}
		if len(r.Header.Values("X-CSRF-Token")) != 1 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(session.CSRF)) != 1 {
			http.Error(w, "CSRF rejected", 403)
			return
		}
		if r.URL.Path == "/api/logout" || r.URL.Path == "/api/logout-all" {
			var empty struct{}
			if !read(&empty) {
				return
			}
			if a.Logout(session.Token, r.URL.Path == "/api/logout-all") != nil {
				http.Error(w, "logout failed", 503)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "pooltune_admin", Value: "", Path: "/", HttpOnly: true, Secure: !cfg.LocalHTTP, SameSite: http.SameSiteStrictMode, MaxAge: -1})
			send(map[string]string{"status": "logged_out"})
			return
		}
		action := strings.TrimPrefix(r.URL.Path, "/api/")
		switch action {
		case "setup/draft", "setup/verify", "setup/observe", "setup/discover", "setup/approve", "freeze", "resume", "safety-resume", "revert", "rules/lock":
		default:
			http.NotFound(w, r)
			return
		}
		var command Command
		if !read(&command) {
			return
		}
		if command.ExpectedRevision == "" || len(command.ExpectedRevision) > 128 || command.IdempotencyKey == "" || len(command.IdempotencyKey) > 128 || len(command.Groups) > 128 || len(command.Revision) > 128 || len(command.RuleID) > 128 || len(command.SemanticPreviewHash) > 64 {
			http.Error(w, "invalid command", 400)
			return
		}
		if draft := command.Setup; draft != nil {
			if len(draft.ConnectionRef) > 4096 || len(draft.TargetID) > 128 || (draft.Provider != "azure" && draft.Provider != "google" && draft.Provider != "supabase" && draft.Provider != "local") || (draft.Path != "postgres" && draft.Path != "rest") {
				http.Error(w, "invalid setup", 400)
				return
			}
		}
		for _, g := range command.Groups {
			if g.WorkloadID == "" || len(g.WorkloadID) > 128 || (g.Class != "critical" && g.Class != "normal" && g.Class != "background") {
				http.Error(w, "invalid group", 400)
				return
			}
		}
		value, e := backend.Execute(ctx, action, command)
		if e != nil {
			if e == ErrConflict {
				http.Error(w, "configuration changed; refresh and review", 409)
			} else {
				http.Error(w, "command refused or outcome uncertain", 503)
			}
			return
		}
		send(value)
	}), nil
}
