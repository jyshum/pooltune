package hostedquery

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
)

// OwnedCronRequest binds discovered query counters to an exact owned Rookery
// fixture. The routine digests come from the fixture enrollment, not the model.
type OwnedCronRequest struct {
	Target                      ProjectCronTarget
	Candidate                   ProjectIndexTarget
	Runtime                     OwnedCronRuntime
	Observed                    Counter
	ReadOID, ProcedureOID       uint32
	ReadDigest, ProcedureDigest string
	JobID                       int64
	RunID                       string
	FirstCycle, Expires         time.Time
}

// OwnedCronPeriodReader reads real observations archived by a trusted fixture
// observer. The normal query detector alone cannot supply the blocking witnesses.
type OwnedCronPeriodReader func(context.Context, OwnedCronRequest, time.Time) ([]CronPeriod, error)

type OwnedCronAction struct {
	Intent   ProjectCronIntent
	Observed Counter
	After    ProjectCronSnapshot
	Report   CronReport
}

type OwnedCronGuards struct {
	CheckPermission       func(context.Context, ProjectCronIntent) error
	CheckCapacity         func(context.Context, *pgx.Conn, ProjectCronIntent) error
	AdmitModel            func(context.Context) error
	RecordIntent          func(context.Context, OwnedCronAction) error
	RecordAcknowledgement func(context.Context, OwnedCronAction) error
}

type OwnedCronDecision struct {
	Report    CronReport
	NotBefore time.Time
}

func ownedCronWindow() ProjectCronWindowPlan {
	return ProjectCronWindowPlan{EveryMinutes: 2, CurrentMinute: 0, ProposedMinute: 1, DeadlineSeconds: 120, MaxRuntimeSeconds: 20}
}

func ownedCronQuery(schema string) string {
	return cronQuery(schema) + " FROM (VALUES (1)) AS invocation(slot)"
}

func ownedCronHandoffDeadline(firstFuture time.Time) time.Time {
	return firstFuture.Add(-15 * time.Second)
}

// RunOwnedCronRepair makes at most one guarded schedule change. A saved action
// always uses inspection, including when its commit acknowledgement is absent.
// The caller must load the trusted action first and persist the unique queue
// continuation whenever NotBefore is set.
func RunOwnedCronRepair(ctx context.Context, cfg *pgx.ConnConfig, req OwnedCronRequest, prior *OwnedCronAction,
	read OwnedCronPeriodReader, investigate IndexInvestigator, guards OwnedCronGuards) (OwnedCronDecision, error) {
	if !validOwnedCronRoute(cfg, req) || req.JobID <= 0 || req.RunID == "" || len(req.RunID) > 64 || read == nil {
		return OwnedCronDecision{}, errors.New("Cron repair requires an exact owned Rookery route and period recorder")
	}
	if prior == nil && (investigate == nil || guards.CheckPermission == nil || guards.CheckCapacity == nil ||
		guards.AdmitModel == nil || guards.RecordIntent == nil || guards.RecordAcknowledgement == nil) {
		return OwnedCronDecision{}, errors.New("Cron repair requires model admission, owner permission and durable action recording")
	}
	conn, err := pgx.ConnectConfig(ctx, cfg.Copy())
	if err != nil {
		return OwnedCronDecision{}, err
	}
	defer closeVacuumConnection(conn)
	provider := newOwnedCronProvider(req)
	if err := provider.check(ctx, conn); err != nil {
		return OwnedCronDecision{}, err
	}
	now, generation, err := provider.clock(ctx, conn)
	if err != nil {
		return OwnedCronDecision{}, err
	}
	report := CronReport{Fixture: req.Candidate.Schema, JobID: req.JobID}
	if prior != nil {
		report = prior.Report
	}
	if !req.Expires.After(now) || !generation.Postmaster.Equal(req.Observed.Boot) {
		return ownedCronResult(report, "inconclusive", "fixture lifetime or database generation changed"), nil
	}
	node, err := readOwnedCronScope(ctx, conn, req, nil)
	if err != nil {
		return ownedCronResult(report, "inconclusive", "owned function, relation or discovered workload identity changed"), nil
	}
	if prior != nil {
		return verifyOwnedCronFollowup(ctx, conn, req, *prior, read, now, generation)
	}

	due := req.FirstCycle.Add(4*time.Minute + 82*time.Second)
	cutoff := req.FirstCycle.Add(4*time.Minute + 96*time.Second)
	future := req.FirstCycle.Add(6 * time.Minute)
	verifyAfter := future.Add(6*time.Minute + 2*time.Second)
	if !req.Expires.After(verifyAfter) || !now.Before(cutoff) {
		return ownedCronResult(report, "inconclusive", "the approved Cron evidence or verification window expired"), nil
	}
	if now.Before(due) {
		return deferOwnedCron(report, "awaiting_evidence", due), nil
	}
	before, err := read(ctx, req, req.FirstCycle)
	if err != nil {
		return OwnedCronDecision{}, err
	}
	if len(before) < 3 {
		return deferOwnedCron(report, "awaiting_evidence", minTime(now.Add(time.Second), cutoff)), nil
	}
	if !ownedCronPeriodsMatch(req, before, req.FirstCycle) || validateCronPeriods(before, true) != nil {
		return ownedCronResult(report, "inconclusive", "three comparable periods of attributed Cron contention were unavailable"), nil
	}
	if _, err := readOwnedCronScope(ctx, conn, req, &before[0].Busy); err != nil {
		return ownedCronResult(report, "inconclusive", "protected traffic evidence did not match the fixture"), nil
	}
	baseline, err := ReadProjectCron(ctx, conn, req.Target)
	if err != nil || baseline.Schedule != cronOriginal {
		return ownedCronResult(report, "inconclusive", "the exact original Cron schedule was unavailable"), nil
	}
	now, actualGeneration, err := provider.clock(ctx, conn)
	if err != nil || !actualGeneration.same(generation) || !now.Before(cutoff) {
		return ownedCronResult(report, "inconclusive", "Cron passed its safe handoff window"), nil
	}
	actionReq := ProjectCronRequest{Target: req.Target, Window: ownedCronWindow(), Baseline: baseline, Generation: generation,
		RunID: req.RunID, ObservedAt: now, Periods: before, ownedHandoff: true}
	err = pgx.BeginTxFunc(ctx, conn, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		expected, err := verifyProjectCronHandoff(ctx, tx, actionReq, now, generation)
		if err == nil && !expected.Equal(future) {
			return errors.New("Cron fixture cycle shifted")
		}
		return err
	})
	if err != nil {
		return ownedCronResult(report, "inconclusive", "scheduler history did not prove the exact safe handoff"), nil
	}
	intent := ProjectCronIntent{Target: req.Target, Window: actionReq.Window, Before: baseline, Generation: generation,
		RunID: req.RunID, ProposedSchedule: cronAlternate, FirstFutureCycle: future}
	if err := guards.CheckPermission(ctx, intent); err != nil {
		return OwnedCronDecision{}, err
	}
	if err := guards.AdmitModel(ctx); err != nil {
		return OwnedCronDecision{}, err
	}
	evidence := IndexEvidence{RunID: req.RunID, At: time.Now().UTC(), Decision: Investigate, TableOID: req.Candidate.TableOID,
		Column: 1, Label: "lookup", Plan: PlanEvidence{Node: node}, Cron: &CronEvidence{JobID: req.Target.JobID, Original: cronOriginal,
			Alternate: cronAlternate, Periods: before, IdentityVerified: true, RequiredRuns: 1, CycleSeconds: 120, DeadlineSeconds: 120}}
	started := time.Now()
	proposal, err := investigate(ctx, evidence)
	if err != nil {
		return OwnedCronDecision{}, err
	}
	report.Before, report.Evidence, report.Proposal, report.GraphMS = before, evidence, proposal, elapsedMS(started)
	if proposal.Outcome == "no_change" && proposal.RunID == evidence.RunID && proposal.TableOID == evidence.TableOID &&
		proposal.Column == evidence.Column && (proposal.Repair == "" || proposal.Repair == "cron") &&
		len(proposal.Columns) == 0 && len(proposal.Reason) > 0 && len(proposal.Reason) <= 1000 {
		return ownedCronResult(report, "no_change", proposal.Reason), nil
	}
	if proposal.Repair != "cron" || len(proposal.Columns) != 0 || ValidateDiagnosticProposal(evidence, proposal, time.Now()) != nil {
		return ownedCronResult(report, "no_change", "the proposal did not authorize the exact alternate Cron window"), nil
	}
	report.FirstFutureCycle, report.VerifyAfter = future, verifyAfter
	var action OwnedCronAction
	started = time.Now()
	applied, err := applyProjectCron(ctx, cfg, actionReq, ProjectCronGuards{
		CheckPermission: func(ctx context.Context, i ProjectCronIntent) error {
			if !sameOwnedCronIntent(intent, i) {
				return errors.New("Cron intent no longer matches the approved fixture")
			}
			return guards.CheckPermission(ctx, i)
		},
		CheckCapacity: func(ctx context.Context, i ProjectCronIntent) error { return guards.CheckCapacity(ctx, conn, i) },
		RecordIntent: func(ctx context.Context, i ProjectCronIntent) error {
			if _, err := readOwnedCronScope(ctx, conn, req, &before[0].Busy); err != nil {
				return err
			}
			if err := conn.QueryRow(ctx, "SELECT pg_catalog.clock_timestamp()").Scan(&report.IntentAt); err != nil {
				return err
			}
			action = OwnedCronAction{Intent: i, Observed: req.Observed, Report: report}
			return guards.RecordIntent(ctx, action)
		},
	}, provider)
	if err != nil {
		return OwnedCronDecision{}, err
	}
	report.ActionMS, report.CommitAt, report.PostWitness = elapsedMS(started), applied.CommitAt, applied.PostCommitWitnesses
	action.After, action.Report = applied.After, report
	if err := guards.RecordAcknowledgement(ctx, action); err != nil {
		return OwnedCronDecision{}, err
	}
	return deferOwnedCron(report, "awaiting_verification", verifyAfter), nil
}

func validOwnedCronRoute(cfg *pgx.ConnConfig, req OwnedCronRequest) bool {
	return validOwnedCronFixtureRoute(cfg, req) && req.Target.DatabaseOID == req.Observed.Database &&
		req.Target.OwnerOID == req.Observed.Role && req.Observed.Query != 0 &&
		!req.Observed.Boot.IsZero() && !req.Observed.Reset.IsZero() && !req.Observed.Entry.IsZero()
}

func validOwnedCronFixtureRoute(cfg *pgx.ConnConfig, req OwnedCronRequest) bool {
	prefix := "supabase/" + OwnedIndexRookeryRef + "/"
	installation, match := strings.CutPrefix(req.Target.Project, prefix)
	project, err := supabaseproject.New(OwnedIndexRookeryRef, installation)
	if !match || err != nil || project.ID() != req.Target.Project ||
		(req.Runtime != (OwnedCronRuntime{}) && (!validOwnedCronRuntime(req) || !validOwnedCronCounterView(req))) ||
		req.FirstCycle.IsZero() || !req.FirstCycle.Equal(cronCycle(req.FirstCycle)) || req.Expires.IsZero() ||
		req.Candidate.Schema != "pt_hosted_cr_"+installation || req.Candidate.Relation != "candidate" ||
		req.Candidate.TableOID == 0 || req.Candidate.DatabaseOID == 0 || req.Candidate.DatabaseOID != req.Target.DatabaseOID ||
		req.Target.OwnerOID == 0 ||
		req.ReadOID == 0 || req.ProcedureOID == 0 || req.ReadOID == req.ProcedureOID ||
		len(req.ReadDigest) != 64 || len(req.ProcedureDigest) != 64 ||
		req.Target.JobID <= 0 || req.Target.WitnessJobID <= 0 || req.Target.WitnessJobID == req.Target.JobID ||
		req.Target.Name != req.Candidate.Schema+"_target" ||
		req.Target.CommandDigest != ownedCronDigest("CALL "+qualified(req.Candidate.Schema, cronProcedure)+"(200)") {
		return false
	}
	host := "db." + OwnedIndexRookeryRef + ".supabase.co"
	return cfg != nil && cfg.Host == host && cfg.Port == 5432 && cfg.Database == "postgres" && cfg.User == req.Candidate.Schema &&
		len(cfg.Fallbacks) == 0 && cfg.TLSConfig != nil && !cfg.TLSConfig.InsecureSkipVerify && cfg.TLSConfig.ServerName == host && cfg.TLSConfig.RootCAs != nil
}

func ownedCronDigest(value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}

func sameOwnedCronIntent(want, actual ProjectCronIntent) bool {
	return want.Target == actual.Target && want.Window == actual.Window && want.Before == actual.Before &&
		want.Generation.same(actual.Generation) && want.RunID == actual.RunID && want.ProposedSchedule == actual.ProposedSchedule &&
		want.FirstFutureCycle.Equal(actual.FirstFutureCycle)
}

func readOwnedCronScope(ctx context.Context, conn *pgx.Conn, req OwnedCronRequest, controls *CronWindow) (node string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	err = pgx.BeginTxFunc(ctx, conn, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL statement_timeout='750ms'; SET LOCAL lock_timeout='150ms'"); err != nil {
			return err
		}
		useOwnedCounters := req.Runtime != (OwnedCronRuntime{})
		if useOwnedCounters {
			if err := checkOwnedCronCounterView(ctx, tx, req); err != nil {
				return err
			}
		}
		var valid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT FROM pg_database d JOIN pg_class c ON c.oid=$2 JOIN pg_namespace n ON n.oid=c.relnamespace
			WHERE d.oid=$1 AND d.datname=current_database() AND c.relname='candidate' AND n.nspname=$3
			  AND current_user=session_user AND current_user::regrole::oid=$4 AND c.relowner=$4 AND n.nspowner=$4
			  AND obj_description(n.oid,'pg_namespace')=$5 AND c.relkind='r' AND NOT (c.relispartition OR c.relrowsecurity OR c.relhasrules)
		) AND EXISTS (
			SELECT FROM cron.job WHERE jobid=$6 AND jobname::text=$3||'_witness'
			  AND username::text=current_user AND database::text=current_database() AND active AND command='SELECT 1' AND schedule='2 seconds'
		)`, req.Target.DatabaseOID, req.Candidate.TableOID, req.Candidate.Schema, req.Target.OwnerOID,
			"Pooltune hosted cron fixture "+req.Target.Project, req.Target.WitnessJobID).Scan(&valid); err != nil || !valid {
			return errors.New("Cron fixture ownership or exact witness could not be verified")
		}
		if err := tx.QueryRow(ctx, `SELECT NOT EXISTS (
			SELECT FROM (VALUES ($1::text,$2::oid,'f',$3::text),($4::text,$5::oid,'p',$6::text)) want(signature,oid,kind,digest)
			WHERE NOT EXISTS(SELECT FROM pg_proc p WHERE p.oid=to_regprocedure(want.signature) AND p.oid=want.oid
			  AND p.proowner=$7 AND p.prokind::text=want.kind AND p.provolatile='v' AND NOT p.prosecdef
			  AND encode(sha256(convert_to(pg_get_functiondef(p.oid),'UTF8')),'hex')=want.digest
			  AND NOT EXISTS(SELECT FROM aclexplode(coalesce(p.proacl,acldefault('f',p.proowner))) a WHERE a.grantee<>p.proowner))
		)`, qualified(req.Candidate.Schema, cronReadFunction)+"()", req.ReadOID, req.ReadDigest,
			qualified(req.Candidate.Schema, cronProcedure)+"(integer)", req.ProcedureOID, req.ProcedureDigest,
			req.Target.OwnerOID).Scan(&valid); err != nil || !valid {
			return errors.New("owned Cron routine definition or ownership changed")
		}
		plan, err := readLookupPlan(ctx, tx, "EXPLAIN (FORMAT JSON, VERBOSE) "+ownedCronQuery(req.Candidate.Schema))
		if err != nil || plan.QueryID != req.Observed.Query || plan.Node.Type == "" {
			return errors.New("the discovered Cron workload no longer matches its exact query")
		}
		node = plan.Node.Type
		checkCounter := func(lane string, c Counter) error {
			if c.Database != req.Target.DatabaseOID || c.Role != req.Target.OwnerOID {
				return errors.New("Cron traffic role changed")
			}
			var ok bool
			var err error
			if useOwnedCounters {
				ok, err = currentOwnedCronCounters(ctx, tx, req, lane, c)
			} else {
				ok, err = currentLookupCounters(ctx, tx, EnrolledLookup{Database: c.Database, Role: c.Role, Query: c.Query},
					[3]time.Time{c.Boot, c.Reset, c.Entry})
			}
			if err != nil || !ok {
				return errors.New("Cron traffic counter generation changed")
			}
			return nil
		}
		if err := checkCounter("target", req.Observed); err != nil || controls == nil {
			return err
		}
		for _, control := range []struct {
			lane, sql string
			arg       int32
			c         Counter
		}{
			{"healthy", ownedCronHealthyQuery(req.Candidate.Schema), 43, controls.Healthy.Before},
			{"write", ownedCronWriteQuery(req.Candidate.Schema), 90001, controls.Write.Before},
		} {
			p, err := readLookupPlan(ctx, tx, "EXPLAIN (FORMAT JSON, VERBOSE) "+control.sql, control.arg)
			if err != nil || p.QueryID != control.c.Query {
				return errors.New("Cron protected query identity changed")
			}
			if err := checkCounter(control.lane, control.c); err != nil {
				return err
			}
		}
		return nil
	})
	return node, err
}

func ownedCronPeriodsMatch(req OwnedCronRequest, periods []CronPeriod, first time.Time) bool {
	if len(periods) != 3 || !periods[0].Cycle.Equal(first) {
		return false
	}
	for _, period := range periods {
		if !ownedCronWindowMatches(req, period) {
			return false
		}
	}
	return true
}

func ownedCronWindowMatches(req OwnedCronRequest, period CronPeriod) bool {
	for _, window := range []CronWindow{period.Busy, period.Control, period.Quiet} {
		if !sameCronCounter(window.Target.Before, req.Observed) || !sameCronCounter(window.Target.After, req.Observed) {
			return false
		}
		for _, c := range []Counter{window.Healthy.Before, window.Write.Before} {
			if c.Database != req.Observed.Database || c.Role != req.Observed.Role || c.Query == req.Observed.Query ||
				!c.Boot.Equal(req.Observed.Boot) || !c.Reset.Equal(req.Observed.Reset) {
				return false
			}
		}
		if window.Healthy.Before.Query == window.Write.Before.Query {
			return false
		}
	}
	return true
}

func verifyOwnedCronFollowup(ctx context.Context, conn *pgx.Conn, req OwnedCronRequest, prior OwnedCronAction,
	read OwnedCronPeriodReader, now time.Time, generation CronGeneration) (OwnedCronDecision, error) {
	report := prior.Report
	future := req.FirstCycle.Add(6 * time.Minute)
	verifyAfter := future.Add(6*time.Minute + 2*time.Second)
	if prior.Intent.Target != req.Target || prior.Intent.Window != ownedCronWindow() || prior.Intent.ProposedSchedule != cronAlternate ||
		prior.Intent.Before.Schedule != cronOriginal || !prior.Intent.FirstFutureCycle.Equal(future) ||
		!sameCronCounter(prior.Observed, req.Observed) || !prior.Intent.Generation.same(generation) ||
		!ownedCronPeriodsMatch(req, report.Before, req.FirstCycle) {
		return ownedCronResult(report, "inconclusive", "the recorded Cron action did not match the current job and workload"), nil
	}
	current, err := ReadProjectCron(ctx, conn, req.Target)
	if err == nil && report.CommitAt.IsZero() {
		switch {
		case current == prior.Intent.Before:
			return ownedCronResult(report, "inconclusive", "the original Cron schedule was observed after an unacknowledged action"), nil
		case current.Schedule == cronAlternate && current.Fingerprint == prior.Intent.Before.Fingerprint &&
			current.Generation != prior.Intent.Before.Generation:
			return ownedCronResult(report, "inconclusive", "the alternate Cron schedule was observed without durable acknowledgement"), nil
		}
	}
	if err != nil || current != prior.After || current.Schedule != cronAlternate ||
		current.Fingerprint != prior.Intent.Before.Fingerprint || current.Generation == prior.Intent.Before.Generation ||
		report.CommitAt.IsZero() || !report.CommitAt.Before(future) ||
		!report.FirstFutureCycle.Equal(future) || !report.VerifyAfter.Equal(verifyAfter) {
		return ownedCronResult(report, "inconclusive", "Cron schedule or commit acknowledgement could not be reconciled"), nil
	}
	witnessDeadline := minTime(report.CommitAt.Add(7*time.Second), ownedCronHandoffDeadline(future))
	checkAt := witnessDeadline.Add(-time.Nanosecond)
	if checkAt.After(now) {
		checkAt = now
	}
	witnesses, err := projectCronRuns(ctx, conn, req.Target.WitnessJobID, report.CommitAt, witnessDeadline)
	if err != nil || len(report.PostWitness) < 2 ||
		validateCronWitness(req.Target.WitnessJobID, witnesses, report.CommitAt, witnessDeadline, checkAt, prior.Intent.Generation, generation) != nil {
		return ownedCronResult(report, "inconclusive", "post-commit scheduler progress could not be verified"), nil
	}
	report.PostWitness = witnesses
	if now.Before(verifyAfter) {
		return deferOwnedCron(report, "awaiting_verification", verifyAfter), nil
	}
	report.History, err = projectCronRuns(ctx, conn, req.Target.JobID, future, future.Add(6*time.Minute))
	if err != nil {
		return OwnedCronDecision{}, err
	}
	if err := validateCronCycles(req.Target.JobID, report.History, future, 3, true); err != nil {
		return ownedCronResult(report, "regressed", "the moved job did not complete once within each approved period"), nil
	}
	report.After, err = read(ctx, req, future)
	if err != nil {
		return OwnedCronDecision{}, err
	}
	if !ownedCronPeriodsMatch(req, report.After, future) {
		return ownedCronResult(report, "inconclusive", "the protected future traffic was missing or misaligned"), nil
	}
	report.CycleWaitMS = float64(now.Sub(report.CommitAt).Microseconds()) / 1000
	outcome, reason := verifyCronPeriods(report.Before, report.After)
	return ownedCronResult(report, outcome, reason), nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func ownedCronResult(report CronReport, outcome, reason string) OwnedCronDecision {
	report.Outcome, report.Reason = outcome, reason
	return OwnedCronDecision{Report: report}
}

func deferOwnedCron(report CronReport, outcome string, at time.Time) OwnedCronDecision {
	report.Outcome = outcome
	return OwnedCronDecision{Report: report, NotBefore: at}
}
