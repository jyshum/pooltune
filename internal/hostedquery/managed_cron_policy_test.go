package hostedquery

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func managedCronPolicyExample(after bool) ManagedCronEvidence {
	first := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	boot := first.Add(-time.Hour)
	if after {
		first = first.Add(15 * time.Minute)
	}
	e := ManagedCronEvidence{JobID: 17, Original: "*/5 * * * *", Alternate: "2-59/5 * * * *", Window: ProjectCronWindowPlan{EveryMinutes: 5, CurrentMinute: 0, ProposedMinute: 2, DeadlineSeconds: 200, MaxRuntimeSeconds: 20}, Generation: CronGeneration{PID: 7, Started: boot, Postmaster: boot}, ApplicationRequestVisibility: "unobserved"}
	for i := range 3 {
		cycle := first.Add(time.Duration(i) * 5 * time.Minute)
		due := cycle
		if after {
			due = cycle.Add(2 * time.Minute)
		}
		p := ManagedCronPeriod{Cycle: cycle, Run: CronRun{ID: int64(i + 1), JobID: 17, PID: 25, Start: due, End: due.Add(10 * time.Second), Status: "succeeded"}}
		if after {
			p.Run.ID += 3
		}
		window := func(start time.Time, busy, quiet bool) ManagedCronWindow {
			measure := func(query int64, mean float64, idle bool) ManagedCronMeasure {
				b := Counter{Database: 1, Role: 2, Query: query, Boot: boot, Reset: boot, Entry: boot}
				b.Calls = int64(start.Sub(boot)/time.Second) * 100
				b.ExecutionMS = float64(b.Calls) * 100
				a := b
				if !idle {
					a.Calls += 50
					a.ExecutionMS += 50 * mean
				}
				return ManagedCronMeasure{b, a}
			}
			mean := 2.
			if busy && !after {
				mean = 42
			}
			return ManagedCronWindow{Start: start, End: start.Add(20 * time.Second), Target: measure(11, mean, quiet), Read: measure(12, 1, false), Write: measure(13, 1, false)}
		}
		p.Busy = window(cycle, true, false)
		p.Control = window(cycle.Add(30*time.Second), false, false)
		p.Quiet = window(cycle.Add(2*time.Minute), false, true)
		if !after {
			p.Blocks = []CronBlock{{At: cycle.Add(time.Second), RunID: p.Run.ID, JobID: 17, PID: 25, BackendStart: cycle, WaitingPID: 26, WaitingRole: 2, WaitingStart: cycle, Query: 11}}
		}
		e.Periods = append(e.Periods, p)
	}
	return e
}

func TestManagedCronPolicyKeepsApplicationVisibilityUnknown(t *testing.T) {
	e := managedCronPolicyExample(false)
	if !ManagedCronCause(e) {
		t.Fatal("qualified database-observed cause refused")
	}
	raw, _ := json.Marshal(e)
	for _, invented := range []string{`"offered"`, `"errors"`, `"missed"`} {
		if strings.Contains(string(raw), invented) {
			t.Fatal("invented application telemetry")
		}
	}
	after := managedCronPolicyExample(true)
	if result, reason := VerifyManagedCronPeriods(e.Periods, after.Periods, e.Window); result != "improved_database_observed" {
		t.Fatalf("verification %s: %s", result, reason)
	}
	for _, mutate := range []func(*ManagedCronEvidence){
		func(e *ManagedCronEvidence) { e.ApplicationRequestVisibility = "zero_errors" },
		func(e *ManagedCronEvidence) { e.Periods[1].Blocks = nil },
		func(e *ManagedCronEvidence) { e.Periods[1].Run.Status = "failed" },
		func(e *ManagedCronEvidence) { e.Periods[1].Busy.Read.After.Calls = 25 },
		func(e *ManagedCronEvidence) { e.Periods[1].Busy.Target.After.Reset = e.Periods[1].Cycle },
		func(e *ManagedCronEvidence) { e.Periods[1].Blocks[0].PID = 9 },
	} {
		bad := managedCronPolicyExample(false)
		mutate(&bad)
		if ManagedCronCause(bad) {
			t.Fatal("unsupported/changed cause admitted")
		}
	}
	after.Periods[1].Quiet.Write.After.ExecutionMS = after.Periods[1].Quiet.Write.Before.ExecutionMS + 50*10
	if result, _ := VerifyManagedCronPeriods(e.Periods, after.Periods, e.Window); result != "regressed" {
		t.Fatal("quiet-window protected regression missed")
	}
}

func TestManagedCronVerificationRefusesReusedRunsAndCounterRewind(t *testing.T) {
	before := managedCronPolicyExample(false)
	after := managedCronPolicyExample(true)
	for i := range after.Periods {
		after.Periods[i].Run.ID += 3
	}
	after.Periods[0].Run.ID = before.Periods[0].Run.ID
	if result, _ := VerifyManagedCronPeriods(before.Periods, after.Periods, before.Window); result != "inconclusive" {
		t.Fatal("reused pre-action scheduler run accepted")
	}
	after = managedCronPolicyExample(true)
	for i := range after.Periods {
		after.Periods[i].Run.ID += 3
		for _, w := range []*ManagedCronWindow{&after.Periods[i].Busy, &after.Periods[i].Control, &after.Periods[i].Quiet} {
			for _, m := range []*ManagedCronMeasure{&w.Target, &w.Read, &w.Write} {
				m.Before.Calls -= 100000
				m.After.Calls -= 100000
				m.Before.ExecutionMS -= 10000000
				m.After.ExecutionMS -= 10000000
			}
		}
	}
	if result, _ := VerifyManagedCronPeriods(before.Periods, after.Periods, before.Window); result != "inconclusive" {
		t.Fatal("counter rewind across action accepted")
	}
}
