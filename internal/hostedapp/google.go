package hostedapp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"pooltune/internal/hostedapp/auth"
)

type googleOIDC struct {
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	client   *http.Client
}

func newGoogleOIDC(clientID, secret, origin string) (*googleOIDC, error) {
	if clientID == "" && secret == "" {
		return nil, nil
	}
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(secret) == "" {
		return nil, errors.New("set both POOLTUNE_GOOGLE_CLIENT_ID and POOLTUNE_GOOGLE_CLIENT_SECRET")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	ctx := oidc.ClientContext(context.Background(), client)
	// Google's documented endpoints avoid a network dependency during app startup.
	verifier := oidc.NewVerifier("https://accounts.google.com", oidc.NewRemoteKeySet(ctx, "https://www.googleapis.com/oauth2/v3/certs"), &oidc.Config{ClientID: clientID, SupportedSigningAlgs: []string{oidc.RS256}})
	return &googleOIDC{oauth: oauth2.Config{ClientID: clientID, ClientSecret: secret, RedirectURL: origin + "/auth/google/callback", Scopes: []string{oidc.ScopeOpenID, "email"}, Endpoint: oauth2.Endpoint{AuthURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token", AuthStyle: oauth2.AuthStyleInParams}}, verifier: verifier, client: client}, nil
}

func (g *googleOIDC) identity(ctx context.Context, code string, flow auth.GoogleFlow) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ctx = oidc.ClientContext(ctx, g.client)
	token, err := g.oauth.Exchange(ctx, code, oauth2.VerifierOption(flow.Verifier))
	if err != nil {
		return "", "", errors.New("Google token exchange failed")
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		return "", "", errors.New("Google ID token missing")
	}
	id, err := g.verifier.Verify(ctx, raw)
	if err != nil {
		return "", "", errors.New("Google ID token rejected")
	}
	if id.Subject == "" || subtle.ConstantTimeCompare([]byte(id.Nonce), []byte(flow.Nonce)) != 1 {
		return "", "", errors.New("Google identity rejected")
	}
	var claims struct {
		Email           string `json:"email"`
		Verified        bool   `json:"email_verified"`
		AuthorizedParty string `json:"azp"`
	}
	if id.Claims(&claims) != nil || !claims.Verified || claims.Email == "" || (claims.AuthorizedParty != "" && claims.AuthorizedParty != g.oauth.ClientID) || (len(id.Audience) > 1 && claims.AuthorizedParty != g.oauth.ClientID) {
		return "", "", errors.New("Google verified email required")
	}
	return id.Subject, claims.Email, nil
}

func (a *App) googleCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: a.cookie + "_google", Value: value, Path: "/", Secure: a.origin.Scheme == "https", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}

func (a *App) googleFailure(w http.ResponseWriter, status int, message string) {
	a.render(w, status, "google-result", Page{Title: "Google sign-in", Error: message})
}

func (a *App) googleStart(w http.ResponseWriter, r *http.Request) {
	if a.google == nil {
		http.NotFound(w, r)
		return
	}
	// Linking and login initiation must originate from our own form. Callback is a
	// top-level cross-site GET authenticated by the browser-bound one-time state.
	if r.Header.Get("Origin") != a.origin.String() {
		http.Error(w, "unrecognized origin", http.StatusForbidden)
		return
	}
	if !a.authQuota.take() {
		a.googleFailure(w, 429, "Try again in a minute.")
		return
	}
	if r.ParseForm() != nil {
		a.googleFailure(w, 400, "Check the form and try again.")
		return
	}
	mode := r.PostForm.Get("mode")
	var userID int64
	var session string
	switch mode {
	case "signup":
		invite := sha256.Sum256([]byte(r.PostForm.Get("invite_code")))
		if subtle.ConstantTimeCompare(invite[:], a.invite[:]) != 1 {
			a.render(w, 403, "signup", accountPage("signup", "Enter a valid preview invite code."))
			return
		}
	case "signin":
	case "link":
		user, err := a.session(r)
		if err != nil {
			http.Redirect(w, r, "/signin", 303)
			return
		}
		userID = user.ID
		cookie, _ := r.Cookie(a.cookie)
		session = cookie.Value
	default:
		a.googleFailure(w, 400, "Choose sign in or create an account and try again.")
		return
	}
	flow, browser, err := a.accounts.BeginGoogleFlow(r.Context(), mode, userID, session)
	if err != nil {
		a.googleFailure(w, 503, "Google sign-in could not start. Try again.")
		return
	}
	a.googleCookie(w, browser, int(auth.GoogleFlowTTL/time.Second))
	http.Redirect(w, r, a.google.oauth.AuthCodeURL(flow.State, oauth2.S256ChallengeOption(flow.Verifier), oidc.Nonce(flow.Nonce), oauth2.SetAuthURLParam("prompt", "select_account")), http.StatusSeeOther)
}

func (a *App) googleCallback(w http.ResponseWriter, r *http.Request) {
	if a.google == nil {
		http.NotFound(w, r)
		return
	}
	cookie, err := r.Cookie(a.cookie + "_google")
	if err != nil {
		a.googleFailure(w, 400, "Google sign-in expired or was opened in a different browser. Start again.")
		return
	}
	flow, err := a.accounts.ConsumeGoogleFlow(r.Context(), r.URL.Query().Get("state"), cookie.Value)
	if err != nil {
		a.googleFailure(w, 400, "Google sign-in expired or was already used. Start again.")
		return
	}
	a.googleCookie(w, "", -1)
	if r.URL.Query().Get("error") != "" {
		a.googleFailure(w, 400, "Google sign-in was cancelled or denied. You can try again.")
		return
	}
	var oldSession string
	if c, err := r.Cookie(a.cookie); err == nil {
		oldSession = c.Value
	}
	if flow.Mode == "link" && !flow.MatchesSession(oldSession) {
		a.googleFailure(w, 400, "Your account session changed. Sign in to your existing account and start linking again.")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" || len(code) > 4096 {
		a.googleFailure(w, 400, "Google sign-in did not return a valid code. Try again.")
		return
	}
	subject, email, err := a.google.identity(r.Context(), code, flow)
	if err != nil {
		a.googleFailure(w, 400, "Google could not verify your account. Try again with a verified Google email.")
		return
	}
	_, session, err := a.accounts.GoogleAccount(r.Context(), flow.Mode, subject, email, flow.LinkUserID, oldSession)
	if err != nil {
		message := "Google sign-in could not finish. Try again."
		switch {
		case errors.Is(err, auth.ErrGoogleLinkRequired):
			message = "Sign in to your existing account with your password, then open Account and choose Link Google account. Use the same email address."
		case errors.Is(err, auth.ErrGoogleUnknown):
			message = "Create an account with your preview invite code before signing in with Google."
		case errors.Is(err, auth.ErrInvalidCredentials):
			message = "Sign in to your existing account and link the Google account with the same email address."
		}
		a.googleFailure(w, 400, message)
		return
	}
	if oldSession != "" {
		_ = a.accounts.SignOut(r.Context(), oldSession)
	}
	a.setSession(w, session, int(auth.SessionTTL/time.Second))
	target := "/app"
	if flow.Mode == "link" {
		target = "/app/account?google=linked"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (a *App) accountSettings(w http.ResponseWriter, r *http.Request, user auth.User) {
	page := Page{Title: "Account", User: user.Email}
	if r.URL.Query().Get("google") == "linked" {
		page.Notice = "Google is linked. You can now use Continue with Google to sign in."
	}
	a.render(w, http.StatusOK, "account-settings", page)
}
