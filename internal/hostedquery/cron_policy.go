package hostedquery

import (
	"errors"
	"time"
)

const cronOriginal = "*/2 * * * *"
const cronAlternate = "1-59/2 * * * *"
const cronWitnessSchedule = "2 seconds"
const cronReadFunction = "pooltune_cron_read"
const cronProcedure = "pooltune_cron_hold"

type CronGeneration struct {
	PID        int32     `json:"pid"`
	Started    time.Time `json:"started"`
	Postmaster time.Time `json:"postmaster"`
}

// Persistence and PostgreSQL decoding can use different locations for the same
// instant. Scheduler identity depends on exact timestamps, not their locations.
func (g CronGeneration) same(other CronGeneration) bool {
	return g.PID == other.PID && g.Started.Equal(other.Started) && g.Postmaster.Equal(other.Postmaster)
}

type CronRun struct {
	ID     int64     `json:"id"`
	JobID  int64     `json:"job_id"`
	PID    int32     `json:"pid"`
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	Status string    `json:"status"`
}

func validateCronWitness(job int64, runs []CronRun, after, deadline, now time.Time, expected, current CronGeneration) error {
	if job <= 0 || expected.PID <= 0 || expected.Started.IsZero() || expected.Postmaster.IsZero() || !expected.same(current) || !now.After(after) || !now.Before(deadline) {
		return errors.New("ambiguous scheduler generation, clock or expired handoff margin")
	}
	ids := map[int64]bool{}
	starts := map[time.Time]bool{}
	for _, r := range runs {
		if r.JobID == job && r.ID > 0 && r.Status == "succeeded" && r.Start.After(after) && !r.End.Before(r.Start) && !r.End.After(now) && !r.End.IsZero() && r.End.Before(deadline) {
			ids[r.ID] = true
			starts[r.Start] = true
		}
	}
	if len(ids) < 2 || len(starts) < 2 {
		return errors.New("two distinct successful later scheduler witness starts required")
	}
	return nil
}
func cronCycle(at time.Time) time.Time { return at.UTC().Truncate(2 * time.Minute) }
func validateCronCycles(job int64, runs []CronRun, first time.Time, count int, alternate bool) error {
	if job <= 0 || count < 1 || first.IsZero() || first != cronCycle(first) {
		return errors.New("invalid required cycle contract")
	}
	seen := make([]int, count)
	for _, r := range runs {
		if r.JobID != job {
			return errors.New("foreign job history")
		}
		index := int(r.Start.Sub(first) / (2 * time.Minute))
		if r.Start.Before(first) || index >= count {
			return errors.New("unexpected execution outside required lifetime")
		}
		due := first.Add(time.Duration(index) * 2 * time.Minute)
		if alternate {
			due = due.Add(time.Minute)
		}
		// The private procedure must finish within its 20-second slot, as well as
		// before the owner deadline. Late starts are not silently moved to a cycle.
		if r.ID <= 0 || r.Status != "succeeded" || r.Start.Before(due) || !r.Start.Before(due.Add(20*time.Second)) || !r.End.After(r.Start) || !r.End.Before(due.Add(20*time.Second)) {
			return errors.New("missing, failed, active or overdue required execution")
		}
		seen[index]++
	}
	for _, n := range seen {
		if n != 1 {
			return errors.New("missing or duplicate required execution")
		}
	}
	return nil
}
