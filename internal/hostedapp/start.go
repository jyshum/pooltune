package hostedapp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
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
	{Key: "index", Label: "Add indexes", Description: "Create nonunique indexes for eligible queries. Requires table-owner access."},
	{Key: "analyze", Label: "Refresh statistics", Description: "Refresh PostgreSQL's estimates when they are stale. Requires table-owner access or a direct MAINTAIN grant on the table."},
	{Key: "statistics", Label: "Improve statistics detail", Description: "Adjust statistics for eligible columns and column groups. Requires table-owner access."},
	{Key: "vacuum", Label: "Maintain tables", Description: "Plain cleanup uses table-owner access or a direct MAINTAIN grant. Changing when cleanup starts requires table-owner access."},
	{Key: "memory", Label: "Tune function memory", Description: "Adjust memory for eligible functions that spill to disk. Requires function-owner access."},
	{Key: "cron", Label: "Reschedule jobs", Description: "Move eligible jobs to reduce interference. Requires job-owner access and an approved schedule window."},
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
	objectiveMS, err := setupObjective(r.PostForm.Get("mean_latency_ms"))
	if err != nil {
		a.showProject(w, r, user, row, err.Error(), selected)
		return
	}
	repairPassword := r.PostForm.Get("repair_password")
	if len(repairPassword) > 512 || strings.ContainsRune(repairPassword, 0) {
		a.showProject(w, r, user, row, "Enter a valid database credential.", selected)
		return
	}
	if err := a.startAutomaticProject(r.Context(), row, token, ca, selected, repairPassword, objectiveMS); err != nil {
		a.showProject(w, r, user, row, err.Error(), selected)
		return
	}
	http.Redirect(w, r, "/app/projects/"+row.ID, http.StatusSeeOther)
}

func (a *App) startAutomaticProject(parent context.Context, row projectRecord, token, ca string, selected []string, repairPassword string, objectiveMS int) error {
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
	credentials.ObjectiveMS = objectiveMS
	if objectiveMS < 0 || objectiveMS > 1000 {
		return errors.New("choose a mean query latency goal between 1 and 1000 milliseconds")
	}
	if len(selected) > 0 {
		credentials.RepairPassword = repairPassword
	}
	if len(credentials.RepairPassword) > 512 || strings.ContainsRune(credentials.RepairPassword, 0) {
		return errors.New("enter a valid database credential")
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
		if len(selected) > 0 && credentials.RepairPassword == "" {
			credentials.RepairPassword = previous.RepairPassword
		}
		sealed, err = a.vault.seal(row, credentials)
		if err != nil {
			return generic
		}
	}
	fixture := a.ownedIndex.Matches(identity) || a.ownedRepair.Matches(identity) || a.ownedCron.Matches(identity)
	if len(selected) > 0 && credentials.RepairPassword == "" && !fixture {
		return errors.New("enter the project's database password to enable managed repairs")
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
	requireDiscoveredIndex := credentials.RepairPassword != "" || (ownedProbe && slices.Contains(credentials.Capabilities, "index"))
	setup := identity.Bootstrap
	if row.Epoch < 0 {
		setup = identity.Reconnect
	} else if ownedProbe {
		setup = identity.BootstrapOwnedIndexProbe
	} else if a.ownedRepair.Matches(identity) && a.ownedRepair.Automatic {
		setup = func(ctx context.Context, token, password string) error {
			return identity.BootstrapOwnedRepairProbe(ctx, token, password, a.ownedRepair.Operation)
		}
	} else if a.ownedCron.Matches(identity) && a.ownedCron.QueryID == 0 {
		setup = func(ctx context.Context, token, password string) error {
			return identity.BootstrapOwnedRepairProbe(ctx, token, password, "cron")
		}
	}
	install := func(ctx context.Context) error {
		return setup(ctx, token, credentials.Password)
	}
	verify := func(ctx context.Context, includeRepairEvidence bool) error {
		return checkProject(ctx, row, credentials, a.policy, includeRepairEvidence && requireDiscoveredIndex)
	}
	fixture := a.ownedIndex.Matches(identity) || a.ownedRepair.Matches(identity) || a.ownedCron.Matches(identity)
	configureRepairs := func(ctx context.Context) error {
		goal := credentials.ObjectiveMS
		if goal == 0 && !fixture {
			goal = 1000
		}
		if goal > 0 {
			if err := identity.ConfigureDiscoveryObjective(ctx, token, goal); err != nil {
				return err
			}
		}
		if credentials.RepairPassword == "" {
			return nil
		}
		if err := identity.ValidateManagedRepair(ctx, credentials.RepairPassword, []byte(credentials.CA)); err != nil {
			return err
		}
		return identity.InstallManagedIndexReader(ctx, token)
	}
	activate := func(ctx context.Context) error { return a.activateAutomaticProject(ctx, row, sealed) }
	return completeAutomaticSetup(ctx, retry, install, verify, configureRepairs, activate)
}

// completeAutomaticSetup resumes after an installed monitor even if a later
// repair credential check or evidence-reader installation failed.
func completeAutomaticSetup(ctx context.Context, retry bool, install func(context.Context) error,
	verify func(context.Context, bool) error, configureRepairs, activate func(context.Context) error) error {
	installed := false
	if retry {
		check, cancel := context.WithTimeout(ctx, 3*time.Second)
		installed = verify(check, false) == nil
		cancel()
	}
	if !installed {
		if err := install(ctx); err != nil {
			return err
		}
	}
	if err := configureRepairs(ctx); err != nil {
		return err
	}
	if err := verify(ctx, true); err != nil {
		return err
	}
	return activate(ctx)
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
	credentials, err := a.vault.open(row, row.Secret)
	if err != nil {
		http.Error(w, "Could not load the project's repair permissions.", http.StatusBadRequest)
		return
	}
	var changed bool
	switch mode {
	case "choices":
		choices, err := capabilityChoices(r.PostForm)
		if err != nil {
			a.showProject(w, r, user, row, err.Error())
			return
		}
		changed = !slices.Equal(credentials.Capabilities, choices)
		credentials.Capabilities = choices
	case "pause", "resume":
		if len(credentials.Capabilities) == 0 {
			http.Error(w, "This project has no repair permissions to pause or resume.", http.StatusBadRequest)
			return
		}
		paused := mode == "pause"
		changed = credentials.RepairsPaused != paused
		credentials.RepairsPaused = paused
	default:
		http.Error(w, "Choose repair permissions, pause or resume.", http.StatusBadRequest)
		return
	}
	if changed {
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

func setupObjective(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	goal, err := strconv.Atoi(value)
	if err != nil || goal < 1 || goal > 1000 {
		return 0, errors.New("choose a mean query latency goal in whole milliseconds from 1 to 1000")
	}
	return goal, nil
}
