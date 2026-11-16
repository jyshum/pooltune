package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
)

// ManagedProjectCron binds owner-selected schedules to database-observed work.
// It never runs a customer statement or infers application request counts.
type ManagedProjectCron struct {
	Config      *pgx.ConnConfig
	Enrollments []ManagedCronEnrollment
	Investigate IndexInvestigator
	AdmitModel  func(context.Context) error
	Paused      bool
}

type ManagedCronReport struct {
	Window                       ProjectCronWindowPlan `json:"window"`
	JobID                        int64                 `json:"job_id"`
	Before                       []ManagedCronPeriod   `json:"before,omitempty"`
	After                        []ManagedCronPeriod   `json:"after,omitempty"`
	Original                     string                `json:"original_schedule"`
	Alternate                    string                `json:"alternate_schedule"`
	Outcome                      string                `json:"outcome"`
	Reason                       string                `json:"reason"`
	Applied                      bool                  `json:"applied"`
	CommitAt                     time.Time             `json:"commit_at,omitempty"`
	VerifyAfter                  time.Time             `json:"verify_after,omitempty"`
	ApplicationRequestVisibility string                `json:"application_request_visibility"`
}

type managedCronQueue struct {
	JobID int64     `json:"managed_cron_job_id"`
	Cycle time.Time `json:"managed_cron_cycle"`
}

type managedCronAction struct {
	RunID       string
	Enrollment  ManagedCronEnrollment
	Queries     ManagedCronQueries
	Evidence    ManagedCronEvidence
	Intent      ProjectCronIntent
	Receipt     *ProjectCronResult
	Followup    *ManagedCronEnrollment
	VerifyAfter time.Time
}

func (m *ManagedProjectCron) enrollment(job int64) (ManagedCronEnrollment, bool) {
	if m == nil {
		return ManagedCronEnrollment{}, false
	}
	for _, e := range m.Enrollments {
		if e.Target.JobID == job {
			return e, true
		}
	}
	return ManagedCronEnrollment{}, false
}

func (m *ManagedProjectCron) dispatch(ctx context.Context, state *pgx.Conn, project supabaseproject.Project, epoch int64, claim investigationClaim) (*DispatchResult, error) {
	var queued managedCronQueue
	if json.Unmarshal(claim.queueEvidence, &queued) != nil || queued.JobID <= 0 {
		return nil, nil
	}
	enrolled, ok := m.enrollment(queued.JobID)
	action, origin, err := readManagedCronAction(ctx, state, claim)
	if err != nil {
		return nil, err
	}
	result := DispatchResult{JobID: claim.ID, Attempt: claim.Attempt, Outcome: "insufficient_evidence"}
	if action != nil && (!ok || m.Paused || !sameManagedCronEnrollment(enrolled, action.Enrollment)) {
		result.Outcome = "inconclusive"
		result.DiagnosticReason = "Scheduling selection or permission changed. Verification stopped; no schedule change was repeated and no benefit is claimed."
		result.ManagedCron = managedCronReport(*action, result.Outcome, result.DiagnosticReason)
		return &result, publishManagedCronAction(ctx, state, project.ID(), epoch, claim, origin, *action, result, nil, false)
	}
	if !ok {
		if claim.RecoveryOnly {
			return nil, errClaimLost
		}
		result.Outcome, result.DiagnosticReason = "no_change", "The scheduling selection was removed before action."
		return &result, finishInvestigation(ctx, state, claim, result, epoch)
	}
	if enrolled.Target.Project != project.ID() || enrolled.Target.DatabaseOID != claim.Database || m.Config == nil || (action == nil && (m.Investigate == nil || m.AdmitModel == nil)) {
		return nil, errors.New("managed scheduling claim does not match active enrollment")
	}
	owner, err := supabaseproject.ConnectRepair(ctx, m.Config)
	if err != nil {
		return nil, err
	}
	defer closeVacuumConnection(owner)
	permission := func(ctx context.Context, _ ProjectCronIntent) error {
		if err := checkOwnedIndexClaim(ctx, state, project, epoch, claim); err != nil {
			return err
		}
		return project.VerifyManagedRepair(ctx, owner)
	}
	if err := permission(ctx, ProjectCronIntent{}); err != nil {
		return nil, err
	}
	if action != nil {
		return m.finish(ctx, state, owner, project, epoch, claim, origin, *action, result)
	}
	if claim.RecoveryOnly {
		return nil, errClaimLost
	}
	if m.Paused {
		result.Outcome = "no_change"
		result.DiagnosticReason = "scheduling repairs are paused"
		return &result, finishInvestigation(ctx, state, claim, result, epoch)
	}
	now, generation, err := readCronClock(ctx, owner)
	if err != nil {
		return nil, err
	}
	cycle := now.UTC().Truncate(time.Duration(enrolled.Window.EveryMinutes) * time.Minute)
	first := cycle.Add(-2 * time.Duration(enrolled.Window.EveryMinutes) * time.Minute)
	observations, err := ReadManagedCronObservations(ctx, state, project.ID(), epoch, queued.JobID, first)
	if err != nil {
		return nil, err
	}
	var queries ManagedCronQueries
	var periods []ManagedCronPeriod
	for _, o := range observations {
		q, e := SelectManagedCronQueries(o)
		if e != nil {
			continue
		}
		if q.Target.Role != claim.Role || q.Target.Query != claim.Query {
			continue
		}
		periods, e = BuildManagedCronPeriods(observations, q, enrolled.Window, queued.JobID, first, false)
		if e == nil {
			queries = q
			break
		}
	}
	original, alternate, _ := enrolled.Window.schedules()
	evidence := ManagedCronEvidence{JobID: queued.JobID, Original: original, Alternate: alternate, Window: enrolled.Window, Generation: generation, Periods: periods, ApplicationRequestVisibility: "unobserved"}
	if !ManagedCronCause(evidence) {
		result.DiagnosticReason = "three matched scheduling periods are not available"
		return &result, finishInvestigation(ctx, state, claim, result, epoch)
	}
	// The observer checked the full catalog fingerprint; repeat immediately before
	// the model and bind the native executor's schedule-independent identity.
	if _, err := ReadManagedCronHistory(ctx, owner, enrolled); err != nil {
		return nil, err
	}
	runID := fmt.Sprintf("managed-cron-%d-%d", claim.ID, claim.Attempt)
	modelEvidence := IndexEvidence{JobID: queued.JobID, RunID: runID, At: now, Decision: Investigate, ManagedCron: &evidence}
	result.Evidence = &modelEvidence
	result.ThreadID = runID
	if err := m.AdmitModel(ctx); err != nil {
		return nil, err
	}
	proposal, err := m.Investigate(ctx, modelEvidence)
	if err != nil {
		return nil, err
	}
	result.Proposal = &proposal
	if proposal.Outcome == "no_change" && proposal.RunID == runID && proposal.JobID == queued.JobID && proposal.TableOID == 0 && proposal.Column == 0 && len(proposal.Columns) == 0 && len(proposal.Reason) > 0 && len(proposal.Reason) <= 1000 {
		result.Outcome, result.DiagnosticReason = "no_change", proposal.Reason
		return &result, finishInvestigation(ctx, state, claim, result, epoch)
	}
	if ValidateDiagnosticProposal(modelEvidence, proposal, time.Now()) != nil {
		return nil, errors.New("managed scheduling proposal does not match its bounded evidence")
	}
	if err := cleanupManagedCronWitnesses(ctx, state, owner, project.ID()); err != nil {
		return nil, err
	}
	witness, err := CreateManagedCronWitness(ctx, owner, project.ID(), runID+"/native-witness", time.Now().Add(time.Minute), func(ctx context.Context) error { return permission(ctx, ProjectCronIntent{}) }, func(ctx context.Context, w ManagedCronWitness) error {
		return SaveManagedCronWitness(ctx, state, project.ID(), epoch, w)
	})
	if err != nil {
		return nil, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
		defer cancel()
		_ = cleanupManagedCronWitnesses(cleanup, state, owner, project.ID())
	}()
	// Actual scheduler runs, not a synthetic acknowledgement, establish progress.
	if err := waitOwnedCleanup(ctx, 4500*time.Millisecond); err != nil {
		return nil, err
	}
	enrolled.Target.WitnessJobID = witness.JobID
	baseline, err := ReadProjectCron(ctx, owner, enrolled.Target)
	if err != nil {
		return nil, err
	}
	if baseline.Schedule != enrolled.Schedule || baseline.Generation != enrolled.Generation {
		return nil, errors.New("enrolled scheduling identity changed during model admission")
	}
	observed, generation, err := readCronClock(ctx, owner)
	if err != nil {
		return nil, err
	}
	request := ProjectCronRequest{Target: enrolled.Target, Window: enrolled.Window, Baseline: baseline, Generation: generation, RunID: runID, ObservedAt: observed, ManagedPeriods: periods}
	pending := managedCronAction{RunID: runID, Enrollment: enrolled, Queries: queries, Evidence: evidence}
	journal, err := pgx.ConnectConfig(ctx, state.Config().Copy())
	if err != nil {
		return nil, err
	}
	defer closeVacuumConnection(journal)
	var fence pgx.Tx
	applied, err := ApplyProjectCron(ctx, m.Config, request, ProjectCronGuards{CheckPermission: permission, CheckCapacity: func(ctx context.Context, _ ProjectCronIntent) error { return checkOwnedRepairCapacity(ctx, owner, nil) }, RecordIntent: func(ctx context.Context, intent ProjectCronIntent) error {
		pending.Intent = intent
		if err := recordOwnedProjectAction(ctx, state, project.ID(), epoch, claim, "cron", pending); err != nil {
			return err
		}
		var err error
		fence, err = lockOwnedIndexEpoch(ctx, state, project.ID(), epoch, claim)
		return err
	}, RecordWitnessFence: func(ctx context.Context, generation string) error {
		return SaveManagedCronWitnessFence(ctx, journal, witness, generation)
	}})
	if fence != nil {
		err = errors.Join(err, releaseOwnedIndexEpoch(state, fence))
	}
	if err != nil {
		return nil, err
	}
	pending.Receipt = &applied
	pending.VerifyAfter = applied.FirstFutureCycle.Add(3*time.Duration(enrolled.Window.EveryMinutes)*time.Minute + 2*time.Second)
	after := enrolled
	after.After = true
	after.Schedule = applied.After.Schedule
	after.Generation = applied.After.Generation
	if err := owner.QueryRow(ctx, `SELECT encode(sha256(convert_to(to_jsonb(j)::text,'UTF8')),'hex') FROM cron.job j WHERE jobid=$1 AND xmin::text=$2`, queued.JobID, applied.After.Generation).Scan(&after.Fingerprint); err != nil {
		return nil, err
	}
	pending.Followup = &after
	if _, err := ReadManagedCronHistory(ctx, owner, after); err != nil {
		return nil, err
	}
	result.Outcome = "awaiting_verification"
	result.ManagedCron = managedCronReport(pending, result.Outcome, "The future schedule changed. Waiting for three actual periods of comparable database work.")
	return &result, publishManagedCronAction(ctx, state, project.ID(), epoch, claim, claim.ID, pending, result, &pending.VerifyAfter, true)
}

func managedCronReport(a managedCronAction, outcome, reason string) *ManagedCronReport {
	r := &ManagedCronReport{Window: a.Intent.Window, JobID: a.Enrollment.Target.JobID, Before: a.Evidence.Periods, Original: a.Evidence.Original, Alternate: a.Evidence.Alternate, Outcome: outcome, Reason: reason, ApplicationRequestVisibility: "unobserved", VerifyAfter: a.VerifyAfter}
	if a.Receipt != nil {
		r.Applied = !a.Receipt.CommitAt.IsZero()
		r.CommitAt = a.Receipt.CommitAt
	}
	return r
}

func (m *ManagedProjectCron) finish(ctx context.Context, state, owner *pgx.Conn, project supabaseproject.Project, epoch int64, claim investigationClaim, origin int64, a managedCronAction, result DispatchResult) (*DispatchResult, error) {
	current, err := ReadProjectCron(ctx, owner, a.Intent.Target)
	outcome, reason := "inconclusive", "The schedule has no durable commit acknowledgement. It was inspected without repeating the change."
	if err != nil {
		return nil, err
	}
	if a.Receipt == nil || a.Followup == nil {
		if current == a.Intent.Before {
			reason = "The original schedule remains after an interrupted action. No change was repeated."
		}
	} else if current != a.Receipt.After {
		reason = "The schedule changed independently. That owner change was preserved."
	} else {
		now, generation, e := readCronClock(ctx, owner)
		if e != nil {
			return nil, e
		}
		if !generation.same(a.Intent.Generation) {
			reason = "The scheduler restarted during verification; a comparable result is unavailable."
		} else if now.Before(a.VerifyAfter) {
			result.Outcome = "awaiting_verification"
			result.ManagedCron = managedCronReport(a, result.Outcome, "Waiting for three actual future scheduling periods.")
			return &result, publishManagedCronAction(ctx, state, project.ID(), epoch, claim, origin, a, result, &a.VerifyAfter, false)
		} else {
			// The saved observation cannot outrun current provider history or
			// independent edits to the enrolled job.
			if _, err := ReadManagedCronHistory(ctx, owner, *a.Followup); err != nil {
				return nil, err
			}
			observations, e := ReadManagedCronObservations(ctx, state, project.ID(), epoch, a.Intent.Target.JobID, a.Receipt.FirstFutureCycle)
			if e != nil {
				return nil, e
			}
			periods, e := BuildManagedCronPeriods(observations, a.Queries, a.Intent.Window, a.Intent.Target.JobID, a.Receipt.FirstFutureCycle, true)
			if e != nil {
				reason = "Matched future database observations are incomplete; no improvement is claimed."
			} else {
				history, err := projectCronRuns(ctx, owner, a.Intent.Target.JobID, a.Receipt.FirstFutureCycle, a.Receipt.FirstFutureCycle.Add(3*time.Duration(a.Intent.Window.EveryMinutes)*time.Minute))
				if err != nil {
					return nil, err
				}
				if len(history) != len(periods) {
					periods = nil
				} else {
					for i, period := range periods {
						if period.Run != history[i] {
							periods = nil
							break
						}
					}
				}
				outcome, reason = VerifyManagedCronPeriods(a.Evidence.Periods, periods, a.Intent.Window)
				result.ManagedCron = managedCronReport(a, outcome, reason)
				result.ManagedCron.After = periods
			}
		}
	}
	result.Outcome, result.DiagnosticReason = outcome, reason
	if result.ManagedCron == nil {
		result.ManagedCron = managedCronReport(a, outcome, reason)
	}
	return &result, publishManagedCronAction(ctx, state, project.ID(), epoch, claim, origin, a, result, nil, false)
}

// The diagnostic witness is an internal temporary identity, not a customer
// enrollment choice. Every saved customer field must still match.
func sameManagedCronEnrollment(a, b ManagedCronEnrollment) bool {
	a.Target.WitnessJobID, b.Target.WitnessJobID = 0, 0
	return a == b
}
