package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"pooltune/internal/labdb"
	"pooltune/internal/supabaseproject"

	"github.com/jackc/pgx/v5"
)

const OwnedIndexRookeryRef = "osdjqwpzgyzwfaxyufze"

var ownedIndexScope = regexp.MustCompile(`^pt_hosted_ix_[0-9a-f]{16}$`)

type OwnedProjectIndex struct {
	Installation string
	OwnerRoleOID uint32
	QueryID      int64
	Target       ProjectIndexTarget
	Columns      []int16
	Inputs       []int32
	Config       *pgx.ConnConfig             `json:"-"`
	Investigate  IndexInvestigator           `json:"-"`
	AdmitModel   func(context.Context) error `json:"-"`
}

func (f *OwnedProjectIndex) Valid() bool {
	if f == nil || f.Config == nil || f.Investigate == nil || f.Target.DatabaseOID == 0 ||
		f.Target.TableOID == 0 || !ownedIndexScope.MatchString(f.Target.Schema) ||
		f.Target.Relation != "workload" || f.OwnerRoleOID == 0 || f.QueryID == 0 ||
		len(f.Columns) != 2 || len(f.Inputs) != 2 || !ValidIndexColumns(f.Columns[0], f.Columns) {
		return false
	}
	_, err := supabaseproject.New(OwnedIndexRookeryRef, f.Installation)
	return err == nil && f.Config.User == f.Target.Schema &&
		f.Config.Host == "db."+OwnedIndexRookeryRef+".supabase.co" && f.Config.Port == 5432 &&
		f.Config.Database == "postgres" && len(f.Config.Fallbacks) == 0 &&
		f.Config.TLSConfig != nil && !f.Config.TLSConfig.InsecureSkipVerify &&
		f.Config.TLSConfig.ServerName == f.Config.Host && f.Config.TLSConfig.RootCAs != nil
}

func (f *OwnedProjectIndex) Matches(project supabaseproject.Project) bool {
	return f.Valid() && project.Ref() == OwnedIndexRookeryRef &&
		strings.HasSuffix(project.ID(), "/"+f.Installation)
}

const projectIndexIntentSchema = `
	CREATE TABLE IF NOT EXISTS pooltune_worker.owned_index_actions (
		job_id bigint PRIMARY KEY REFERENCES pooltune_worker.investigations(id),
		project_id text NOT NULL,
		intent jsonb NOT NULL,
		outcome text NOT NULL DEFAULT 'pending',
		recorded_at timestamptz NOT NULL DEFAULT statement_timestamp()
	);`

func (f *OwnedProjectIndex) matchesClaim(project supabaseproject.Project, claim investigationClaim) bool {
	return f.Matches(project) && claim.Database == f.Target.DatabaseOID &&
		claim.Role == f.OwnerRoleOID && claim.Query == f.QueryID
}

func (f *OwnedProjectIndex) run(ctx context.Context, state, monitor *pgx.Conn, project supabaseproject.Project,
	epoch int64, claim investigationClaim, windows *ObservedLookupLatency, result *DispatchResult) error {
	result.ObservedLatency = windows
	var interrupted bool
	err := state.QueryRow(ctx, "SELECT EXISTS (SELECT FROM pooltune_worker.owned_index_actions WHERE job_id=$1 AND project_id=$2)",
		claim.ID, project.ID()).Scan(&interrupted)
	if err != nil {
		return err
	}
	if interrupted {
		result.Outcome, result.DiagnosticReason = "repair_interrupted", "a prior index action requires reconciliation"
		return nil
	}
	owner, err := pgx.ConnectConfig(ctx, f.Config.Copy())
	if err != nil {
		return err
	}
	defer labdb.Close(owner)
	catalog, err := ResolveProjectIndex(ctx, owner, f.Target, f.Columns[0], f.Columns)
	if err != nil {
		return err
	}
	verify := func(ctx context.Context) error {
		return f.recheck(ctx, state, monitor, project, epoch, claim, windows)
	}
	if err := verify(ctx); err != nil {
		return err
	}

	before, err := f.measure(ctx, owner, project, catalog.Keys)
	if err != nil {
		return err
	}
	runID := fmt.Sprintf("owned-index-%d-%d", claim.ID, claim.Attempt)
	evidence := IndexEvidence{RunID: runID, At: time.Now().UTC(), TableOID: f.Target.TableOID, Column: f.Columns[0],
		Columns: f.Columns, Existing: catalog.Covering, Decision: Investigate, Label: catalog.Keys[0].Name,
		Plan: PlanEvidence{Node: before.Node.Type, ExaminedRows: before.Node.Rows + before.Node.Filtered,
			ReturnedRows: before.Node.Rows, SharedBlocks: before.Node.Read + before.Node.Hit}}
	report := IndexTrialReport{RunID: runID, JobID: claim.ID, Evidence: evidence, Before: fixtureTiming(before), Measurement: "owned_single_input"}
	if catalog.Covering || before.Node.Type != "Seq Scan" || evidence.Plan.ExaminedRows < 10000 ||
		evidence.Plan.ReturnedRows <= 0 || evidence.Plan.ExaminedRows/evidence.Plan.ReturnedRows < 10 {
		result.Outcome, result.DiagnosticReason = "no_change", "the enrolled lookup no longer needs an index"
		return nil
	}
	if err := verify(ctx); err != nil {
		return err
	}
	if f.AdmitModel == nil {
		return errors.New("owned index needs model admission")
	}
	if err := f.AdmitModel(ctx); err != nil {
		if errors.Is(err, ErrProjectModelBudget) {
			result.Outcome, result.DiagnosticReason = "diagnosis_unavailable", "daily_model_limit"
			return nil
		}
		return err
	}
	started := time.Now()
	proposal, err := f.Investigate(ctx, evidence)
	report.ModelWallMS = elapsedMS(started)
	if err != nil {
		return err
	}
	result.Proposal, result.Evidence, result.ThreadID = &proposal, &evidence, runID
	report.Proposal = proposal
	if proposal.Outcome == "no_change" && proposal.RunID == runID && proposal.TableOID == evidence.TableOID &&
		proposal.Column == evidence.Column && slices.Equal(proposal.Columns, evidence.Columns) &&
		len(proposal.Reason) > 0 && len(proposal.Reason) <= 1000 {
		result.Outcome, result.DiagnosticReason = "no_change", proposal.Reason
		return nil
	}
	if err := qualifyIndex(evidence, proposal, time.Now()); err != nil {
		result.Outcome, result.DiagnosticReason = "no_change", err.Error()
		return nil
	}
	lockedGate := func(ctx context.Context, i ProjectIndexActionIntent) error {
		if i.Target != f.Target || i.RunID != runID {
			return ErrProjectIndexTargetChanged
		}
		return verify(ctx)
	}
	started = time.Now()
	var fence pgx.Tx
	var indexName string
	action, err := ApplyProjectIndex(ctx, f.Config, f.Target, evidence, proposal, ProjectIndexActionGuards{
		CheckPermission: lockedGate,
		CheckCapacity: func(ctx context.Context, i ProjectIndexActionIntent) error {
			var available bool
			e := owner.QueryRow(ctx, `SELECT sum(numbackends) * 3 < current_setting('max_connections')::int * 2 FROM pg_stat_database`).Scan(&available)
			if e == nil && !available {
				return errors.New("the database does not have connection headroom")
			}
			return e
		},
		RecordIntent: func(ctx context.Context, i ProjectIndexActionIntent) error {
			if err := lockedGate(ctx, i); err != nil {
				return err
			}
			if err := recordOwnedIndexIntent(ctx, state, project.ID(), epoch, claim, i); err != nil {
				return err
			}
			indexName = i.IndexName
			var err error
			fence, err = lockOwnedIndexEpoch(ctx, state, project.ID(), epoch, claim)
			return err
		},
	})
	if fence != nil {
		err = errors.Join(err, releaseOwnedIndexEpoch(state, fence))
	}
	report.BuildWallMS = elapsedMS(started)
	if err != nil {
		if action.WriteAttempted {
			result.Outcome, result.DiagnosticReason, result.Repair = "repair_interrupted", "an index write was attempted and requires reconciliation", &report
			return nil
		}
		return err
	}
	if action.AlreadyCovered {
		result.Outcome, result.DiagnosticReason = "no_change", "an equivalent index appeared before the action"
		return nil
	}
	report.IndexValid, report.IndexBytes = true, action.IndexBytes
	started = time.Now()
	after, err := f.measure(ctx, owner, project, catalog.Keys)
	report.VerifyWallMS = elapsedMS(started)
	if err != nil {
		result.Outcome, result.DiagnosticReason, result.Repair = "repair_interrupted", "the new index needs verification", &report
		return nil
	}
	report.After = fixtureTiming(after)
	report.Outcome, report.Reason = "inconclusive", "the isolated input did not demonstrate a useful improvement"
	var observedIndex uint32
	err = owner.QueryRow(ctx, "SELECT COALESCE(to_regclass($1)::oid,0)", qualified(f.Target.Schema, indexName)).Scan(&observedIndex)
	if err != nil {
		return err
	}
	if after.Node.Rows == before.Node.Rows && after.Node.Index == indexName && observedIndex == action.IndexOID &&
		after.Node.Read+after.Node.Hit < before.Node.Read+before.Node.Hit &&
		after.ExecutionMS > 0 && after.ExecutionMS < before.ExecutionMS*0.8 {
		report.Outcome, report.Reason = "improved_tested_inputs", "the measured lookup improved after the compound index"
	}
	if err := verify(ctx); err != nil {
		return err
	}
	result.Outcome, result.DiagnosticReason, result.Repair = report.Outcome, report.Reason, &report
	return nil
}

// A committed intent exists before this lock is acquired. Holding the same
// project row used by pause and disconnect fences DDL until the action returns.
func lockOwnedIndexEpoch(ctx context.Context, state *pgx.Conn, project string, epoch int64, claim investigationClaim) (pgx.Tx, error) {
	tx, err := state.Begin(ctx)
	if err != nil {
		return nil, err
	}
	var id string
	err = tx.QueryRow(ctx, `SELECT p.project_id FROM pooltune_worker.projects p
		JOIN pooltune_worker.investigations j USING(project_id)
		WHERE p.project_id=$1 AND p.monitor_epoch=$2 AND j.id=$3 AND j.attempts=$4
		AND j.finished_at IS NULL AND j.lease_until>clock_timestamp() FOR UPDATE OF p`,
		project, epoch, claim.ID, claim.Attempt).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = errClaimLost
	}
	if err != nil {
		return nil, errors.Join(err, releaseOwnedIndexEpoch(state, tx))
	}
	return tx, nil
}

func releaseOwnedIndexEpoch(state *pgx.Conn, tx pgx.Tx) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := tx.Rollback(ctx); err != nil {
		labdb.Close(state)
		return err
	}
	return nil
}

func fixtureTiming(plan explain) TrialSample {
	return TrialSample{ReadMS: plan.ExecutionMS, Reads: 1, Blocks: float64(plan.Node.Hit + plan.Node.Read), Plan: plan.Node.Type,
		PlanReturned: plan.Node.Rows, PlanExamined: plan.Node.Rows + plan.Node.Filtered}
}

func (f *OwnedProjectIndex) measure(ctx context.Context, owner *pgx.Conn, project supabaseproject.Project, keys []ProjectIndexKey) (plan explain, err error) {
	err = pgx.BeginTxFunc(ctx, owner, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout='750ms'; SET LOCAL search_path=pg_catalog; SET LOCAL lock_timeout='200ms'; SET LOCAL max_parallel_workers_per_gather=0; SET LOCAL enable_bitmapscan=off"); err != nil {
			return err
		}
		relation := qualified(f.Target.Schema, f.Target.Relation)
		if _, err := tx.Exec(ctx, "LOCK TABLE ONLY "+relation+" IN ACCESS SHARE MODE NOWAIT"); err != nil {
			return err
		}
		var owned bool
		err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
			WHERE c.oid=$1 AND c.relname=$2 AND n.nspname=$3 AND c.relnamespace IS NOT NULL
			AND c.relowner=current_user::regrole::oid AND n.nspowner=c.relowner AND c.relowner=$4
			AND obj_description(n.oid,'pg_namespace')=$5 AND c.relkind='r' AND NOT c.relispartition
			AND NOT (c.relrowsecurity OR c.relhasrules) AND pg_total_relation_size(c.oid)<=25165824
			AND NOT EXISTS(SELECT FROM pg_inherits WHERE inhrelid=c.oid OR inhparent=c.oid)
			AND NOT EXISTS(SELECT FROM pg_index i WHERE i.indrelid=c.oid AND (i.indexprs IS NOT NULL OR i.indpred IS NOT NULL))
			AND (SELECT count(*) FROM pg_attribute a WHERE a.attrelid=c.oid AND a.attnum=ANY($6::smallint[])
				AND NOT a.attisdropped AND a.atttypid='int4'::regtype)=2)`,
			f.Target.TableOID, f.Target.Relation, f.Target.Schema, f.OwnerRoleOID,
			"Pooltune hosted index fixture "+project.ID(), f.Columns).Scan(&owned)
		if err != nil || !owned {
			return errors.Join(ErrProjectIndexTargetChanged, err)
		}
		first, second := pgx.Identifier{keys[0].Name}.Sanitize(), pgx.Identifier{keys[1].Name}.Sanitize()
		statement := "SELECT " + first + " FROM ONLY " + relation + " WHERE " + first + " = $1 AND " + second + " = $2"
		plan, err = readLookupPlan(ctx, tx, "EXPLAIN (ANALYZE,BUFFERS,VERBOSE,FORMAT JSON,TIMING OFF) "+statement, f.Inputs[0], f.Inputs[1])
		if err == nil && (plan.QueryID != f.QueryID || plan.Node.Schema != f.Target.Schema ||
			plan.Node.Relation != f.Target.Relation || len(plan.Node.Plans) != 0 || plan.Node.Loops != 1) {
			return ErrProjectIndexTargetChanged
		}
		return err
	})
	return plan, err
}

func (f *OwnedProjectIndex) recheck(ctx context.Context, state, monitor *pgx.Conn, project supabaseproject.Project,
	epoch int64, claim investigationClaim, windows *ObservedLookupLatency) error {
	var started, reset, entry time.Time
	var objective float64
	var calls int64
	err := monitor.QueryRow(ctx, "SELECT boot,reset_at,entry_since,mean_execution_ms,calls FROM "+
		qualified(project.Role(), "snapshot")+"() WHERE database_oid=$1 AND role_oid=$2 AND query_id=$3",
		claim.Database, claim.Role, claim.Query).Scan(&started, &reset, &entry, &objective, &calls)
	if err != nil {
		return err
	}
	if len(claim.Generation) != 3 || !started.Equal(claim.Generation[0]) ||
		!reset.Equal(claim.Generation[1]) || !entry.Equal(claim.Generation[2]) || objective != windows.ObjectiveMS || calls < 1 {
		return errLookupScope
	}
	var active bool
	err = state.QueryRow(ctx, `SELECT EXISTS (SELECT FROM pooltune_worker.projects p JOIN pooltune_worker.investigations j USING(project_id)
		WHERE p.project_id=$1 AND p.monitor_epoch=$2 AND j.id=$3 AND j.attempts=$4 AND j.finished_at IS NULL AND j.lease_until>clock_timestamp())`,
		project.ID(), epoch, claim.ID, claim.Attempt).Scan(&active)
	if err == nil && !active {
		return errClaimLost
	}
	return err
}

func recordOwnedIndexIntent(ctx context.Context, state *pgx.Conn, project string, epoch int64, claim investigationClaim, i ProjectIndexActionIntent) error {
	payload, err := json.Marshal(i)
	if err != nil {
		return err
	}
	tag, err := state.Exec(ctx, `WITH active AS MATERIALIZED (
			SELECT project_id FROM pooltune_worker.projects WHERE project_id=$1 AND monitor_epoch=$2 FOR UPDATE
		)
		INSERT INTO pooltune_worker.owned_index_actions(job_id,project_id,intent)
		SELECT j.id,j.project_id,$5::jsonb FROM pooltune_worker.investigations j JOIN active USING(project_id)
		WHERE j.id=$3 AND j.attempts=$4 AND j.finished_at IS NULL AND j.lease_until>clock_timestamp()
		ON CONFLICT (job_id) DO NOTHING`, project, epoch, claim.ID, claim.Attempt, string(payload))
	if err == nil && tag.RowsAffected() != 1 {
		return errClaimLost
	}
	return err
}
