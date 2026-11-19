package hostedapp

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"pooltune/internal/hostedapp/auth"
)

func (a *App) setupPermissions(w http.ResponseWriter, r *http.Request, user auth.User) {
	a.render(w, http.StatusOK, "setup-permissions", Page{Title: "Set up your workspace", User: user.Email, Capabilities: projectCapabilities})
}

func draftBinding(owner int64) []byte {
	return []byte("pooltune/setup-choices/v1/" + strconv.FormatInt(owner, 10))
}

func (a *App) saveSetupPermissions(w http.ResponseWriter, r *http.Request, user auth.User) {
	if r.ParseForm() != nil {
		http.Error(w, "Check your repair choices and try again.", http.StatusBadRequest)
		return
	}
	selected, err := capabilityChoices(r.PostForm)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// An owner-bound draft travels with this tab through setup. Only the
	// explicit start form on a selected project can authorize actual repairs.
	draft := base64.RawURLEncoding.EncodeToString(a.vault.Seal(nil, nil, []byte(strings.Join(selected, ",")), draftBinding(user.ID)))
	http.Redirect(w, r, withDraft("/app/projects/new", draft), http.StatusSeeOther)
}

func withDraft(path, draft string) string {
	if draft == "" {
		return path
	}
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + "draft=" + url.QueryEscape(draft)
}

func (a *App) draftChoices(owner int64, draft string) ([]string, bool) {
	if draft == "" {
		return nil, true
	}
	if len(draft) > 256 {
		return nil, false
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(draft)
	if err != nil {
		return nil, false
	}
	plain, err := a.vault.Open(nil, nil, ciphertext, draftBinding(owner))
	if err != nil {
		return nil, false
	}
	if len(plain) == 0 {
		return []string{}, true
	}
	selected, err := capabilityChoices(url.Values{"capability": strings.Split(string(plain), ",")})
	return selected, err == nil
}
