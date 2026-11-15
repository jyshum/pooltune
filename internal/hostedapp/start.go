package hostedapp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"pooltune/internal/hostedapp/auth"
	"pooltune/internal/labdb"
	"pooltune/internal/supabaseproject"
)

type CapabilityView struct {
	Key, Label, Description, Availability string
	Selected                              bool
}

var projectCapabilities = []CapabilityView{
	{Key: "index", Label: "Add indexes", Description: "Create nonunique indexes for eligible queries."},
	{Key: "analyze", Label: "Refresh statistics", Description: "Refresh PostgreSQL's estimates when they are stale."},
	{Key: "statistics", Label: "Improve statistics detail", Description: "Adjust statistics for eligible columns and column groups."},
	{Key: "vacuum", Label: "Maintain tables", Description: "Run bounded cleanup or adjust when it starts."},
	{Key: "memory", Label: "Tune function memory", Description: "Adjust memory for eligible functions that spill to disk."},
	{Key: "cron", Label: "Reschedule jobs", Description: "Move eligible jobs inside approved operating windows."},
}

func capabilityChoices(form url.Values) ([]string, error) {
	keys := form["capability"]
	for _, key := range keys {
		if !slices.ContainsFunc(projectCapabilities, func(c CapabilityView) bool { return c.Key == key }) {
			return nil, errors.New("select one of the repair permissions shown")
		}
	}
	var selected []string
	for _, capability := range projectCapabilities {
		if form.Get("all_capabilities") == "1" || slices.Contains(keys, capability.Key) {
			selected = append(selected, capability.Key)
		}
	}
	return selected, nil
}

func (a *App) startProject(w http.ResponseWriter, r *http.Request, user auth.User) {
	row, ok := a.ownedProject(w, r, user)
	if !ok {
		return
	}
	if row.Configured || r.ParseForm() != nil {
		a.showProject(w, r, user, row, "This project could not start. Check its current connection.")
		return
	}
	selected, err := capabilityChoices(r.PostForm)
	if err != nil {
		a.showProject(w, r, user, row, err.Error())
		return
	}
	token, ca := strings.TrimSpace(r.PostForm.Get("setup_token")), strings.TrimSpace(r.PostForm.Get("ca"))
	if len(token) == 0 || len(token) > 512 || len(ca) == 0 || len(ca) > 32768 || strings.ContainsAny(token, " \t\r\n") {
		a.showProject(w, r, user, row, "Enter your project-scoped setup token and Supabase root certificate.", selected)
		return
	}
	if err := a.startAutomaticProject(r.Context(), row, token, ca, selected); err != nil {
		a.showProject(w, r, user, row, err.Error(), selected)
		return
	}
	http.Redirect(w, r, "/app/projects/"+row.ID, http.StatusSeeOther)
}

func (a *App) startAutomaticProject(parent context.Context, row projectRecord, token, ca string, selected []string) error {
	ctx, cancel := context.WithTimeout(parent, 18*time.Second)
	defer cancel()
	generic := errors.New("could not start this agent. Check the connection and try again")
	identity, err := row.identity()
	if err != nil {
		return generic
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return generic
	}
	credentials := projectSecrets{
		Password: base64.RawURLEncoding.EncodeToString(random), CA: ca, Automatic: true, Capabilities: selected,
	}
	if _, err := identity.Config(credentials.Password, []byte(ca)); err != nil {
		return errors.New("paste a valid Supabase root certificate")
	}
	sealed, err := a.vault.seal(row, credentials)
	if err != nil {
		return generic
	}
	lock, err := a.projects.db.Acquire(ctx)
	if err != nil {
		return generic
	}
	defer lock.Release()
	var acquired bool
	if err := lock.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,761994345008))`, row.ID).Scan(&acquired); err != nil {
		labdb.Close(lock.Conn())
		return generic
	}
	if !acquired {
		return errors.New("this agent is already being set up")
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := lock.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended($1,761994345008))`, row.ID); err != nil {
			labdb.Close(lock.Conn())
		}
	}()

	var pending []byte
	if err := lock.QueryRow(ctx, `SELECT pending_setup FROM pooltune_app.projects WHERE id=$1 AND owner_id=$2 AND epoch=$3 AND epoch<=0`,
		row.ID, row.Owner, row.Epoch).Scan(&pending); err != nil {
		return generic
	}
	if pending != nil {
		previous, err := a.vault.open(row, pending)
		if err != nil || !previous.Automatic {
			return generic
		}
		credentials.Password = previous.Password
		sealed, err = a.vault.seal(row, credentials)
		if err != nil {
			return generic
		}
	}
	tag, err := lock.Exec(ctx, `UPDATE pooltune_app.projects SET pending_setup=$4 WHERE id=$1 AND owner_id=$2 AND epoch=$3 AND epoch<=0`,
		row.ID, row.Owner, row.Epoch, sealed)
	if err != nil || tag.RowsAffected() != 1 {
		return generic
	}
	return a.installAutomaticMonitor(ctx, row, identity, token, credentials, pending != nil, sealed)
}

func (a *App) installAutomaticMonitor(ctx context.Context, row projectRecord, identity supabaseproject.Project, token string, credentials projectSecrets, retry bool, sealed []byte) error {
	ownedProbe := a.ownedIndex.Matches(identity) && a.ownedIndex.Discover
	requireDiscoveredIndex := ownedProbe && slices.Contains(credentials.Capabilities, "index")
	if retry {
		check, cancel := context.WithTimeout(ctx, 3*time.Second)
		installed := checkProject(check, row, credentials, a.policy, requireDiscoveredIndex) == nil
		cancel()
		if installed {
			return a.activateAutomaticProject(ctx, row, sealed)
		}
	}
	setup := identity.Bootstrap
	if row.Epoch < 0 {
		setup = identity.Reconnect
	} else if ownedProbe {
		setup = identity.BootstrapOwnedIndexProbe
	} else if a.ownedRepair.Matches(identity) && a.ownedRepair.Automatic {
		setup = func(ctx context.Context, token, password string) error {
			return identity.BootstrapOwnedRepairProbe(ctx, token, password, a.ownedRepair.Operation)
		}
	}
	if err := setup(ctx, token, credentials.Password); err != nil {
		return err
	}
	if err := checkProject(ctx, row, credentials, a.policy, requireDiscoveredIndex); err != nil {
		return err
	}
	return a.activateAutomaticProject(ctx, row, sealed)
}

func (a *App) activateAutomaticProject(ctx context.Context, row projectRecord, sealed []byte) error {
	err := a.projects.activate(ctx, row, sealed)
	if err == nil || errors.Is(err, errProjectCapacity) || errors.Is(err, errProjectActive) {
		return err
	}
	return errors.New("monitor setup completed, but the agent could not start. Retry from this page")
}

func (a *App) changeRepairMode(w http.ResponseWriter, r *http.Request, user auth.User) {
	row, ok := a.ownedProject(w, r, user)
	if !ok {
		return
	}
	if !row.Configured || r.ParseForm() != nil {
		http.Error(w, "This project is not monitoring.", http.StatusBadRequest)
		return
	}
	mode := r.PostForm.Get("mode")
	if mode != "pause" && mode != "resume" {
		http.Error(w, "Choose pause or resume.", http.StatusBadRequest)
		return
	}
	credentials, err := a.vault.open(row, row.Secret)
	if err != nil || len(credentials.Capabilities) == 0 {
		http.Error(w, "This project has no repair permissions to change.", http.StatusBadRequest)
		return
	}
	paused := mode == "pause"
	if credentials.RepairsPaused != paused {
		credentials.RepairsPaused = paused
		sealed, err := a.vault.seal(row, credentials)
		if err == nil {
			err = a.projects.replaceActivePermissions(r.Context(), row, sealed)
		}
		if err != nil {
			a.showProject(w, r, user, row, "The project's repair mode could not change. Refresh and try again.")
			return
		}
	}
	http.Redirect(w, r, "/app/projects/"+row.ID, http.StatusSeeOther)
}
