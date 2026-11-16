package hostedapp

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	"pooltune/internal/hostedapp/auth"
)

type EnrollmentOptionView struct {
	Label, Schedule, Token, Status                                               string
	EveryMinutes, CurrentMinute, ProposedMinute, DeadlineSeconds, RuntimeSeconds int
	Selectable                                                                   bool
}
type EnrollmentView struct {
	Functions, Jobs, Saved       []EnrollmentOptionView
	MemorySelected, CronSelected bool
	Notice                       string
}

func (a *App) repairEnrollmentPage(w http.ResponseWriter, r *http.Request, user auth.User) {
	row, ok := a.ownedProject(w, r, user)
	if !ok {
		return
	}
	secrets, err := a.vault.open(row, row.Secret)
	if err != nil || !row.Configured || (secrets.RepairPassword == "" && row.SupabaseConnection == "") {
		http.Error(w, "Connect managed repair access before enrolling a function or job.", http.StatusBadRequest)
		return
	}
	view := EnrollmentView{MemorySelected: slices.Contains(secrets.Capabilities, "memory"), CronSelected: slices.Contains(secrets.Capabilities, "cron")}
	now := time.Now()
	for _, saved := range secrets.Enrollments {
		token, err := a.vault.enrollmentChoice(row, saved.Target, now)
		if err != nil {
			http.Error(w, "Could not load enrollment choices.", http.StatusServiceUnavailable)
			return
		}
		item := enrollmentOption(saved.Target, token)
		item.Status = "Enrolled. Memory changes require verified internal disk spill, an enforced limit on simultaneous calls and available memory. Check project activity for the latest qualification or repair outcome."
		if saved.Target.Kind == "cron" {
			item.Status = "Enrolled. Scheduling changes require repeated interference and protected database-work checks. Check project activity for progress and outcomes. Application-wide requests and errors remain unobserved."
			if saved.Window != nil {
				item.EveryMinutes = saved.Window.EveryMinutes
				item.CurrentMinute = saved.Window.CurrentMinute
				item.ProposedMinute = saved.Window.ProposedMinute
				item.DeadlineSeconds = saved.Window.DeadlineSeconds
				item.RuntimeSeconds = saved.Window.MaxRuntimeSeconds
			}
		}
		if !slices.Contains(secrets.Capabilities, saved.Target.Kind) {
			item.Status = "Permission is currently off. This saved enrollment cannot start a repair."
		}
		view.Saved = append(view.Saved, item)
	}
	catalog, err := a.loadRepairEnrollmentCatalog(r.Context(), row, secrets)
	if err != nil {
		view.Notice = "The live database catalog is unavailable. Existing enrollments remain saved, and you can remove them below. Check project activity for any ongoing repair."
	} else {
		view.Notice = catalog.CronUnavailable
		for i, saved := range secrets.Enrollments {
			if !catalog.contains(saved.Target) {
				view.Saved[i].Status = "The live definition differs from this saved enrollment. Check project activity for an exact Pooltune-applied change. Independently changed targets require review before another repair."
			} else if saved.Target.Kind == "cron" && slices.Contains(secrets.Capabilities, "cron") {
				if history, ok := catalog.Scheduling[saved.Target.JobID]; ok {
					view.Saved[i].Status = fmt.Sprintf("Enrolled. Read %d recorded job runs across the last three cycles. A change also requires comparable completed database work, repeated interference and scheduler acknowledgement. Application-wide request and error rates remain unobserved. Check activity for outcomes.", len(history.Runs))
				}
			}
		}
		if catalog.Truncated {
			view.Notice += " The first 100 functions and jobs are shown; additional targets are not selectable here."
		}
		for _, target := range append(slices.Clone(catalog.Functions), catalog.Jobs...) {
			token, err := a.vault.enrollmentChoice(row, target, now)
			if err != nil {
				continue
			}
			item := enrollmentOption(target, token)
			if target.Kind == "memory" {
				item.Selectable = view.MemorySelected
				view.Functions = append(view.Functions, item)
			} else {
				every, current, err := enrollmentCronCycle(target.Schedule)
				item.Selectable = err == nil && view.CronSelected
				if err != nil {
					item.Status = "This schedule is outside the supported fixed UTC cycles."
				} else {
					item.EveryMinutes = every
					item.CurrentMinute = current
					item.ProposedMinute = (current + 1) % every
					item.DeadlineSeconds = every * 60
				}
				view.Jobs = append(view.Jobs, item)
			}
		}
	}
	a.render(w, http.StatusOK, "repair-enrollment", Page{Title: "Function and job enrollment", User: user.Email, Project: baseProjectView(row), Enrollment: view})
}

func enrollmentOption(target repairEnrollmentTarget, token string) EnrollmentOptionView {
	label := target.Signature
	if target.Kind == "cron" {
		label = target.Name
	}
	return EnrollmentOptionView{Label: label, Token: token, Schedule: target.Schedule}
}

func (a *App) changeRepairEnrollment(w http.ResponseWriter, r *http.Request, user auth.User) {
	a.changeRepairEnrollmentWith(w, r, user, a.loadRepairEnrollmentCatalog)
}

// The injected reader boundary permits a local PostgreSQL catalog in endpoint
// tests. The production route always verifies its bound native installation.
func (a *App) changeRepairEnrollmentWith(w http.ResponseWriter, r *http.Request, user auth.User, read func(context.Context, projectRecord, projectSecrets) (repairEnrollmentCatalog, error)) {
	row, ok := a.ownedProject(w, r, user)
	if !ok {
		return
	}
	if !row.Configured || row.Epoch <= 0 || r.ParseForm() != nil {
		http.Error(w, "Connect this project before changing its enrollments.", http.StatusBadRequest)
		return
	}
	secrets, err := a.vault.open(row, row.Secret)
	if err != nil {
		http.Error(w, "Could not load the project's permissions.", http.StatusServiceUnavailable)
		return
	}
	if secrets.DisconnectPending {
		http.Error(w, "Finish disconnecting before changing enrollments.", http.StatusConflict)
		return
	}
	now := time.Now().UTC()
	target, err := a.vault.openEnrollmentChoice(row, r.PostForm.Get("target"), now)
	if err != nil {
		http.Error(w, errEnrollmentChoice.Error(), http.StatusConflict)
		return
	}
	mode := r.PostForm.Get("mode")
	switch mode {
	case "remove":
		before := len(secrets.Enrollments)
		secrets.Enrollments = slices.DeleteFunc(secrets.Enrollments, func(e repairEnrollment) bool { return sameEnrollmentIdentity(e.Target, target) })
		if before == len(secrets.Enrollments) {
			http.Error(w, "This enrollment is no longer saved. Refresh the page.", http.StatusConflict)
			return
		}
	case "enroll":
		if (secrets.RepairPassword == "" && row.SupabaseConnection == "") || (target.Kind != "memory" && target.Kind != "cron") || !slices.Contains(secrets.Capabilities, target.Kind) {
			http.Error(w, "Select this repair permission and connect managed repair access first.", http.StatusForbidden)
			return
		}
		catalog, err := read(r.Context(), row, secrets)
		if err != nil {
			http.Error(w, "Could not verify the live target. Nothing was enrolled; refresh and try again.", http.StatusServiceUnavailable)
			return
		}
		if !catalog.contains(target) {
			http.Error(w, "This function or job changed after the page loaded. Refresh and choose its current identity.", http.StatusConflict)
			return
		}
		enrollment := repairEnrollment{Target: target, GrantedAtEpoch: row.Epoch + 1, EnrolledAt: now}
		if target.Kind == "cron" {
			enrollment.Window, err = enrollmentCronWindow(target.Schedule, r.PostForm)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		secrets.Enrollments = slices.DeleteFunc(secrets.Enrollments, func(e repairEnrollment) bool { return sameEnrollmentObject(e.Target, target) })
		count := 0
		for _, e := range secrets.Enrollments {
			if e.Target.Kind == target.Kind {
				count++
			}
		}
		if count >= 8 {
			http.Error(w, "This preview supports eight enrolled targets per repair type. Remove one before adding another.", http.StatusBadRequest)
			return
		}
		secrets.Enrollments = append(secrets.Enrollments, enrollment)
	default:
		http.Error(w, "Choose enroll or remove.", http.StatusBadRequest)
		return
	}
	sealed, err := a.vault.seal(row, secrets)
	if err == nil {
		err = a.projects.replaceActivePermissions(r.Context(), row, sealed)
	}
	if err != nil {
		http.Error(w, "The project permissions changed. Refresh before trying again.", http.StatusConflict)
		return
	}
	http.Redirect(w, r, "/app/projects/"+row.ID+"/repair-enrollment", http.StatusSeeOther)
}

func sameEnrollmentObject(a, b repairEnrollmentTarget) bool {
	return a.Kind == b.Kind && a.DatabaseOID == b.DatabaseOID && a.ObjectOID == b.ObjectOID && a.JobID == b.JobID
}
