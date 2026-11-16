package hostedquery

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
)

type managedStatisticsObservation struct {
	samples  []ManagedIndexMeasurement
	prior    projectStatisticsState
	next     int32
	evidence IndexEvidence
}

type ManagedStatisticsTrialReport struct {
	Deadline              time.Time                  `json:"deadline,omitempty"`
	StatisticsRegenerated bool                       `json:"statistics_regenerated,omitempty"`
	OwnerChanged          bool                       `json:"owner_changed,omitempty"`
	VerificationScope     string                     `json:"verification_scope"`
	SampledOutcome        string                     `json:"sampled_outcome,omitempty"`
	Measurement           string                     `json:"measurement"`
	Variant               string                     `json:"variant"`
	Before                []ManagedIndexMeasurement  `json:"before"`
	After                 []ManagedIndexMeasurement  `json:"after,omitempty"`
	BeforeTraffic         *ManagedMaintenanceTraffic `json:"before_traffic,omitempty"`
	AfterTraffic          *ManagedMaintenanceTraffic `json:"after_traffic,omitempty"`
	ColumnTarget          *ProjectStatisticsResult   `json:"column_target,omitempty"`
	Applied               bool                       `json:"applied"`
	Retained              bool                       `json:"retained"`
	Restored              bool                       `json:"restored"`
	Outcome               string                     `json:"outcome"`
	Reason                string                     `json:"reason"`
}

// runStatisticsTrial leaves verification and the catalog change in one native
// transaction. A failed or inconclusive trial restores the exact previous sample.
func (m *ManagedProjectIndex) runStatisticsTrial(ctx context.Context, state, monitor *pgx.Conn, project supabaseproject.Project, epoch int64, claim investigationClaim, windows *ObservedLookupLatency, result *DispatchResult, observed supabaseproject.DiscoveredIndexEvidence, owner *pgx.Conn, first ManagedIndexMeasurement) (bool, error) {
	if !slices.Contains(m.Capabilities, "statistics") || !first.Catalog.Covering {
		return false, nil
	}
	runID := fmt.Sprintf("managed-statistics-%d-%d", claim.ID, claim.Attempt)
	var before managedStatisticsObservation
	err := pgx.BeginFunc(ctx, owner, func(tx pgx.Tx) error {
		var err error
		before, err = readManagedStatistics(ctx, tx, observed, first, nil, runID)
		return err
	})
	if errors.Is(err, errOwnedStatisticsEvidence) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	verify := func(ctx context.Context) error {
		if err := checkOwnedIndexClaim(ctx, state, project, epoch, claim); err != nil {
			return err
		}
		fresh, err := project.ReadDiscoveredIndexEvidence(ctx, monitor, claim.Role, claim.Query)
		if err != nil {
			return err
		}
		if len(claim.Generation) != 3 || fresh.DatabaseOID != claim.Database || fresh.RoleOID != claim.Role || fresh.QueryID != claim.Query || fresh.ObjectiveMS != windows.ObjectiveMS || !fresh.Boot.Equal(claim.Generation[0]) || !fresh.ResetAt.Equal(claim.Generation[1]) || !fresh.EntrySince.Equal(claim.Generation[2]) || fresh.TableOID != observed.TableOID || fresh.Schema != observed.Schema || fresh.Relation != observed.Relation || fresh.Only != observed.Only || !slices.Equal(fresh.Columns, observed.Columns) {
			return errLookupScope
		}
		return project.VerifyManagedRepair(ctx, owner)
	}
	if err := verify(ctx); err != nil {
		return true, err
	}
	result.Evidence, result.ThreadID, result.ObservedLatency = &before.evidence, runID, windows
	controls, err := observeManagedControls(ctx, owner, claim.Database, claim.Role, claim.Query, nil)
	if err != nil {
		result.DiagnosticReason = "protected_normal_traffic_unavailable"
		return true, nil
	}
	if err := m.AdmitModel(ctx); err != nil {
		return true, err
	}
	proposal, err := m.Investigate(ctx, before.evidence)
	if err != nil {
		return true, err
	}
	result.Proposal = &proposal
	if proposal.Outcome == "no_change" && proposal.RunID == runID && proposal.TableOID == observed.TableOID && proposal.Column == observed.Columns[0] && len(proposal.Columns) == 0 && len(proposal.Reason) > 0 && len(proposal.Reason) <= 1000 {
		result.Outcome, result.DiagnosticReason = "no_change", proposal.Reason
		return true, nil
	}
	if proposal.Repair != "statistics" || len(proposal.Columns) != 0 || ValidateDiagnosticProposal(before.evidence, proposal, time.Now()) != nil {
		result.Outcome, result.DiagnosticReason = "no_change", "the proposed action does not match the supported statistics evidence"
		return true, nil
	}
	report := ManagedStatisticsTrialReport{Measurement: "managed_sampled_inputs_and_normal_traffic_windows", VerificationScope: "uncommitted_candidate_inputs_and_concurrent_load", Variant: "joint", Before: before.samples, BeforeTraffic: maintenanceTraffic(controls), Outcome: "inconclusive", Reason: "the statistics trial did not demonstrate benefit"}
	var fence pgx.Tx
	record := func(ctx context.Context, intent any) error {
		if err := verify(ctx); err != nil {
			return err
		}
		if err := recordOwnedProjectAction(ctx, state, project.ID(), epoch, claim, "statistics", intent); err != nil {
			return err
		}
		var err error
		fence, err = lockOwnedIndexEpoch(ctx, state, project.ID(), epoch, claim)
		return err
	}
	recheck := func(ctx context.Context, tx pgx.Tx) error {
		current, err := readManagedStatistics(ctx, tx, observed, first, managedStatisticsInputs(before.samples), runID)
		if err != nil {
			return err
		}
		if current.next != before.next || current.prior != before.prior || len(current.samples) != len(before.samples) {
			return errOwnedStatisticsEvidence
		}
		for i, s := range current.samples {
			b := before.samples[i]
			if s.EstimatedRows != b.EstimatedRows || s.Sample.PlanReturned != b.Sample.PlanReturned || s.Evidence.Node != b.Evidence.Node {
				return errOwnedStatisticsEvidence
			}
		}
		return verify(ctx)
	}
	trialVerify := func(ctx context.Context, tx pgx.Tx) (bool, error) {
		for _, b := range before.samples {
			a, err := MeasureManagedIndexLookupTx(ctx, tx, observed, first.Catalog, b.Inputs)
			if err != nil {
				return false, err
			}
			report.After = append(report.After, a)
		}
		afterControls, err := observeManagedControls(ctx, owner, claim.Database, claim.Role, claim.Query, controls)
		report.AfterTraffic = maintenanceTraffic(afterControls)
		if errors.Is(err, ErrManagedTrafficRegression) {
			report.Outcome, report.Reason = "regressed", "a protected normal-traffic query regressed during the trial"
			return false, nil
		}
		if err != nil {
			report.Reason = "comparable normal traffic was unavailable after the trial"
			return false, nil
		}
		report.Outcome, report.Reason, report.SampledOutcome = classifyManagedStatisticsTrial(before.samples, report.After, controls, afterControls)
		// Other application sessions still see the old planner statistics.
		// Their concurrent counters can detect load harm, but cannot qualify
		// retention. Preserve the original target and planner sample until the
		// protected-plan verification and recovery design is approved.
		return false, nil
	}
	if before.next > 0 {
		report.Variant = "column_target"
		req := ProjectStatisticsRequest{Target: first.Catalog.Target, RunID: runID, ObservedAt: before.evidence.At, Column: observed.Columns[0], NextTarget: before.next}
		guard := func(ctx context.Context, i ProjectStatisticsIntent) error {
			if i.Target != req.Target || i.RunID != runID || i.Column != req.Column || i.NextTarget != req.NextTarget || i.Prior != before.prior.setting || i.PriorEffective != before.prior.effective {
				return ErrProjectStatisticsTarget
			}
			return verify(ctx)
		}
		action, actionErr := applyProjectStatisticsTarget(ctx, m.Config, req, ProjectStatisticsGuards{CheckPermission: guard, CheckCapacity: func(ctx context.Context, _ ProjectStatisticsIntent) error {
			return checkOwnedRepairCapacity(ctx, owner, nil)
		}, RecordIntent: func(ctx context.Context, i ProjectStatisticsIntent) error {
			if err := guard(ctx, i); err != nil {
				return err
			}
			return record(ctx, i)
		}}, projectStatisticsTrial{recheck: func(ctx context.Context, tx pgx.Tx, _ ProjectStatisticsIntent) error { return recheck(ctx, tx) }, verify: func(ctx context.Context, tx pgx.Tx, _ ProjectStatisticsIntent) (bool, error) {
			return trialVerify(ctx, tx)
		}})
		report.ColumnTarget = &action
		report.Applied = action.WriteAttempted
		report.Retained = action.Confirmed && !action.Restored
		report.Restored = action.Restored
		err = actionErr
		if action.Restored && err != nil {
			report.Outcome, report.Reason = "inconclusive", "verification could not finish; PostgreSQL restored the original target and sample"
			err = nil
		}
	} else {
		report.Applied, report.Retained, err = applyManagedJointStatistics(ctx, m.Config, observed, first.Catalog, before.evidence.At, runID, recheck, trialVerify, verify, func(ctx context.Context) error { return checkOwnedRepairCapacity(ctx, owner, nil) }, record)
		report.Restored = report.Applied && !report.Retained && err == nil
	}
	if fence != nil {
		err = errors.Join(err, releaseOwnedIndexEpoch(state, fence))
	}
	if err != nil {
		return true, err
	}
	if report.Restored {
		report.Reason += "; PostgreSQL restored the original statistics"
	}
	if err := verify(ctx); err != nil {
		return true, err
	}
	result.ownedOperation = "statistics"
	result.Outcome, result.DiagnosticReason = report.Outcome, report.Reason
	result.ManagedStatistics = &ManagedStatisticsReport{ManagedStatisticsTrialReport: report}
	return true, nil
}

func managedStatisticsInputs(samples []ManagedIndexMeasurement) [][]int32 {
	out := make([][]int32, len(samples))
	for i, s := range samples {
		out[i] = s.Inputs
	}
	return out
}

func readManagedStatistics(ctx context.Context, tx pgx.Tx, observed supabaseproject.DiscoveredIndexEvidence, first ManagedIndexMeasurement, inputs [][]int32, runID string) (out managedStatisticsObservation, err error) {
	if len(observed.Columns) < 1 || len(observed.Columns) > 2 || len(first.Catalog.Keys) != len(observed.Columns) || !first.Catalog.Covering {
		return out, errOwnedStatisticsEvidence
	}
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='750ms'; SET LOCAL lock_timeout='100ms'; SET LOCAL search_path=pg_catalog`); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, "LOCK TABLE ONLY "+qualified(observed.Schema, observed.Relation)+" IN ACCESS SHARE MODE NOWAIT"); err != nil {
		return out, err
	}
	for _, key := range first.Catalog.Keys {
		if _, err := readProjectStatisticsState(ctx, tx, first.Catalog.Target, key, false); err != nil {
			return out, err
		}
	}
	var names []string
	var covering bool
	if err := tx.QueryRow(ctx, projectIndexCatalogSQL, observed.DatabaseOID, observed.TableOID, observed.Schema, observed.Relation, observed.Columns).Scan(&names, &covering); err != nil {
		return out, err
	}
	if !covering || len(names) != len(first.Catalog.Keys) {
		return out, errOwnedStatisticsEvidence
	}
	for i, key := range first.Catalog.Keys {
		if key.Name != names[i] {
			return out, errOwnedStatisticsEvidence
		}
	}
	if inputs == nil {
		inputs = [][]int32{first.Inputs}
		if len(observed.Columns) == 2 {
			second, err := managedStatisticsSecondInput(ctx, tx, observed, first)
			if err != nil {
				return out, err
			}
			inputs = append(inputs, second)
		}
	}
	for _, input := range inputs {
		sample, err := MeasureManagedIndexLookupTx(ctx, tx, observed, first.Catalog, input)
		if err != nil {
			return out, err
		}
		out.samples = append(out.samples, sample)
	}
	if len(out.samples) != len(observed.Columns) {
		return out, errOwnedStatisticsEvidence
	}
	e := IndexEvidence{RunID: runID, At: time.Now().UTC(), Decision: Investigate, TableOID: observed.TableOID, Column: observed.Columns[0], Existing: true, Plan: out.samples[0].Evidence, ExecutionMS: out.samples[0].Sample.ReadMS, Label: first.Catalog.Keys[0].Name, Statistics: &StatisticsEvidence{}}
	for _, s := range out.samples {
		e.Statistics.Cases = append(e.Statistics.Cases, AnalyzeEvidence{EstimatedRows: s.EstimatedRows, ActualRows: s.Sample.PlanReturned})
	}
	if len(observed.Columns) == 1 {
		key := first.Catalog.Keys[0]
		out.prior, err = readProjectStatisticsState(ctx, tx, first.Catalog.Target, key, false)
		if err != nil {
			return out, err
		}
		out.next = min(out.prior.effective*2, out.prior.effective+100, int32(projectStatisticsMaxTarget))
		if !boundedProjectStatisticsTarget(out.prior.effective, out.next) {
			return out, errOwnedStatisticsEvidence
		}
		frequency, err := ownedMarginalFrequency(ctx, tx, first.Catalog.Target, key.Name, inputs[0][0])
		if err != nil {
			return out, err
		}
		if frequency != nil {
			return out, errOwnedStatisticsEvidence
		}
		var full bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pg_stat_all_tables WHERE relid=$1 AND last_analyze>statement_timestamp()-interval '60 minutes' AND n_mod_since_analyze=0)
   AND EXISTS(SELECT FROM pg_stats WHERE schemaname=$2 AND tablename=$3 AND attname=$4 AND NOT inherited AND cardinality(most_common_freqs)=$5)`, observed.TableOID, observed.Schema, observed.Relation, key.Name, out.prior.effective).Scan(&full); err != nil {
			return out, err
		}
		if !full {
			return out, errOwnedStatisticsEvidence
		}
		e.Statistics.ColumnTarget = &StatisticsColumnTargetEvidence{CurrentTarget: out.prior.effective, NextTarget: out.next}
	} else {
		request := OwnedStatisticsRepairRequest{Target: first.Catalog.Target, Inputs: inputs}
		names := []string{first.Catalog.Keys[0].Name, first.Catalog.Keys[1].Name}
		correlated, err := ownedJointCorrelation(ctx, tx, request, names)
		if err != nil {
			return out, err
		}
		if !correlated {
			return out, errOwnedStatisticsEvidence
		}
		for _, s := range out.samples {
			if s.Evidence.Node != "Seq Scan" {
				return out, errOwnedStatisticsEvidence
			}
		}
		e.Statistics.Correlated = true
	}
	if !e.Statistics.SupportsRepair(true) {
		return out, errOwnedStatisticsEvidence
	}
	out.evidence = e
	return out, nil
}

func managedStatisticsSecondInput(ctx context.Context, outer pgx.Tx, observed supabaseproject.DiscoveredIndexEvidence, first ManagedIndexMeasurement) (input []int32, err error) {
	tx, err := outer.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err = errors.Join(err, tx.Rollback(cleanup))
	}()
	var role string
	if err := tx.QueryRow(ctx, "SELECT rolname FROM pg_roles WHERE oid=$1", observed.RoleOID).Scan(&role); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{role}.Sanitize()); err != nil {
		return nil, err
	}
	a, b := pgx.Identifier{first.Catalog.Keys[0].Name}.Sanitize(), pgx.Identifier{first.Catalog.Keys[1].Name}.Sanitize()
	input = make([]int32, 2)
	err = tx.QueryRow(ctx, "SELECT "+a+","+b+" FROM ONLY "+qualified(observed.Schema, observed.Relation)+" WHERE "+a+"<>$1 AND "+b+"<>$2 LIMIT 1", first.Inputs[0], first.Inputs[1]).Scan(&input[0], &input[1])
	if errors.Is(err, pgx.ErrNoRows) {
		err = errOwnedStatisticsEvidence
	}
	return input, err
}

func applyManagedJointStatistics(ctx context.Context, config *pgx.ConnConfig, observed supabaseproject.DiscoveredIndexEvidence, catalog ProjectIndexCatalog, at time.Time, runID string, recheck func(context.Context, pgx.Tx) error, verify func(context.Context, pgx.Tx) (bool, error), permission, capacity func(context.Context) error, record func(context.Context, any) error) (applied, retained bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	db, err := pgx.ConnectConfig(ctx, config.Copy())
	if err != nil {
		return false, false, err
	}
	defer closeVacuumConnection(db)
	tx, err := db.Begin(ctx)
	if err != nil {
		return false, false, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	if _, err := tx.Exec(ctx, `SET LOCAL search_path=pg_catalog; SET LOCAL statement_timeout='2s'; SET LOCAL lock_timeout='150ms'; SET LOCAL transaction_timeout='12s'; SET LOCAL idle_in_transaction_session_timeout='2s'; SET LOCAL maintenance_work_mem='16MB'; SET LOCAL max_parallel_maintenance_workers=0`); err != nil {
		return false, false, err
	}
	relation := qualified(observed.Schema, observed.Relation)
	if _, err := tx.Exec(ctx, "LOCK TABLE ONLY "+relation+" IN SHARE UPDATE EXCLUSIVE MODE NOWAIT"); err != nil {
		return false, false, err
	}
	var locked bool
	if err := tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1,$2)", projectIndexLockNamespace, int32(observed.TableOID)).Scan(&locked); err != nil {
		return false, false, err
	}
	if !locked {
		return false, false, errors.New("another Pooltune action is active for this relation")
	}
	var bytes int64
	for _, key := range catalog.Keys {
		s, err := readProjectStatisticsState(ctx, tx, catalog.Target, key, true)
		if err != nil {
			return false, false, err
		}
		bytes = s.relationBytes
	}
	if err := recheck(ctx, tx); err != nil {
		return false, false, err
	}
	if at.IsZero() || time.Since(at) > 30*time.Second || at.After(time.Now()) {
		return false, false, errors.New("statistics evidence expired")
	}
	name := managedJointStatisticsName(observed.TableOID, observed.Columns)
	var absent bool
	if err := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT FROM pg_statistic_ext WHERE stxnamespace=$1::regnamespace AND stxname=$2)`, observed.Schema, name).Scan(&absent); err != nil {
		return false, false, err
	}
	if !absent {
		return false, false, ErrProjectStatisticsTarget
	}
	for _, gate := range []func(context.Context) error{permission, capacity, func(ctx context.Context) error {
		return record(ctx, OwnedExtendedStatisticsIntent{RunID: runID, Target: catalog.Target, ColumnNumbers: slices.Clone(observed.Columns), ObjectName: name, RelationBytes: bytes})
	}, permission} {
		if err := gate(ctx); err != nil {
			return false, false, err
		}
	}
	columns := pgx.Identifier{catalog.Keys[0].Name}.Sanitize() + "," + pgx.Identifier{catalog.Keys[1].Name}.Sanitize()
	if _, err := tx.Exec(ctx, "CREATE STATISTICS "+qualified(observed.Schema, name)+" (mcv) ON "+columns+" FROM "+relation); err != nil {
		return true, false, err
	}
	applied = true
	if _, err := tx.Exec(ctx, "ANALYZE (BUFFER_USAGE_LIMIT '256kB') "+relation+" ("+columns+")"); err != nil {
		return true, false, err
	}
	var size int64
	if err := tx.QueryRow(ctx, `SELECT (pg_column_size(most_common_vals)+pg_column_size(most_common_freqs)+pg_column_size(most_common_base_freqs))::bigint FROM pg_stats_ext WHERE statistics_schemaname=$1 AND statistics_name=$2 AND schemaname=$1 AND tablename=$3 AND NOT inherited`, observed.Schema, name, observed.Relation).Scan(&size); err != nil {
		return true, false, err
	}
	if size <= 0 || size > maxStatisticsBytes {
		return true, false, errors.New("joint statistics data exceeded its budget")
	}
	retain, err := verify(ctx, tx)
	if err != nil || !retain {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		rollback := tx.Rollback(cleanup)
		if rollback != nil {
			return true, false, errors.Join(err, rollback)
		}
		// A completed rollback proves restoration even when measurement failed.
		return true, false, nil
	}
	if err := permission(ctx); err != nil {
		return true, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return true, false, err
	}
	return true, true, nil
}
