package hostedquery

import (
	"errors"
	"slices"
	"time"
)

// Managed scheduling observes completed database work. There are intentionally
// no application offered/error/missed fields: those facts are unobserved.
type ManagedCronMeasure struct {
	Before Counter `json:"before"`
	After  Counter `json:"after"`
}
type ManagedCronWindow struct {
	Start  time.Time          `json:"start"`
	End    time.Time          `json:"end"`
	Target ManagedCronMeasure `json:"target"`
	Read   ManagedCronMeasure `json:"read"`
	Write  ManagedCronMeasure `json:"write"`
}
type ManagedCronPeriod struct {
	Cycle   time.Time         `json:"cycle"`
	Run     CronRun           `json:"run"`
	Busy    ManagedCronWindow `json:"busy"`
	Control ManagedCronWindow `json:"control"`
	Quiet   ManagedCronWindow `json:"quiet"`
	Blocks  []CronBlock       `json:"blocks"`
}
type ManagedCronEvidence struct {
	JobID                        int64                 `json:"job_id"`
	Original                     string                `json:"original_schedule"`
	Alternate                    string                `json:"alternate_schedule"`
	Window                       ProjectCronWindowPlan `json:"window"`
	Generation                   CronGeneration        `json:"generation"`
	Periods                      []ManagedCronPeriod   `json:"periods"`
	ApplicationRequestVisibility string                `json:"application_request_visibility"`
}

func managedCronMean(m ManagedCronMeasure) (float64, int64, error) {
	d, err := counterDelta(m.Before, m.After)
	if err != nil || !sameCronCounter(m.Before, m.After) {
		return 0, 0, errors.New("comparable completed database calls are missing")
	}
	if d.Calls == 0 {
		return 0, 0, nil
	}
	mean := d.ExecutionMS / float64(d.Calls)
	if !nonnegativeFinite(mean) {
		return 0, 0, errors.New("invalid database latency")
	}
	return mean, d.Calls, nil
}

func ManagedCronCause(e ManagedCronEvidence) bool {
	original, alternate, err := e.Window.schedules()
	if err != nil || e.JobID <= 0 || original != e.Original || alternate != e.Alternate || e.ApplicationRequestVisibility != "unobserved" || e.Generation.PID <= 0 || e.Generation.Started.IsZero() || e.Generation.Postmaster.IsZero() {
		return false
	}
	return validateManagedCronPeriods(e.Periods, e.Window, e.JobID, e.Generation, false, true) == nil
}

func validateManagedCronPeriods(periods []ManagedCronPeriod, window ProjectCronWindowPlan, job int64, generation CronGeneration, after, requireCause bool) error {
	if len(periods) != 3 {
		return errors.New("three complete scheduling periods are required")
	}
	if _, _, err := window.schedules(); err != nil {
		return err
	}
	cycleDuration := time.Duration(window.EveryMinutes) * time.Minute
	seenRuns := map[int64]bool{}
	var chronological []ManagedCronWindow

	for i, p := range periods {
		if seenRuns[p.Run.ID] {
			return errors.New("scheduler run reused across periods")
		}
		seenRuns[p.Run.ID] = true
		chronological = append(chronological, p.Busy, p.Control, p.Quiet)
		if p.Cycle.IsZero() || !p.Cycle.Equal(p.Cycle.UTC().Truncate(cycleDuration)) || (i > 0 && !p.Cycle.Equal(periods[i-1].Cycle.Add(cycleDuration))) {
			return errors.New("scheduling cycles are not consecutive")
		}
		oldDue := p.Cycle.Add(time.Duration(window.CurrentMinute) * time.Minute)
		newDue := p.Cycle.Add(time.Duration(window.ProposedMinute) * time.Minute)
		due := oldDue
		if after {
			due = newDue
		}
		if p.Run.JobID != job || p.Run.ID <= 0 || p.Run.PID <= 0 || p.Run.Status != "succeeded" || p.Run.Start.Before(due) || !p.Run.End.After(p.Run.Start) || p.Run.End.After(due.Add(20*time.Second)) || p.Run.End.Sub(p.Run.Start) > time.Duration(window.MaxRuntimeSeconds)*time.Second || p.Run.End.After(p.Cycle.Add(time.Duration(window.DeadlineSeconds)*time.Second)) {
			return errors.New("job did not complete in its approved slot and deadline")
		}
		for n, w := range []ManagedCronWindow{p.Busy, p.Control, p.Quiet} {
			start := []time.Time{oldDue, oldDue.Add(30 * time.Second), newDue}[n]
			if w.Start.Before(start) || w.Start.After(start.Add(time.Second)) || w.End.Before(start.Add(20*time.Second)) || w.End.After(start.Add(21*time.Second)) {
				return errors.New("database windows are not aligned to the schedule")
			}
			for j, m := range []ManagedCronMeasure{w.Target, w.Read, w.Write} {
				base := []ManagedCronWindow{periods[0].Busy, periods[0].Control, periods[0].Quiet}[n]
				ref := []ManagedCronMeasure{base.Target, base.Read, base.Write}[j]
				_, calls, err := managedCronMean(m)
				_, reference, baseErr := managedCronMean(ref)
				if err != nil || baseErr != nil || (calls < 20 && !(n == 2 && j == 0 && calls == 0)) || !sameCronCounter(m.Before, ref.Before) || !m.Before.Boot.Equal(generation.Postmaster) || float64(calls) < float64(reference)*.75 || float64(calls) > float64(reference)*1.25 {
					return errors.New("completed-work identities, generations or volumes are not comparable")
				}
			}
		}
		if requireCause {
			busy, _, _ := managedCronMean(p.Busy.Target)
			control, _, _ := managedCronMean(p.Control.Target)
			if busy-control < 20 || busy < control*1.3 || len(p.Blocks) == 0 {
				return errors.New("repeated material job interference is not demonstrated")
			}
		}
		for _, b := range p.Blocks {
			if b.RunID != p.Run.ID || b.JobID != job || b.PID != p.Run.PID || b.WaitingPID <= 0 || b.WaitingRole != p.Busy.Target.Before.Role || b.WaitingPID == b.PID || b.BackendStart.IsZero() || b.WaitingStart.IsZero() || b.BackendStart.After(b.At) || b.WaitingStart.After(b.At) || b.Query != p.Busy.Target.Before.Query || b.At.Before(p.Run.Start) || b.At.After(p.Run.End) || b.At.Before(p.Busy.Start) || b.At.After(p.Busy.End) {
				return errors.New("blocking evidence is not bound to the actual job execution")
			}
		}
	}
	slices.SortFunc(chronological, func(a, b ManagedCronWindow) int { return a.Start.Compare(b.Start) })
	for i, w := range chronological {
		if w.Target.Before.Database != w.Read.Before.Database || w.Target.Before.Database != w.Write.Before.Database || sameCronCounter(w.Target.Before, w.Read.Before) || sameCronCounter(w.Target.Before, w.Write.Before) || sameCronCounter(w.Read.Before, w.Write.Before) {
			return errors.New("protection queries must be distinct in the same database")
		}
		if i == 0 {
			continue
		}
		prev := chronological[i-1]
		for j, m := range []ManagedCronMeasure{w.Target, w.Read, w.Write} {
			p := []ManagedCronMeasure{prev.Target, prev.Read, prev.Write}[j]
			if _, err := counterDelta(p.After, m.Before); err != nil {
				return errors.New("database counters moved backwards between windows")
			}
		}
	}
	return nil
}

// VerifyManagedCronPeriods reports database-observed protection only. Lower
// completed work cannot masquerade as lower query latency.
func VerifyManagedCronPeriods(before, after []ManagedCronPeriod, window ProjectCronWindowPlan) (string, string) {
	if len(before) != 3 || len(after) != 3 || !after[0].Cycle.After(before[2].Cycle) {
		return "inconclusive", "three original and three future periods are required"
	}
	generation := CronGeneration{Postmaster: before[0].Busy.Target.Before.Boot}
	job := before[0].Run.JobID
	if validateManagedCronPeriods(before, window, job, generation, false, true) != nil || validateManagedCronPeriods(after, window, job, generation, true, false) != nil {
		return "inconclusive", "database work, job completion or attribution was not comparable"
	}
	seen := make(map[int64]bool)
	var all []ManagedCronWindow
	for _, side := range [][]ManagedCronPeriod{before, after} {
		for _, period := range side {
			if seen[period.Run.ID] {
				return "inconclusive", "scheduler history reused across the schedule move"
			}
			seen[period.Run.ID] = true
			all = append(all, period.Busy, period.Control, period.Quiet)
		}
	}
	slices.SortFunc(all, func(a, b ManagedCronWindow) int { return a.Start.Compare(b.Start) })
	for i := 1; i < len(all); i++ {
		previous, current := all[i-1], all[i]
		for j, m := range []ManagedCronMeasure{current.Target, current.Read, current.Write} {
			p := []ManagedCronMeasure{previous.Target, previous.Read, previous.Write}[j]
			if _, err := counterDelta(p.After, m.Before); err != nil {
				return "inconclusive", "database counters moved backwards across the schedule move"
			}
		}
	}
	var beforeTarget, afterTarget float64
	for i := range before {
		for n, b := range []ManagedCronWindow{before[i].Busy, before[i].Control, before[i].Quiet} {
			a := []ManagedCronWindow{after[i].Busy, after[i].Control, after[i].Quiet}[n]
			for j, bm := range []ManagedCronMeasure{b.Target, b.Read, b.Write} {
				am := []ManagedCronMeasure{a.Target, a.Read, a.Write}[j]
				bmean, bc, _ := managedCronMean(bm)
				amean, ac, _ := managedCronMean(am)
				if !sameCronCounter(bm.Before, am.Before) || float64(ac) < float64(bc)*.75 || float64(ac) > float64(bc)*1.25 {
					return "inconclusive", "completed work or counter generation changed across the schedule move"
				}
				rel, absolute := .2, 5.
				if j == 2 {
					rel, absolute = .1, 2
				}
				if amean-bmean >= absolute && amean >= bmean*(1+rel) {
					return "regressed", "protected completed database work became slower"
				}
				if n == 0 && j == 0 {
					beforeTarget += bmean
					afterTarget += amean
				}
			}
		}
	}
	if beforeTarget-afterTarget >= 60 && afterTarget <= beforeTarget*.7 {
		return "improved_database_observed", "database latency improved with comparable completed work and on-time jobs; application-wide requests and errors remain unobserved"
	}
	return "inconclusive", "database observations did not demonstrate sufficient improvement"
}
