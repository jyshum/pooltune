package hostedquery

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCronWitnessGenerationUsesExactInstantsAcrossRepresentations(t *testing.T) {
	boundary := time.Date(2026, 11, 14, 12, 1, 0, 0, time.UTC)
	live := CronGeneration{PID: 41, Started: boundary.Add(-time.Hour), Postmaster: boundary.Add(-2 * time.Hour)}
	runs := []CronRun{
		{ID: 1, JobID: 7, Start: boundary.Add(2 * time.Second), End: boundary.Add(3 * time.Second), Status: "succeeded"},
		{ID: 2, JobID: 7, Start: boundary.Add(4 * time.Second), End: boundary.Add(5 * time.Second), Status: "succeeded"},
	}
	for _, zone := range []*time.Location{time.FixedZone("database UTC", 0), time.FixedZone("database offset", -8*3600)} {
		t.Run(zone.String(), func(t *testing.T) {
			current := live
			current.Started = current.Started.In(zone)
			current.Postmaster = current.Postmaster.In(zone)
			data, err := json.Marshal(live)
			if err != nil {
				t.Fatal(err)
			}
			var saved CronGeneration
			if err := json.Unmarshal(data, &saved); err != nil {
				t.Fatal(err)
			}
			if err := validateCronWitness(7, runs, boundary, boundary.Add(45*time.Second), boundary.Add(8*time.Second), saved, current); err != nil {
				t.Fatalf("same scheduler instant rejected after JSON round trip: %v", err)
			}
			for _, changed := range []string{"pid", "started", "postmaster"} {
				t.Run(changed, func(t *testing.T) {
					other := current
					switch changed {
					case "pid":
						other.PID++
					case "started":
						other.Started = other.Started.Add(time.Nanosecond)
					case "postmaster":
						other.Postmaster = other.Postmaster.Add(time.Nanosecond)
					}
					if err := validateCronWitness(7, runs, boundary, boundary.Add(45*time.Second), boundary.Add(8*time.Second), saved, other); err == nil {
						t.Fatal("actual scheduler generation change admitted")
					}
				})
			}
		})
	}
}

func TestCronWitnessRequiresTwoLaterStartsAndStableGeneration(t *testing.T) {
	boundary := time.Date(2026, 11, 14, 12, 1, 0, 0, time.UTC)
	generation := CronGeneration{PID: 41, Started: boundary.Add(-time.Hour), Postmaster: boundary.Add(-2 * time.Hour)}
	run := func(id int64, seconds int) CronRun {
		return CronRun{ID: id, JobID: 7, Start: boundary.Add(time.Duration(seconds) * time.Second), End: boundary.Add(time.Duration(seconds+1) * time.Second), Status: "succeeded"}
	}
	cases := []struct {
		name    string
		runs    []CronRun
		now     time.Time
		changed bool
		ok      bool
	}{
		{"two subsequent loops", []CronRun{run(1, 2), run(2, 4)}, boundary.Add(8 * time.Second), false, true},
		{"prequeued first witness cannot count", []CronRun{run(1, -1), run(2, 2)}, boundary.Add(8 * time.Second), false, false},
		{"one stale cache loop before commit", []CronRun{run(1, 2)}, boundary.Add(8 * time.Second), false, false},
		{"duplicate is one start", []CronRun{run(1, 2), run(1, 2)}, boundary.Add(8 * time.Second), false, false},
		{"delayed second", []CronRun{run(1, 2), run(2, 46)}, boundary.Add(48 * time.Second), false, false},
		{"restart", []CronRun{run(1, 2), run(2, 4)}, boundary.Add(8 * time.Second), true, false},
		{"clock reversed", []CronRun{run(1, 2), run(2, 4)}, boundary.Add(3 * time.Second), false, false},
		{"expired margin", []CronRun{run(1, 2), run(2, 4)}, boundary.Add(46 * time.Second), false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			current := generation
			if c.changed {
				current.PID++
			}
			err := validateCronWitness(7, c.runs, boundary, boundary.Add(45*time.Second), c.now, generation, current)
			if (err == nil) != c.ok {
				t.Fatalf("witness admitted=%v want=%v: %v", err == nil, c.ok, err)
			}
		})
	}
}

func TestCronHistoryRequiresExactFullWorkEachCycle(t *testing.T) {
	first := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	runs := []CronRun{}
	for i := 0; i < 3; i++ {
		s := first.Add(time.Duration(i*120+60) * time.Second)
		runs = append(runs, CronRun{ID: int64(i + 1), JobID: 9, Start: s, End: s.Add(17 * time.Second), Status: "succeeded"})
	}
	for _, mode := range []string{"valid", "missing", "duplicate", "late", "failed", "active", "foreign"} {
		t.Run(mode, func(t *testing.T) {
			r := append([]CronRun(nil), runs...)
			switch mode {
			case "missing":
				r = r[:2]
			case "duplicate":
				r = append(r, r[1])
			case "late":
				r[1].End = first.Add(240 * time.Second)
			case "failed":
				r[1].Status = "failed"
			case "active":
				r[1].End = time.Time{}
			case "foreign":
				r[1].JobID = 8
			}
			err := validateCronCycles(9, r, first, 3, true)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("%s: %v", mode, err)
			}
		})
	}
}

func TestCronEvidenceAndVerificationRequireRealComparablePeriods(t *testing.T) {
	first := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	metric := func(mean float64, calls int64) CronMeasure {
		return CronMeasure{Before: Counter{Database: 1, Role: 2, Query: 3, Boot: first, Reset: first, Entry: first}, After: Counter{Database: 1, Role: 2, Query: 3, Boot: first, Reset: first, Entry: first, Calls: calls, ExecutionMS: mean * float64(calls)}, Offered: calls}
	}
	periods := func(slow bool) []CronPeriod {
		p := []CronPeriod{}
		for i := 0; i < 3; i++ {
			cycle := first.Add(time.Duration(i) * 2 * time.Minute)
			mean := 2.
			if slow {
				mean = 42.
			}
			p = append(p, CronPeriod{Cycle: cycle, Busy: CronWindow{Start: cycle, End: cycle.Add(20 * time.Second), Target: metric(mean, 40), Healthy: metric(1, 80), Write: metric(1, 40)}, Control: CronWindow{Start: cycle.Add(30 * time.Second), End: cycle.Add(50 * time.Second), Target: metric(2, 40), Healthy: metric(1, 80), Write: metric(1, 40)}, Quiet: CronWindow{Start: cycle.Add(time.Minute), End: cycle.Add(80 * time.Second), Healthy: metric(1, 80), Write: metric(1, 40)}, Blocks: []CronBlock{{At: cycle.Add(time.Second), RunID: int64(i + 1), JobID: 9, PID: 42, BackendStart: cycle, WaitingPID: 99, WaitingStart: cycle, Query: 3}}})
		}
		return p
	}
	before, after := periods(true), periods(false)
	if err := validateCronPeriods(before, true); err != nil {
		t.Fatal(err)
	}
	outcome, _ := verifyCronPeriods(before, after)
	if outcome != "improved_tested_inputs" {
		t.Fatal(outcome)
	}
	for _, mode := range []string{"few", "overlap_only", "small_relative", "small_absolute", "missing_guard", "reset", "demand", "errors", "quiet_target", "healthy_identity", "noise"} {
		t.Run(mode, func(t *testing.T) {
			b, a := periods(true), periods(false)
			switch mode {
			case "few":
				b = b[:2]
			case "overlap_only":
				b[1].Blocks = nil
			case "small_relative":
				b[1].Busy.Target = metric(102, 40)
				b[1].Control.Target = metric(82, 40)
			case "small_absolute":
				b[1].Busy.Target = metric(10, 40)
			case "missing_guard":
				a[1].Quiet.Write = CronMeasure{}
			case "reset":
				b[1].Busy.Target.After.Reset = first.Add(time.Second)
			case "demand":
				a[1].Busy.Target.Offered = 30
			case "errors":
				a[1].Quiet.Healthy.Errors = 1
			case "quiet_target":
				a[1].Quiet.Target.Offered = 1
			case "healthy_identity":
				a[1].Quiet.Healthy.Before.Query = 4
				a[1].Quiet.Healthy.After.Query = 4
			case "noise":
				for i, v := range []float64{22, 70, 30} {
					b[i].Busy.Target = metric(v, 40)
				}
				for i, v := range []float64{15, 50, 25} {
					a[i].Busy.Target = metric(v, 40)
				}
			}
			if err := validateCronPeriods(b, true); err == nil {
				outcome, _ := verifyCronPeriods(b, a)
				if outcome == "improved_tested_inputs" {
					t.Fatal("invalid evidence verified")
				}
			}
		})
	}
}
