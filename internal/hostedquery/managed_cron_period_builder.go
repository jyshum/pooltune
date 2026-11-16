package hostedquery

import (
	"errors"
	"time"
)

type ManagedCronQueries struct{ Target, Read, Write Counter }

// SelectManagedCronQueries discovers the affected identity from an actual job
// blocker. Ambiguous same-query IDs across roles are refused.
func SelectManagedCronQueries(o ManagedCronObservation) (ManagedCronQueries, error) {
	var chosen ManagedCronQueries
	for _, block := range o.Blocks {
		chosen = ManagedCronQueries{}
		matches := 0
		for _, c := range o.Counters {
			if c.Kind == "read" && c.Counter.Query == block.Query && block.WaitingRole != 0 && c.Counter.Role == block.WaitingRole {
				chosen.Target = c.Counter
				matches++
			}
		}
		if matches != 1 {
			continue
		}
		for _, c := range o.Counters {
			if c.Counter.Database != chosen.Target.Database || c.Counter.Role != chosen.Target.Role || c.Counter.Query == chosen.Target.Query {
				continue
			}
			if c.Kind == "read" && chosen.Read.Query == 0 {
				chosen.Read = c.Counter
			}
			if c.Kind == "write" && chosen.Write.Query == 0 {
				chosen.Write = c.Counter
			}
		}
		if chosen.Read.Query != 0 && chosen.Write.Query != 0 {
			return chosen, nil
		}
	}
	return ManagedCronQueries{}, errors.New("no unambiguous blocked query with protected read and write counters")
}

func BuildManagedCronPeriods(observations []ManagedCronObservation, query ManagedCronQueries, window ProjectCronWindowPlan, job int64, first time.Time, after bool) ([]ManagedCronPeriod, error) {
	if len(observations) < 2 || job <= 0 || first.IsZero() {
		return nil, errors.New("scheduling observations are incomplete")
	}
	for _, o := range observations {
		if !o.Generation.same(observations[0].Generation) {
			return nil, errors.New("scheduler changed across observation windows")
		}
	}
	cycleDuration := time.Duration(window.EveryMinutes) * time.Minute
	if _, _, err := window.schedules(); err != nil {
		return nil, err
	}
	find := func(at time.Time) (ManagedCronObservation, error) {
		var best *ManagedCronObservation
		for i := range observations {
			o := &observations[i]
			if !o.At.Before(at) && !o.At.After(at.Add(time.Second)) && (best == nil || o.At.Before(best.At)) {
				best = o
			}
		}
		if best == nil {
			return ManagedCronObservation{}, errors.New("an aligned scheduling snapshot is missing")
		}
		return *best, nil
	}
	counter := func(o ManagedCronObservation, wanted Counter) (Counter, error) {
		for _, c := range o.Counters {
			if sameCronCounter(c.Counter, wanted) {
				return c.Counter, nil
			}
		}
		return Counter{}, errors.New("a protected query identity or generation changed")
	}
	makeWindow := func(at time.Time) (ManagedCronWindow, error) {
		b, err := find(at)
		if err != nil {
			return ManagedCronWindow{}, err
		}
		a, err := find(at.Add(20 * time.Second))
		if err != nil {
			return ManagedCronWindow{}, err
		}
		if !b.Generation.same(a.Generation) {
			return ManagedCronWindow{}, errors.New("scheduler changed within observation window")
		}
		w := ManagedCronWindow{Start: b.At, End: a.At}
		for i, wanted := range []Counter{query.Target, query.Read, query.Write} {
			before, err := counter(b, wanted)
			if err != nil {
				return w, err
			}
			after, err := counter(a, wanted)
			if err != nil {
				return w, err
			}
			m := ManagedCronMeasure{before, after}
			switch i {
			case 0:
				w.Target = m
			case 1:
				w.Read = m
			case 2:
				w.Write = m
			}
		}
		return w, nil
	}
	runs := map[int64]CronRun{}
	for _, o := range observations {
		for _, r := range o.Runs {
			if r.JobID == job {
				previous, exists := runs[r.ID]
				if !exists || r.End.After(previous.End) {
					runs[r.ID] = r
				}
			}
		}
	}
	var periods []ManagedCronPeriod
	for i := range 3 {
		cycle := first.Add(time.Duration(i) * cycleDuration)
		p := ManagedCronPeriod{Cycle: cycle}
		oldDue := cycle.Add(time.Duration(window.CurrentMinute) * time.Minute)
		newDue := cycle.Add(time.Duration(window.ProposedMinute) * time.Minute)
		var err error
		p.Busy, err = makeWindow(oldDue)
		if err != nil {
			return nil, err
		}
		p.Control, err = makeWindow(oldDue.Add(30 * time.Second))
		if err != nil {
			return nil, err
		}
		p.Quiet, err = makeWindow(newDue)
		if err != nil {
			return nil, err
		}
		due := oldDue
		if after {
			due = newDue
		}
		count := 0
		for _, run := range runs {
			if !run.Start.Before(cycle) && run.Start.Before(cycle.Add(cycleDuration)) {
				p.Run = run
				count++
			}
		}
		if count != 1 || p.Run.Start.Before(due) {
			return nil, errors.New("one exact job execution per cycle was not observed")
		}
		for _, o := range observations {
			for _, b := range o.Blocks {
				if b.JobID == job && b.Query == query.Target.Query && b.WaitingRole == query.Target.Role && !b.At.Before(p.Busy.Start) && !b.At.After(p.Busy.End) {
					p.Blocks = append(p.Blocks, b)
				}
			}
		}
		periods = append(periods, p)
	}
	return periods, nil
}
