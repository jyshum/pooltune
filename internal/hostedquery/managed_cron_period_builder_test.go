package hostedquery

import (
	"testing"
	"time"
)

func managedCronObservationsExample(e ManagedCronEvidence) []ManagedCronObservation {
	var out []ManagedCronObservation
	var runs []CronRun
	for _, p := range e.Periods {
		runs = append(runs, p.Run)
	}
	for _, p := range e.Periods {
		for _, w := range []ManagedCronWindow{p.Busy, p.Control, p.Quiet} {
			for _, end := range []bool{false, true} {
				o := ManagedCronObservation{At: w.Start, Generation: e.Generation, Runs: runs}
				for i, m := range []ManagedCronMeasure{w.Target, w.Read, w.Write} {
					c := m.Before
					if end {
						c = m.After
					}
					kind := "read"
					if i == 2 {
						kind = "write"
					}
					o.Counters = append(o.Counters, ManagedCronCounter{Counter: c, Kind: kind})
				}
				if end {
					o.At = w.End
				}
				out = append(out, o)
			}
		}
		out = append(out, ManagedCronObservation{At: p.Blocks[0].At, Generation: e.Generation, Blocks: p.Blocks})
	}
	return out
}

func TestManagedCronPeriodBuilderUsesActualBoundariesAndSchedulerGeneration(t *testing.T) {
	e := managedCronPolicyExample(false)
	observed := managedCronObservationsExample(e)
	q := ManagedCronQueries{Target: e.Periods[0].Busy.Target.Before, Read: e.Periods[0].Busy.Read.Before, Write: e.Periods[0].Busy.Write.Before}
	got, err := BuildManagedCronPeriods(observed, q, e.Window, e.JobID, e.Periods[0].Cycle, false)
	if err != nil {
		t.Fatal(err)
	}
	e.Periods = got
	if !ManagedCronCause(e) {
		t.Fatal("assembled real windows did not retain attribution")
	}
	// A launcher restart between windows must not be hidden by stable pg_stat_statements counters.
	observed[2].Generation.PID++
	observed[3].Generation.PID++
	if _, err := BuildManagedCronPeriods(observed, q, e.Window, e.JobID, e.Periods[0].Cycle, false); err == nil {
		t.Fatal("scheduler restart between windows accepted")
	}
	observed = managedCronObservationsExample(e)
	observed[0].At = observed[0].At.Add(2 * time.Second)
	if _, err := BuildManagedCronPeriods(observed, q, e.Window, e.JobID, e.Periods[0].Cycle, false); err == nil {
		t.Fatal("missing aligned snapshot silently invented")
	}
}

func TestManagedCronQueryDiscoveryRequiresUniqueRoleAndDistinctProtectedQueries(t *testing.T) {
	e := managedCronPolicyExample(false)
	o := managedCronObservationsExample(e)[0]
	o.Blocks = e.Periods[0].Blocks
	q, err := SelectManagedCronQueries(o)
	if err != nil || q.Target.Query != 11 || q.Read.Query != 12 || q.Write.Query != 13 {
		t.Fatal("actual blocked query was not selected", err)
	}
	duplicate := o.Counters[0]
	duplicate.Counter.Role++
	o.Counters = append(o.Counters, duplicate)
	if q, err := SelectManagedCronQueries(o); err != nil || q.Target.Role != o.Blocks[0].WaitingRole {
		t.Fatal("actual waiter role was not selected", err)
	}
	o.Counters = o.Counters[1:]
	if _, err := SelectManagedCronQueries(o); err == nil {
		t.Fatal("missing actual role was substituted")
	}
}

func TestManagedCronQueryDiscoveryRequiresObservedWaiterRole(t *testing.T) {
	e := managedCronPolicyExample(false)
	o := managedCronObservationsExample(e)[0]
	o.Blocks = e.Periods[0].Blocks
	o.Blocks[0].WaitingRole = o.Counters[0].Counter.Role + 1
	if _, err := SelectManagedCronQueries(o); err == nil {
		t.Fatal("different role sharing query ID was attributed to observed waiter")
	}
	e.Periods[0].Blocks[0].WaitingRole++
	if ManagedCronCause(e) {
		t.Fatal("cause accepted mismatched waiter role")
	}
}
