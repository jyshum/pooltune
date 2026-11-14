package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"
)

// Recovery intake is independent of the ordinary two-attempt claim budget.
// It schedules inspection only; a pending intent never authorizes another mutation.
func ensureCronRecoveryClaim(ctx context.Context, state *pgx.Conn, project, id string) error {
	return pgx.BeginTxFunc(ctx, state, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		var locked string
		if err := tx.QueryRow(ctx, "SELECT project_id FROM pooltune_worker.projects WHERE project_id=$1 FOR UPDATE", project).Scan(&locked); errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence,parent_id)
 SELECT j.project_id,j.database_oid,j.role_oid,j.query_id,j.evidence,j.id
 FROM pooltune_worker.investigations j JOIN `+journalTable+` r ON r.id=$2
 WHERE j.project_id=$1 AND r.status IN ('cron_pending','cron_verifying') AND (r.report->>'job_id')::bigint IN
 (WITH RECURSIVE chain AS (SELECT id,parent_id FROM pooltune_worker.investigations WHERE id=j.id UNION ALL SELECT p.id,p.parent_id FROM pooltune_worker.investigations p JOIN chain c ON c.parent_id=p.id) SELECT id FROM chain)
 AND j.finished_at IS NOT NULL AND NOT EXISTS(SELECT FROM pooltune_worker.investigations child WHERE child.parent_id=j.id)
 AND NOT EXISTS(SELECT FROM pooltune_worker.investigations open WHERE open.project_id=$1 AND open.finished_at IS NULL)
 ORDER BY j.id DESC LIMIT 1 ON CONFLICT(parent_id) DO NOTHING`, project, id)
		return err
	})
}
func cronSave(result *DispatchResult, r *CronReport) {
	result.Outcome = r.Outcome
	result.Cron = r
	if r.Evidence.RunID != "" {
		result.Evidence = &r.Evidence
		result.Proposal = &r.Proposal
		result.ThreadID = r.Evidence.RunID
		result.GraphMS = r.GraphMS
	}
}
func cronDefer(result *DispatchResult, r *CronReport, outcome string, at time.Time) {
	r.Outcome = outcome
	cronSave(result, r)
	result.continuationAt = &at
}
func DispatchFixtureCron(ctx context.Context, state *pgx.Conn, config *pgx.ConnConfig, route string, fixture ConsumerFixture, investigate IndexInvestigator) (*DispatchResult, error) {
	if investigate == nil || ValidateConsumerFixtureID(fixture.ID) != nil || fixture.Enrollment.Project != route+"/consumer/"+fixture.ID {
		return nil, errors.New("Cron dispatch requires exact enrolled private fixture")
	}
	if err := prepareCronPeriods(ctx, state); err != nil {
		return nil, err
	}
	if err := ensureCronRecoveryClaim(ctx, state, fixture.Enrollment.Project, fixture.ID); err != nil {
		return nil, err
	}
	return dispatchInvestigation(ctx, state, fixture.Enrollment.Project, func(ctx context.Context, claim investigationClaim, result *DispatchResult) error {
		var status string
		var raw []byte
		if err := state.QueryRow(ctx, "SELECT status,report FROM "+journalTable+" WHERE id=$1 AND target=$2", fixture.ID, journalTargetConfig(config, route)).Scan(&status, &raw); err != nil {
			return err
		}
		r := CronReport{Fixture: fixture.ID, JobID: claim.ID}
		if raw != nil {
			if err := json.Unmarshal(raw, &r); err != nil {
				return err
			}
		}
		if raw != nil {
			var belongs bool
			err := state.QueryRow(ctx, `WITH RECURSIVE chain AS (
              SELECT id,parent_id FROM pooltune_worker.investigations WHERE id=$1 AND project_id=$2
              UNION ALL SELECT p.id,p.parent_id FROM pooltune_worker.investigations p JOIN chain c ON c.parent_id=p.id
            ) SELECT EXISTS(SELECT FROM chain c JOIN pooltune_worker.investigations root ON root.id=c.id WHERE root.id=$3 AND root.project_id=$2 AND root.database_oid=$4 AND root.role_oid=$5 AND root.query_id=$6)`, claim.ID, claim.Project, r.JobID, claim.Database, claim.Role, claim.Query).Scan(&belongs)
			if err != nil {
				return err
			}
			if !belongs {
				result.Outcome = "insufficient_evidence"
				return nil
			}
		}
		if status == "cron_verified" || status == "cron_declined" {
			cronSave(result, &r)
			return nil
		}
		target, err := pgx.ConnectConfig(ctx, config)
		if err != nil {
			return err
		}
		defer closeVacuumConnection(target)
		if err = lockTrial(ctx, target); err != nil {
			return err
		}
		f, err := readCronFixture(ctx, state, fixture.ID)
		if err != nil {
			return err
		}
		if raw == nil {
			tx, e := target.Begin(ctx)
			if e != nil {
				return e
			}
			p, e := readLookupPlan(ctx, tx, "EXPLAIN (FORMAT JSON, VERBOSE) "+cronQuery(trialSchema(fixture.ID)))
			var same bool
			if e == nil {
				e = tx.QueryRow(ctx, "SELECT current_user::regrole::oid=$1 AND (SELECT oid FROM pg_database WHERE datname=current_database())=$2", claim.Role, claim.Database).Scan(&same)
			}
			tx.Rollback(ctx)
			if e != nil {
				return e
			}
			if !same || p.QueryID != claim.Query {
				result.Outcome = "insufficient_evidence"
				return nil
			}
		}
		now, g, err := readCronClock(ctx, target)
		if err != nil {
			return err
		}
		final := func(outcome, reason string) error {
			r.Outcome = outcome
			r.Reason = reason
			if err := writeCronReport(ctx, state, "cron_declined", &r); err != nil {
				return err
			}
			cronSave(result, &r)
			return nil
		}
		if f.Activated.IsZero() || !g.same(f.Generation) || !now.Before(f.Expires) {
			return final("inconclusive", "missing activation, changed scheduler generation, or expired fixture")
		}
		current, err := readCronJob(ctx, target, f.Target.Name)
		if err != nil {
			return err
		}
		if err = validateCronFixture(ctx, target, state, fixture.ID, f, current.Schedule); err != nil {
			return final("inconclusive", err.Error())
		}
		if status == "cron_pending" || status == "cron_verifying" {
			if current.Schedule != cronAlternate {
				return final("inconclusive", "pending schedule absent or independently changed; mutation is never repeated")
			}
			if r.CommitAt.IsZero() || len(r.PostWitness) < 2 {
				return final("inconclusive", "mutation acknowledgement or post-commit scheduler proof missing; inspect without repeat")
			}
			if now.Before(r.VerifyAfter) {
				cronDefer(result, &r, "awaiting_verification", r.VerifyAfter)
				return nil
			}
			r.After, err = loadCronPeriods(ctx, state, fixture.ID, r.FirstFutureCycle)
			if err != nil {
				return err
			}
			r.History, err = readCronRuns(ctx, target, f.Target.ID)
			if err != nil {
				return err
			}
			if err = validateCronLifetime(f, r.History, r.FirstFutureCycle, now); err != nil {
				return final("regressed", err.Error())
			}
			r.Outcome, r.Reason = verifyCronPeriods(r.Before, r.After)
			r.CycleWaitMS = now.Sub(r.CommitAt).Seconds() * 1000
			if err = writeCronReport(ctx, state, "cron_verified", &r); err != nil {
				return err
			}
			cronSave(result, &r)
			return nil
		}
		if status != "cron_ready" && status != "cron_waiting" {
			return final("inconclusive", "unrecognized Cron action state")
		}
		if current.Schedule != cronOriginal || !current.Active {
			return final("inconclusive", "original enrolled schedule is not active")
		}
		evidenceDue := f.FirstCycle.Add(4*time.Minute + 82*time.Second)
		if now.Before(evidenceDue) {
			r.Outcome = "awaiting_evidence"
			if err = writeCronReport(ctx, state, "cron_waiting", &r); err != nil {
				return err
			}
			cronDefer(result, &r, "awaiting_evidence", evidenceDue)
			return nil
		}
		r.Before, err = loadCronPeriods(ctx, state, fixture.ID, f.FirstCycle)
		if err != nil {
			return err
		}
		if err = validateCronPeriods(r.Before, true); err != nil {
			return final("inconclusive", err.Error())
		}
		last := f.FirstCycle.Add(4 * time.Minute)
		deadline := last.Add(105 * time.Second)
		if !now.Before(last.Add(96 * time.Second)) {
			return final("inconclusive", "eligible handoff window expired before model admission")
		}
		r.History, err = readCronRuns(ctx, target, f.Target.ID)
		if err != nil {
			return err
		}
		if err = validateCronCycles(f.Target.ID, r.History, f.FirstCycle, 3, false); err != nil {
			return final("inconclusive", err.Error())
		}
		r.PreWitness, err = readCronRuns(ctx, target, f.Witness.ID)
		if err != nil {
			return err
		}
		if err = validateCronWitness(f.Witness.ID, r.PreWitness, last.Add(time.Minute), deadline, now, f.Generation, g); err != nil {
			return final("inconclusive", err.Error())
		}
		objects, err := expectedFixture(ctx, state, fixture.ID)
		if err != nil {
			return err
		}
		tx, err := target.Begin(ctx)
		if err != nil {
			return err
		}
		plan, err := readLookupPlan(ctx, tx, "EXPLAIN (FORMAT JSON, VERBOSE) "+cronQuery(trialSchema(fixture.ID)))
		if err != nil {
			tx.Rollback(ctx)
			return err
		}
		var bound bool
		err = tx.QueryRow(ctx, "SELECT current_user::regrole::oid=$1 AND (SELECT oid FROM pg_database WHERE datname=current_database())=$2", claim.Role, claim.Database).Scan(&bound)
		if err == nil && bound && plan.QueryID == claim.Query && len(claim.Generation) == 3 {
			bound, err = currentLookupCounters(ctx, tx, EnrolledLookup{Database: claim.Database, Role: claim.Role, Query: claim.Query}, [3]time.Time(claim.Generation))
		} else {
			bound = false
		}
		tx.Rollback(ctx)
		if err != nil {
			return err
		}
		if !bound {
			return final("inconclusive", "claimed outer query or counter generation changed")
		}
		r.Evidence = IndexEvidence{At: time.Now().UTC(), Decision: Investigate, TableOID: objects.Candidate, Column: 1, Label: "lookup", Plan: PlanEvidence{Node: plan.Node.Type}, Cron: &CronEvidence{JobID: f.Target.ID, Original: cronOriginal, Alternate: cronAlternate, Periods: r.Before, IdentityVerified: true, RequiredRuns: 1, CycleSeconds: 120, DeadlineSeconds: 120}}
		if !cronCause(r.Evidence) {
			return final("no_change", "Cron cause not qualified")
		}
		beforeModelClock := now
		if err = runIndexInvestigator(ctx, claim, result, r.Evidence, investigate); err != nil {
			return err
		}
		if result.Proposal == nil {
			return final("no_change", "investigator did not propose a repair")
		}
		r.Evidence = *result.Evidence
		r.Proposal = *result.Proposal
		r.GraphMS = result.GraphMS
		if r.Proposal.Outcome != "propose" || r.Proposal.Repair != "cron" {
			return final("no_change", "model declined enrolled alternate window")
		}
		r.FirstFutureCycle = last.Add(2 * time.Minute)
		r.VerifyAfter = r.FirstFutureCycle.Add(6*time.Minute + 2*time.Second)
		started := time.Now()
		// Persist intent while holding catalog row locks, before the schedule mutation.
		err = pgx.BeginFunc(ctx, target, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout='2s'; SET LOCAL lock_timeout='250ms'"); err != nil {
				return err
			}
			var locked int64
			if err := tx.QueryRow(ctx, "SELECT jobid FROM cron.job WHERE jobid=$1 FOR UPDATE", f.Target.ID).Scan(&locked); err != nil {
				return err
			}
			now, g, err = readCronClock(ctx, tx)
			if err != nil {
				return err
			}
			if now.Before(beforeModelClock) {
				return errors.New("target database clock reversed during investigation")
			}
			if err = validateCronWitness(f.Witness.ID, r.PreWitness, last.Add(time.Minute), deadline.Add(-8*time.Second), now, f.Generation, g); err != nil {
				return err
			}
			if err = validateCronFixture(ctx, tx, state, fixture.ID, f, cronOriginal); err != nil {
				return err
			}
			var active bool
			if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT FROM cron.job_run_details WHERE jobid=$1 AND (end_time IS NULL OR status NOT IN ('succeeded','failed')))", f.Target.ID).Scan(&active); err != nil {
				return err
			}
			if active {
				return errors.New("target execution active before mutation")
			}
			r.IntentAt = now
			r.Outcome = "awaiting_verification"
			if err = writeCronReport(ctx, state, "cron_pending", &r); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, "SELECT cron.alter_job($1,schedule := $2)", f.Target.ID, cronAlternate)
			return err
		})
		if err != nil {
			return err
		}
		r.CommitAt, g, err = readCronClock(ctx, target)
		if err != nil {
			return err
		}
		if !g.same(f.Generation) || r.CommitAt.Before(r.IntentAt) || !r.CommitAt.Before(deadline) {
			return errors.New("ambiguous Cron commit acknowledgement")
		}
		if err = writeCronReport(ctx, state, "cron_pending", &r); err != nil {
			return err
		}
		lastClock := r.CommitAt
		for {
			r.PostWitness, err = readCronRuns(ctx, target, f.Witness.ID)
			if err != nil {
				return err
			}
			now, g, err = readCronClock(ctx, target)
			if err != nil {
				return err
			}
			if now.Before(lastClock) {
				return errors.New("target database clock reversed after commit")
			}
			lastClock = now
			if validateCronWitness(f.Witness.ID, r.PostWitness, r.CommitAt, deadline, now, f.Generation, g) == nil {
				break
			}
			if !g.same(f.Generation) || !now.Before(deadline) || now.Sub(r.CommitAt) > 8*time.Second {
				return errors.New("post-commit scheduler progress unavailable; reconciliation required")
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(300 * time.Millisecond):
			}
		}
		r.ActionMS = elapsedMS(started)
		if err = writeCronReport(ctx, state, "cron_verifying", &r); err != nil {
			return err
		}
		cronDefer(result, &r, "awaiting_verification", r.VerifyAfter)
		return nil
	})
}
func validateCronLifetime(f CronFixture, runs []CronRun, firstFuture, now time.Time) error {
	original, alternate := []CronRun{}, []CronRun{}
	for _, r := range runs {
		if r.Start.Before(firstFuture) {
			original = append(original, r)
		} else {
			alternate = append(alternate, r)
		}
	}
	n := int(firstFuture.Sub(f.FirstCycle) / (2 * time.Minute))
	if err := validateCronCycles(f.Target.ID, original, f.FirstCycle, n, false); err != nil {
		return err
	}
	// Verification is shortly after the third complete cycle and before the next
	// alternate due time. Any extra run is a frequency failure.
	if now.Before(firstFuture.Add(6 * time.Minute)) {
		return errors.New("future cycle deadlines have not passed")
	}
	return validateCronCycles(f.Target.ID, alternate, firstFuture, 3, true)
}
