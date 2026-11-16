package hostedapp

import (
	"context"
	"net/http"
	"pooltune/internal/hostedapp/auth"
	"pooltune/internal/supabaseproject"
	"strings"
)

func (a *App) replaceMonitoringToken(w http.ResponseWriter, r *http.Request, user auth.User) {
	a.replaceMonitoringTokenWith(w, r, user, func(ctx context.Context, ref, token string) error {
		reader, err := supabaseproject.NewMemoryMetricsReader(ref, token)
		if err != nil {
			return err
		}
		_, err = reader.Read(ctx, ref)
		return err
	})
}

func (a *App) replaceMonitoringTokenWith(w http.ResponseWriter, r *http.Request, user auth.User, verify func(context.Context, string, string) error) {
	row, ok := a.ownedProject(w, r, user)
	if !ok {
		return
	}
	if !row.Configured || r.ParseForm() != nil {
		http.Error(w, "Connect the project before updating monitoring access.", http.StatusBadRequest)
		return
	}
	credentials, err := a.vault.open(row, row.Secret)
	if err != nil || credentials.DisconnectPending {
		http.Error(w, "This connection cannot update monitoring access.", http.StatusConflict)
		return
	}
	token := strings.TrimSpace(r.PostForm.Get("monitoring_token"))
	if len(token) == 0 || len(token) > 512 || strings.ContainsAny(token, " \t\r\n") {
		http.Error(w, "Enter one project-scoped token with Logs Read permission.", http.StatusBadRequest)
		return
	}
	identity, err := row.identity()
	if err != nil || verify(r.Context(), identity.Ref(), token) != nil {
		http.Error(w, "Could not read this project's memory metrics. Check the token's project and Logs Read permission.", http.StatusBadRequest)
		return
	}
	credentials.LogsToken = token
	sealed, err := a.vault.seal(row, credentials)
	if err == nil {
		err = a.projects.replaceActivePermissions(r.Context(), row, sealed)
	}
	if err != nil {
		http.Error(w, "Monitoring access could not update. Refresh and try again.", http.StatusConflict)
		return
	}
	http.Redirect(w, r, "/app/projects/"+row.ID, http.StatusSeeOther)
}
