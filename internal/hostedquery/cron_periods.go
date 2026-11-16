package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"math"
	"time"
)

type CronMeasure struct {
	Before  Counter `json:"before"`
	After   Counter `json:"after"`
	Offered int64   `json:"offered"`
	Errors  int64   `json:"errors"`
	Missed  int64   `json:"missed"`
}
type CronWindow struct {
	Start   time.Time   `json:"start"`
	End     time.Time   `json:"end"`
	Target  CronMeasure `json:"target"`
	Healthy CronMeasure `json:"healthy"`
	Write   CronMeasure `json:"write"`
}
type CronBlock struct {
	At           time.Time `json:"at"`
	RunID        int64     `json:"run_id"`
	JobID        int64     `json:"job_id"`
	PID          int32     `json:"pid"`
	BackendStart time.Time `json:"backend_start"`
	WaitingPID   int32     `json:"waiting_pid"`
	WaitingRole  uint32    `json:"waiting_role,omitempty"`
	WaitingStart time.Time `json:"waiting_start"`
	Query        int64     `json:"query_id,string"`
}
type CronPeriod struct {
	Cycle   time.Time   `json:"cycle"`
	Busy    CronWindow  `json:"busy"`
	Control CronWindow  `json:"control"`
	Quiet   CronWindow  `json:"quiet"`
	Blocks  []CronBlock `json:"blocks"`
}
type CronEvidence struct {
	JobID            int64        `json:"job_id"`
	Original         string       `json:"original_schedule"`
	Alternate        string       `json:"alternate_schedule"`
	Periods          []CronPeriod `json:"periods"`
	IdentityVerified bool         `json:"identity_verified"`
	RequiredRuns     int          `json:"required_runs_per_cycle"`
	CycleSeconds     int          `json:"cycle_seconds"`
	DeadlineSeconds  int          `json:"deadline_seconds"`
}
type CronReport struct {
	Fixture          string        `json:"fixture"`
	JobID            int64         `json:"job_id"`
	Outcome          string        `json:"outcome"`
	Reason           string        `json:"reason"`
	Before           []CronPeriod  `json:"before"`
	After            []CronPeriod  `json:"after"`
	Evidence         IndexEvidence `json:"evidence"`
	Proposal         IndexProposal `json:"proposal"`
	IntentAt         time.Time     `json:"intent_at"`
	CommitAt         time.Time     `json:"commit_at"`
	FirstFutureCycle time.Time     `json:"first_future_cycle"`
	VerifyAfter      time.Time     `json:"verify_after"`
	PreWitness       []CronRun     `json:"pre_witness"`
	PostWitness      []CronRun     `json:"post_witness"`
	History          []CronRun     `json:"history"`
	GraphMS          float64       `json:"investigation_wall_ms"`
	ActionMS         float64       `json:"action_wall_ms"`
	CycleWaitMS      float64       `json:"cycle_wait_ms"`
}

func cronMean(m CronMeasure) (float64, error) {
	o, err := Measure(m.Before, m.After, m.Offered)
	if err != nil {
		return 0, err
	}
	if m.Errors != 0 || m.Missed != 0 {
		return 0, errors.New("request errors or missed offered slots")
	}
	return o.MeanMS, nil
}
func sameCronCounter(a, b Counter) bool {
	return a.Database == b.Database && a.Role == b.Role && a.Query == b.Query && a.Boot.Equal(b.Boot) && a.Reset.Equal(b.Reset) && a.Entry.Equal(b.Entry)
}
func validateCronPeriod(p CronPeriod, attributed bool) error {
	if p.Cycle.IsZero() || p.Cycle != cronCycle(p.Cycle) {
		return errors.New("invalid cycle")
	}
	var prior *Counter
	for i, w := range []CronWindow{p.Busy, p.Control, p.Quiet} {
		if !sameCronCounter(p.Busy.Healthy.Before, w.Healthy.Before) || !sameCronCounter(p.Busy.Write.Before, w.Write.Before) {
			return errors.New("protected query identity or generation changed")
		}
		offset := []time.Duration{0, 30 * time.Second, time.Minute}[i]
		expected := p.Cycle.Add(offset)
		// Database-boundary snapshots can include bounded transport overhead.
		if w.Start.Before(expected) || w.Start.After(expected.Add(time.Second)) || w.End.Before(expected.Add(20*time.Second)) || w.End.After(expected.Add(21*time.Second)) {
			return errors.New("missing or mistimed actual period")
		}
		for j, m := range []CronMeasure{w.Target, w.Healthy, w.Write} {
			if i == 2 && j == 0 {
				if m.Offered != 0 || m.After.Calls != m.Before.Calls {
					return errors.New("target traffic appeared in quiet window")
				}
				continue
			}
			count := int64(40)
			if j == 1 {
				count = 80
			}
			if m.Offered != count {
				return errors.New("offered demand changed")
			}
			if _, err := cronMean(m); err != nil {
				return err
			}
			if j == 0 {
				if prior != nil {
					a, b := prior, m.Before
					if a.Database != b.Database || a.Role != b.Role || a.Query != b.Query || !a.Boot.Equal(b.Boot) || !a.Reset.Equal(b.Reset) || !a.Entry.Equal(b.Entry) {
						return errors.New("target counter generation changed")
					}
				}
				c := m.Before
				prior = &c
			}
		}
	}
	if attributed {
		busy, _ := cronMean(p.Busy.Target)
		control, _ := cronMean(p.Control.Target)
		if busy-control < 20 || busy < 1.3*control {
			return errors.New("affected period lacks required 30 percent and 20 ms contention cost")
		}
		if len(p.Blocks) == 0 {
			return errors.New("no attributed live blocking")
		}
		for _, b := range p.Blocks {
			if b.RunID <= 0 || b.JobID <= 0 || b.PID <= 0 || b.WaitingPID <= 0 || b.BackendStart.IsZero() || b.WaitingStart.IsZero() || b.Query != p.Busy.Target.Before.Query || b.At.Before(p.Busy.Start) || b.At.After(p.Busy.End) {
				return errors.New("unbound blocking observation")
			}
		}
	}
	return nil
}
func validateCronPeriods(periods []CronPeriod, attributed bool) error {
	if len(periods) != 3 {
		return errors.New("three complete actual periods required")
	}
	for i, p := range periods {
		if err := validateCronPeriod(p, attributed); err != nil {
			return err
		}
		if i > 0 {
			if !sameCronCounter(periods[0].Busy.Healthy.Before, p.Busy.Healthy.Before) || !sameCronCounter(periods[0].Busy.Write.Before, p.Busy.Write.Before) {
				return errors.New("protected period identity or generation changed")
			}
			if !p.Cycle.Equal(periods[i-1].Cycle.Add(2 * time.Minute)) {
				return errors.New("nonconsecutive periods")
			}
			a, b := periods[0].Busy.Target.Before, p.Busy.Target.Before
			if a.Database != b.Database || a.Role != b.Role || a.Query != b.Query || !a.Boot.Equal(b.Boot) || !a.Reset.Equal(b.Reset) || !a.Entry.Equal(b.Entry) {
				return errors.New("period identity or generation changed")
			}
		}
	}
	return nil
}
func cronCause(e IndexEvidence) bool {
	c := e.Cron
	return c != nil && e.Decision == Investigate && c.JobID > 0 && c.IdentityVerified && c.Original == cronOriginal && c.Alternate == cronAlternate && c.RequiredRuns == 1 && c.CycleSeconds == 120 && c.DeadlineSeconds == 120 && validateCronPeriods(c.Periods, true) == nil
}
func verifyCronPeriods(before, after []CronPeriod) (string, string) {
	if validateCronPeriods(before, true) != nil || validateCronPeriods(after, false) != nil {
		return "inconclusive", "missing comparable periods or protected traffic"
	}
	if !sameCronCounter(before[0].Busy.Healthy.Before, after[0].Busy.Healthy.Before) || !sameCronCounter(before[0].Busy.Write.Before, after[0].Busy.Write.Before) {
		return "inconclusive", "protected query identity or generation changed across repair"
	}
	a, b := before[0].Busy.Target.Before, after[0].Busy.Target.Before
	if a.Database != b.Database || a.Role != b.Role || a.Query != b.Query || !a.Boot.Equal(b.Boot) || !a.Reset.Equal(b.Reset) || !a.Entry.Equal(b.Entry) {
		return "inconclusive", "counter generation changed across repair"
	}
	values := func(ps []CronPeriod, window, kind int) []float64 {
		v := []float64{}
		for _, p := range ps {
			w := []CronWindow{p.Busy, p.Control, p.Quiet}[window]
			m := []CronMeasure{w.Target, w.Healthy, w.Write}[kind]
			n, _ := cronMean(m)
			v = append(v, n)
		}
		return v
	}
	for w := 0; w < 3; w++ {
		for k := 0; k < 3; k++ {
			if w == 2 && k == 0 {
				continue
			}
			bm, bd := meanMAD(values(before, w, k))
			am, ad := meanMAD(values(after, w, k))
			relative, absolute := .2, 5.
			if k == 2 {
				relative, absolute = .1, 2
			}
			d := am - bm
			if d >= relative*bm && d >= absolute && d > 3*math.Max(bd, ad) {
				return "regressed", "foreground or quiet-window protected traffic regressed"
			}
		}
	}
	bm, bd := meanMAD(values(before, 0, 0))
	am, ad := meanMAD(values(after, 0, 0))
	gain := bm - am
	if gain >= 5 && gain >= .2*bm && gain > 3*math.Max(bd, ad) {
		return "improved_tested_inputs", "three future foreground periods improved with observed quiet-window protection"
	}
	return "inconclusive", "latency benefit or noise gate not met"
}
func readCronRuns(ctx context.Context, db *pgx.Conn, job int64) ([]CronRun, error) {
	rows, err := db.Query(ctx, `SELECT runid,jobid,COALESCE(job_pid,0),COALESCE(start_time,'epoch'::timestamptz),COALESCE(end_time,'epoch'::timestamptz),status FROM cron.job_run_details WHERE jobid=$1 ORDER BY runid`, job)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []CronRun{}
	for rows.Next() {
		var r CronRun
		if err = rows.Scan(&r.ID, &r.JobID, &r.PID, &r.Start, &r.End, &r.Status); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
func writeCronReport(ctx context.Context, state *pgx.Conn, status string, r *CronReport) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tag, err := state.Exec(ctx, "UPDATE "+journalTable+" SET status=$2,report=$3::jsonb WHERE id=$1", r.Fixture, status, string(raw))
	if err == nil && tag.RowsAffected() != 1 {
		return errors.New("Cron report missing")
	}
	return err
}
func prepareCronPeriods(ctx context.Context, state *pgx.Conn) error {
	_, err := state.Exec(ctx, `CREATE TABLE IF NOT EXISTS pooltune_lab_state.cron_periods(fixture text NOT NULL,cycle timestamptz NOT NULL,payload jsonb NOT NULL,recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),PRIMARY KEY(fixture,cycle))`)
	return err
}
func loadCronPeriods(ctx context.Context, state *pgx.Conn, id string, first time.Time) ([]CronPeriod, error) {
	rows, err := state.Query(ctx, `SELECT payload FROM pooltune_lab_state.cron_periods WHERE fixture=$1 AND cycle >= $2 AND cycle < $2+interval '6 minutes' ORDER BY cycle`, id, first)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ps := []CronPeriod{}
	for rows.Next() {
		var raw []byte
		var p CronPeriod
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		ps = append(ps, p)
	}
	return ps, rows.Err()
}

// RecordCronPeriod accepts selected actual measurements from the private traffic
// harness. It binds them to the fixture, current counter identity and real history.
func RecordCronPeriod(ctx context.Context, target, state *pgx.Conn, route, id string, p CronPeriod) error {
	if ValidateConsumerFixtureID(id) != nil {
		return errors.New("invalid Cron fixture")
	}
	var bound bool
	if err := state.QueryRow(ctx, "SELECT EXISTS(SELECT FROM "+journalTable+" WHERE id=$1 AND target=$2 AND status LIKE 'cron_%')", id, journalTarget(target, route)).Scan(&bound); err != nil {
		return err
	}
	if !bound {
		return errors.New("period fixture binding mismatch")
	}
	f, err := readCronFixture(ctx, state, id)
	if err != nil {
		return err
	}
	now, g, err := readCronClock(ctx, target)
	if err != nil {
		return err
	}
	if !g.same(f.Generation) || p.Cycle.Before(f.FirstCycle) || now.Before(p.Quiet.End) || now.Sub(p.Quiet.End) > 15*time.Second {
		return errors.New("period is stale, future, or scheduler changed")
	}
	if err = validateCronPeriod(p, false); err != nil {
		return err
	}
	current, err := readCronJob(ctx, target, f.Target.Name)
	if err != nil {
		return err
	}
	if err = validateCronFixture(ctx, target, state, id, f, current.Schedule); err != nil {
		return err
	}
	tx, err := target.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	plan, err := readLookupPlan(ctx, tx, "EXPLAIN (FORMAT JSON, VERBOSE) "+cronQuery(trialSchema(id)))
	if err != nil {
		return err
	}
	var db, role uint32
	if err = target.QueryRow(ctx, "SELECT oid,current_user::regrole::oid FROM pg_database WHERE datname=current_database()").Scan(&db, &role); err != nil {
		return err
	}
	c := p.Busy.Target.Before
	if c.Database != db || c.Role != role || c.Query != plan.QueryID {
		return errors.New("period target identity mismatch")
	}
	if ok, err := currentLookupCounters(ctx, tx, EnrolledLookup{Database: db, Role: role, Query: plan.QueryID}, [3]time.Time{c.Boot, c.Reset, c.Entry}); err != nil || !ok {
		return errors.Join(err, errors.New("period counter generation mismatch"))
	}
	for _, guard := range []struct {
		m   CronMeasure
		sql string
		arg int32
	}{
		{p.Busy.Healthy, "SELECT id FROM ONLY " + qualified(trialSchema(id), "healthy") + " WHERE id=$1", 43},
		{p.Busy.Write, "UPDATE ONLY " + qualified(trialSchema(id), "healthy") + " SET padding=CASE padding WHEN 'a' THEN 'b' ELSE 'a' END WHERE id=$1 RETURNING id", 90001},
	} {
		gp, e := readLookupPlan(ctx, tx, "EXPLAIN (FORMAT JSON, VERBOSE) "+guard.sql, guard.arg)
		if e != nil {
			return e
		}
		gc := guard.m.Before
		if gc.Database != db || gc.Role != role || gc.Query != gp.QueryID {
			return errors.New("protected query identity mismatch")
		}
		if ok, e := currentLookupCounters(ctx, tx, EnrolledLookup{Database: db, Role: role, Query: gp.QueryID}, [3]time.Time{gc.Boot, gc.Reset, gc.Entry}); e != nil || !ok {
			return errors.Join(e, errors.New("protected counter generation mismatch"))
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	runs, err := readCronRuns(ctx, target, f.Target.ID)
	if err != nil {
		return err
	}
	for _, b := range p.Blocks {
		matched := false
		for _, r := range runs {
			if b.RunID == r.ID && b.JobID == r.JobID && b.PID == r.PID && !b.At.Before(r.Start) && !b.At.After(r.End) && !b.BackendStart.Before(r.Start.Add(-5*time.Second)) && !b.BackendStart.After(b.At) {
				matched = true
			}
		}
		if !matched {
			return errors.New("blocking observation does not bind actual owned run")
		}
	}
	if err = prepareCronPeriods(ctx, state); err != nil {
		return err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = state.Exec(ctx, "INSERT INTO pooltune_lab_state.cron_periods(fixture,cycle,payload) VALUES($1,$2,$3::jsonb)", id, p.Cycle, string(raw))
	return err
}
