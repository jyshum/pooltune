package hostedapp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"pooltune/internal/hostedapp/auth"
	"pooltune/internal/hostedquery"
	"pooltune/internal/supabaseproject"
)

type Page struct {
	ProjectLimit, CreditLimit, CreditsUsed int
	Draft                                  string
	Capabilities                           []CapabilityView
	Title, User, Error                     string
	Notice                                 string
	GoogleEnabled                          bool
	SupabaseProjects                       []SupabaseProject
	SupabaseConnection                     string
	RepairWorkerKey                        string
	Invite                                 bool
	Projects                               []ProjectView
	Project                                ProjectView
	Enrollment                             EnrollmentView
}

type ProjectView struct {
	SharedName                            bool
	CreditsUsed                           int
	PollCadence                           string
	BeforeRun                             int64
	OlderRunsAfter                        int64
	Section                               string
	Charts                                []activityChart
	Allowed                               int
	Epoch                                 int64
	AgentPaused, DisconnectPending        bool
	ID, Name, Ref, Status, Role, LastPoll string
	SetupSQL, QuerySQL                    string
	QuickStart                            bool
	SupabaseConnected                     bool
	SupabaseBlocked                       bool
	Capabilities                          []CapabilityView
	RepairsSelected, RepairsPaused        bool
	RepairWorkerLinked                    bool
	ManagedRepairs                        bool
	ManagedCredentialNeeded               bool
	RepairWorkerSummary                   string
	FixtureIndex                          bool
	FixtureRepair                         *CapabilityView
	Queries                               []QueryGroup
	Runs                                  []RunView
	Polls                                 []PollView
}

type QueryView struct{ Calls, Mean, Target, Blocks, Decision, At string }
type QueryGroup struct {
	ID                string
	Role, Database    uint32
	Windows, Measured int
	Calls             int64
	Decision          string
	Recent            []QueryView
}

func groupQueryWindows(windows []hostedquery.ProjectWindow) []QueryGroup {
	type fingerprint struct {
		database, role uint32
		query          int64
	}
	positions := make(map[fingerprint]int)
	var groups []QueryGroup
	for i := len(windows) - 1; i >= 0; i-- {
		w := windows[i]
		key := fingerprint{w.Database, w.Role, w.Query}
		pos, found := positions[key]
		if !found {
			if len(groups) >= 100 {
				continue
			}
			pos = len(groups)
			positions[key] = pos
			groups = append(groups, QueryGroup{ID: strconv.FormatInt(w.Query, 10), Role: w.Role, Database: w.Database, Decision: outcomeLabel(string(w.Decision))})
		}
		group := &groups[pos]
		group.Windows++
		if w.Calls != nil {
			group.Measured++
			group.Calls += *w.Calls
		}
		if len(group.Recent) >= 6 {
			continue
		}
		mean := "—"
		if w.MeanMS != nil {
			mean = decimalMS(*w.MeanMS)
		}
		group.Recent = append(group.Recent, QueryView{Calls: integerOrUnavailable(w.Calls), Mean: mean, Target: decimalMS(w.ObjectiveMS), Blocks: integerOrUnavailable(w.SharedBlocks), Decision: outcomeLabel(string(w.Decision)), At: displayTime(w.End)})
	}
	return groups
}

type RunView struct {
	ParentID                                                                    int64
	ParentVisible                                                               bool
	Created                                                                     string
	CanRevert, Reverted                                                         bool
	ID, Query, Outcome, Started, Finished, Queue, Model, Analysis, Reason, Plan string
	Repair, RepairStatus                                                        string
	Measurements                                                                []RepairMeasurementView
}
type RepairMeasurementView struct{ Label, Before, After string }
type PollView struct{ At, Status string }

const historyRange = 7 * 24 * time.Hour

func baseProjectView(row projectRecord) ProjectView {
	status := "Pending setup"
	if row.Epoch < 0 {
		status = "Disconnected"
	}
	if row.Configured {
		status = "Monitoring"
	}
	if row.AgentPaused {
		status = "Paused"
	}
	if row.SupabaseBlocked {
		status = "Reconnect Supabase"
	}
	return ProjectView{Epoch: row.Epoch, AgentPaused: row.AgentPaused, SupabaseBlocked: row.SupabaseBlocked, ID: row.ID, Name: row.Name, Ref: row.Ref, Status: status}
}

func (a *App) dashboard(w http.ResponseWriter, r *http.Request, user auth.User) {
	rows, err := a.projects.list(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "Could not load your projects.", http.StatusServiceUnavailable)
		return
	}
	if len(rows) == 0 && r.URL.Query().Get("view") != "projects" {
		a.setupPermissions(w, r, user)
		return
	}
	page := Page{Title: "Projects", User: user.Email, ProjectLimit: maxActiveProjects, CreditLimit: dailyDiagnosisCredits}
	names := make(map[string]int, len(rows))
	for _, row := range rows {
		names[strings.ToLower(row.Name)]++
	}
	for _, row := range rows {
		project := baseProjectView(row)
		project.SharedName = names[strings.ToLower(row.Name)] > 1
		project.CreditsUsed = row.CreditsUsed
		page.CreditsUsed += row.CreditsUsed
		page.Projects = append(page.Projects, project)
	}
	a.render(w, http.StatusOK, "dashboard", page)
}

func (a *App) project(w http.ResponseWriter, r *http.Request, user auth.User) {
	row, ok := a.ownedProject(w, r, user)
	if ok {
		a.showProject(w, r, user, row, "")
	}
}

func (a *App) projectActivity(row projectRecord, r *http.Request, beforeJob ...int64) (hostedquery.ProjectActivity, error) {
	identity, err := row.identity()
	if err != nil {
		return hostedquery.ProjectActivity{}, err
	}
	return hostedquery.ReadProjectActivity(r.Context(), a.projects.db, identity.ID(), time.Now().Add(-historyRange), beforeJob...)
}

func (a *App) downloadActivity(w http.ResponseWriter, r *http.Request, user auth.User) {
	row, ok := a.ownedProject(w, r, user)
	if !ok {
		return
	}
	activity, err := a.projectActivity(row, r)
	if err != nil {
		http.Error(w, "Could not load project activity.", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="pooltune-activity.json"`)
	_ = json.NewEncoder(w).Encode(activity)
}

func (a *App) showProject(w http.ResponseWriter, r *http.Request, user auth.User, row projectRecord, failure string, requested ...[]string) {
	identity, err := row.identity()
	if err != nil {
		http.Error(w, "Could not load this project.", http.StatusServiceUnavailable)
		return
	}
	view := baseProjectView(row)
	view.Section = r.URL.Query().Get("section")
	if !slices.Contains([]string{"overview", "queries", "changes", "permissions", "connection"}, view.Section) {
		view.Section = "overview"
	}
	view.Role = identity.Role()
	view.SupabaseConnected = row.SupabaseConnection != ""
	fixtureMatches := a.ownedIndex.Matches(identity)
	view.FixtureIndex = fixtureMatches && !a.ownedIndex.Discover
	repairMatches := a.ownedRepair.Matches(identity)
	cronMatches := a.ownedCron.Matches(identity)
	fixtureOperation := ""
	if repairMatches && !a.ownedRepair.Automatic {
		fixtureOperation = a.ownedRepair.Operation
	} else if cronMatches && a.ownedCron.QueryID != 0 {
		fixtureOperation = "cron"
	}
	for _, capability := range projectCapabilities {
		if capability.Key == fixtureOperation {
			view.FixtureRepair = &capability
			break
		}
	}
	if !row.Configured {
		view.QuickStart = true
		if len(requested) == 0 {
			var saved []string
			if a.projects.db.QueryRow(r.Context(), `SELECT setup_capabilities FROM pooltune_app.projects WHERE id=$1 AND owner_id=$2`, row.ID, row.Owner).Scan(&saved) == nil {
				requested = [][]string{saved}
			}
		}
		view.Capabilities = slices.Clone(projectCapabilities)
		if len(requested) > 0 {
			for i := range view.Capabilities {
				view.Capabilities[i].Selected = slices.Contains(requested[0], view.Capabilities[i].Key)
			}
		}
		view.SetupSQL = identity.InstallSQL() + "\n" + identity.LookupEvidenceSQL() + "\n" + identity.LookupPlanSQL() + "\n" + identity.TrafficPlanSQL()
		view.QuerySQL = queryEnrollment(identity)
	} else if configured, err := a.vault.open(row, row.Secret); err == nil {
		view.Capabilities = slices.Clone(projectCapabilities)
		view.RepairsSelected = len(configured.Capabilities) > 0
		view.Allowed = len(configured.Capabilities)
		view.RepairsPaused = configured.RepairsPaused
		view.DisconnectPending = configured.DisconnectPending
		if configured.DisconnectPending && failure == "" {
			failure = "Repairs are stopped. Temporary scheduler cleanup is pending. Retry disconnect to finish; the encrypted credential is retained only for cleanup."
		}
		view.ManagedRepairs = configured.RepairPassword != "" || row.SupabaseConnection != ""
		view.RepairWorkerLinked, view.RepairWorkerSummary = a.repairWorkerStatus(r.Context(), row)
		view.ManagedCredentialNeeded = configured.Automatic && !view.ManagedRepairs && view.RepairsSelected && !view.RepairWorkerLinked
		for i := range view.Capabilities {
			option := &view.Capabilities[i]
			option.Selected = slices.Contains(configured.Capabilities, option.Key)
			if option.Selected {
				option.Availability = repairAvailability(option.Key, view.ManagedRepairs)
				if view.ManagedRepairs && (option.Key == "memory" || option.Key == "cron") {
					count := 0
					for _, enrollment := range configured.Enrollments {
						if enrollment.Target.Kind == option.Key {
							count++
						}
					}
					if count > 0 {
						option.Availability = fmt.Sprintf("Enrolled targets: %d. Automatic repair is still waiting for supported measurements and safety limits.", count)
					}
				}
				if fixtureMatches && a.ownedIndex.Discover == configured.Automatic && option.Key == "index" {
					option.Availability = "Available for the owned Rookery fixture"
				}
				if repairMatches && a.ownedRepair.Automatic == configured.Automatic && option.Key == a.ownedRepair.Operation {
					option.Availability = "Available for the owned Rookery fixture"
				}
				if cronMatches && (a.ownedCron.QueryID == 0) == configured.Automatic && option.Key == "cron" {
					option.Availability = "Available for the owned Rookery fixture"
				}
				if configured.RepairsPaused {
					option.Availability = "Paused. " + option.Availability
				}
			}
		}
	}
	status := http.StatusOK
	if failure != "" {
		status = http.StatusBadRequest
	}
	var cursor []int64
	if view.Section == "changes" {
		before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
		view.BeforeRun = max(before, 0)
		cursor = []int64{view.BeforeRun}
	}
	activity, err := a.projectActivity(row, r, cursor...)
	if err != nil {
		if failure == "" {
			failure = "Activity is temporarily unavailable. Try refreshing this page."
		}
		status = http.StatusServiceUnavailable
	} else {
		populateActivity(&view, activity, a.policy.Interval, time.Now())
	}
	a.render(w, status, "project", Page{Title: row.Name, User: user.Email, Project: view, Error: failure})
}

// Permission is a saved owner choice, not a promise that the project already
// supplies the evidence and execution support for every repair family.
func repairAvailability(key string, managed bool) string {
	var readiness string
	switch key {
	case "index":
		readiness = "Automatically checks eligible one- or two-column integer lookups. A repair still needs measured evidence."
	case "analyze":
		readiness = "Automatically checks eligible one-column integer lookups for stale statistics."
	case "vacuum":
		readiness = "Automatically checks eligible lookups for plain table cleanup and changes to cleanup settings. A repair still needs measured evidence."
	case "statistics":
		if !managed {
			return "Reconnect with your database password to enable managed repairs. Your permission is saved."
		}
		return "Eligible lookups can run a bounded statistics trial. Pooltune keeps verified improvements; otherwise it restores the previous settings and refreshes statistics. The old random statistics sample cannot be restored exactly."
	case "memory":
		return "Needs a chosen function, limits on memory and simultaneous calls, and measured evidence of disk spill."
	case "cron":
		return "Needs a chosen job, a permitted schedule window and measured job history."
	default:
		return "This repair is not available for this project."
	}
	if !managed {
		return "Reconnect with your database password to enable managed repairs. Your permission is saved."
	}
	return readiness
}

func queryEnrollment(project supabaseproject.Project) string {
	return fmt.Sprintf(`-- Find exact query IDs from statements your app has already executed.
SELECT userid AS role_oid, queryid, calls, mean_exec_time AS mean_ms, query
FROM extensions.pg_stat_statements
WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
  AND toplevel
ORDER BY calls DESC
LIMIT 50;

-- Replace the example values before running the enrollment statement.
-- INSERT INTO %s.enrolled_queries (slot, role_oid, query_id, mean_execution_ms)
-- VALUES (1, 12345, -1234567890123456789, 25);

-- Optional lookup evidence for a supported statement. Replace all example values.
-- SELECT %s.allow_lookup(12345::oid, -1234567890123456789::bigint,
--   'public.example'::regclass, 'lookup'::name);

-- See the ordinary-traffic plans runbook for role logging and per-query plan grants.
`, project.Role(), project.Role())
}

func displayTime(at time.Time) string {
	return at.UTC().Format("Jan 2, 15:04:05 UTC")
}

func decimalMS(ms float64) string { return fmt.Sprintf("%.2f ms", ms) }

func integerOrUnavailable(value *int64) string {
	if value == nil {
		return "—"
	}
	return strconv.FormatInt(*value, 10)
}

func outcomeLabel(outcome string) string {
	switch outcome {
	case "investigate":
		return "Investigate"
	case "no_change":
		return "No change"
	case "diagnosed":
		return "Diagnosis saved"
	case "insufficient_evidence":
		return "Insufficient evidence"
	case "awaiting_evidence":
		return "Waiting for evidence"
	case "diagnosis_unavailable":
		return "Diagnosis unavailable"
	default:
		return strings.ReplaceAll(outcome, "_", " ")
	}
}

func populateActivity(view *ProjectView, activity hostedquery.ProjectActivity, interval time.Duration, now time.Time) {
	view.Charts = overviewCharts(activity, now)
	view.PollCadence = cadenceLabel(interval)
	if activity.LastPoll != nil {
		view.LastPoll = displayTime(*activity.LastPoll)
		if view.Status == "Monitoring" && now.Sub(*activity.LastPoll) > 2*interval {
			view.LastPoll += " (delayed)"
		}
	}
	if view.Section == "queries" {
		view.Queries = groupQueryWindows(activity.Windows)
	}
	if len(activity.Jobs) > 40 {
		view.OlderRunsAfter = activity.Jobs[39].ID
	}
	visibleRuns := make(map[int64]bool)
	for _, job := range activity.Jobs[:min(40, len(activity.Jobs))] {
		visibleRuns[job.ID] = true
	}
	for i, job := range activity.Jobs {
		if i >= 40 {
			break
		}
		item := RunView{ParentID: job.ParentID, ParentVisible: visibleRuns[job.ParentID], Created: displayTime(job.CreatedAt), CanRevert: job.CanRevert && view.ManagedRepairs && !view.SupabaseBlocked && !view.DisconnectPending && view.Epoch > 0, Reverted: job.Reverted, ID: strconv.FormatInt(job.ID, 10), Query: strconv.FormatInt(job.Query, 10),
			Outcome: outcomeLabel(job.Outcome), Reason: job.Reason, Plan: job.PlanNode}
		switch job.Outcome {
		case "propose":
			item.Outcome = "Recommendation saved"
		case "awaiting_verification":
			item.Outcome = "Waiting for verification"
		case "improved_tested_inputs":
			item.Outcome = "Improved on tested inputs"
		case "regressed":
			item.Outcome = "Regression detected"
		case "inconclusive":
			item.Outcome = "Inconclusive"
		case "repair_interrupted":
			item.Outcome = "Repair interrupted"
		}
		if repair := job.Repair; repair != nil {
			if repair.AnalysisMS > 0 {
				item.Analysis = decimalMS(repair.AnalysisMS)
			}
			for _, capability := range projectCapabilities {
				if capability.Key == repair.Kind {
					item.Repair = capability.Label
					break
				}
			}
			switch repair.Action {
			case "recorded":
				item.RepairStatus = "Action recorded"
			case "removed":
				item.RepairStatus = "Statistics object removed"
				if repair.Kind == "index" {
					item.RepairStatus = "Pooltune-created index removed"
				}
			case "restored":
				item.RepairStatus = "Original setting restored"
			case "interrupted":
				item.RepairStatus = "Interrupted"
			}
			for _, metric := range repair.Metrics {
				item.Measurements = append(item.Measurements, RepairMeasurementView{
					Label: metric.Label, Before: displayRepairMeasurement(metric.Before, metric.Unit), After: displayRepairMeasurement(metric.After, metric.Unit),
				})
			}
		}
		if item.Reason == "daily_model_limit" {
			item.Reason = "The daily diagnosis limit has been reached. Monitoring continues."
		}
		if job.ClaimedAt != nil {
			item.Started = displayTime(*job.ClaimedAt)
			item.Queue = job.ClaimedAt.Sub(job.CreatedAt).Round(time.Millisecond).String()
		}
		if job.FinishedAt != nil {
			item.Finished = displayTime(*job.FinishedAt)
		}
		if job.ModelMS != nil {
			item.Model = decimalMS(*job.ModelMS)
		}
		if job.PlanNode != "" && job.PlanSource == "ordinary_traffic" {
			item.Plan += " · Ordinary traffic"
		}
		if job.PlanNode != "" && job.PlanSource == "owned_sample" {
			item.Plan += " · One test input"
		}
		if job.PlanNode != "" && job.PlanSource == "managed_sample" {
			item.Plan += " · Sampled input experiment"
		}
		view.Runs = append(view.Runs, item)
	}
	for i := len(activity.Polls) - 1; i >= 0 && len(view.Polls) < 50; i-- {
		poll := activity.Polls[i]
		status := "Check completed"
		if i > 0 {
			gap := poll.At.Sub(activity.Polls[i-1].At)
			if gap > 2*interval {
				status = "Resumed after a " + gap.Round(time.Second).String() + " gap"
			}
		}
		view.Polls = append(view.Polls, PollView{At: displayTime(poll.At), Status: status})
	}
}

func displayRepairMeasurement(value float64, unit string) string {
	if unit != "" {
		return fmt.Sprintf("%.2f %s", value, unit)
	}
	return fmt.Sprintf("%.2f", value)
}
