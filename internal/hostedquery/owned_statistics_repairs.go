package hostedquery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var ownedStatisticsSchema = regexp.MustCompile(`^pt_hosted_st_[0-9a-f]{16}$`)
var errOwnedStatisticsEvidence = errors.New("the owned statistics workload could not be verified")
var errOwnedStatisticsRollback = errors.New("the statistics trial did not meet retention thresholds")

const ownedExtendedStatisticsName = "pooltune_owned_lookup_mcv"

type OwnedStatisticsRepairRequest struct {
	Operation, ProjectID, RunID string
	Target                      ProjectIndexTarget
	OwnerRoleOID, GuardTableOID uint32
	QueryID                     int64
	Columns                     []int16
	Inputs                      [][]int32
	NextTarget                  int32
}

type OwnedStatisticsRepairGuards struct {
	CheckPermission func(context.Context) error
	CheckCapacity   func(context.Context) error
	RecordIntent    func(context.Context, any) error
}

// OwnedExtendedStatisticsIntent binds the sole joint statistics action and its
// exact ordered columns to the enrolled relation before any catalog mutation.
type OwnedExtendedStatisticsIntent struct {
	RunID         string
	Target        ProjectIndexTarget
	ColumnNumbers []int16
	ObjectName    string
	RelationBytes int64
}

// RunOwnedStatisticsRepair measures a fixed, already discovered workload on an
// owned staging fixture. It accepts the target and predicate keys from trusted
// configuration, never from model text. The caller must bind the query to a fresh
// investigation claim and implement the live permit and durable action guards.
func RunOwnedStatisticsRepair(ctx context.Context, writer *pgx.ConnConfig, request OwnedStatisticsRepairRequest,
	investigate IndexInvestigator, guards OwnedStatisticsRepairGuards) (DispatchResult, error) {
	deferred := DispatchResult{Outcome: "insufficient_evidence", DiagnosticReason: errOwnedStatisticsEvidence.Error()}
	if !validOwnedStatisticsRequest(writer, request) || investigate == nil ||
		guards.CheckPermission == nil || guards.CheckCapacity == nil || guards.RecordIntent == nil {
		return deferred, nil
	}
	if err := guards.CheckPermission(ctx); err != nil {
		return deferred, err
	}
	if err := guards.CheckCapacity(ctx); err != nil {
		return deferred, err
	}

	start := time.Now()
	db, err := pgx.ConnectConfig(ctx, writer.Copy())
	logOwnedStatisticsPhase(request, "connect_sampler", start, err)
	if err != nil {
		return deferred, err
	}
	defer closeVacuumConnection(db)

	if request.Operation == "analyze" {
		result, err := runOwnedAnalyze(ctx, db, writer, request, investigate, guards)
		if errors.Is(err, errOwnedStatisticsEvidence) {
			return deferred, nil
		}
		return result, err
	}
	var result DispatchResult
	if request.NextTarget > 0 {
		result, err = runOwnedStatisticsTarget(ctx, db, writer, request, investigate, guards)
	} else {
		result, err = runOwnedExtendedStatistics(ctx, db, request, investigate, guards)
	}
	if errors.Is(err, errOwnedStatisticsEvidence) {
		return deferred, nil
	}
	return result, err
}

func validOwnedStatisticsRequest(writer *pgx.ConnConfig, request OwnedStatisticsRepairRequest) bool {
	if writer == nil || writer.User != request.Target.Schema || !ownedStatisticsSchema.MatchString(request.Target.Schema) ||
		request.ProjectID != "supabase/"+OwnedIndexRookeryRef+"/"+strings.TrimPrefix(request.Target.Schema, "pt_hosted_st_") ||
		request.Target.Relation != "workload" || request.Target.TableOID == 0 || request.Target.DatabaseOID == 0 ||
		request.GuardTableOID == 0 || request.GuardTableOID == request.Target.TableOID || request.OwnerRoleOID == 0 ||
		request.QueryID == 0 || request.RunID == "" || len(request.RunID) > 64 {
		return false
	}
	switch request.Operation {
	case "analyze":
		return request.NextTarget == 0 && validOwnedStatisticsScalar(request)
	case "statistics":
		if request.NextTarget > 0 {
			return request.NextTarget <= projectStatisticsMaxTarget && validOwnedStatisticsScalar(request)
		}
		return request.NextTarget == 0 && len(request.Columns) == 2 && ValidIndexColumns(request.Columns[0], request.Columns) &&
			len(request.Inputs) == 2 && len(request.Inputs[0]) == 2 && len(request.Inputs[1]) == 2 &&
			request.Inputs[0][0] != request.Inputs[1][0] && request.Inputs[0][1] != request.Inputs[1][1]
	default:
		return false
	}
}

func validOwnedStatisticsScalar(request OwnedStatisticsRepairRequest) bool {
	return len(request.Columns) == 1 && request.Columns[0] > 0 &&
		len(request.Inputs) == 1 && len(request.Inputs[0]) == 1
}

func runOwnedAnalyze(ctx context.Context, db *pgx.Conn, writer *pgx.ConnConfig, request OwnedStatisticsRepairRequest,
	investigate IndexInvestigator, guards OwnedStatisticsRepairGuards) (DispatchResult, error) {
	req := ProjectAnalyzeRequest{
		Target: request.Target, RunID: request.RunID, DatabaseRoleOID: request.OwnerRoleOID,
		QueryID: request.QueryID, Column: request.Columns[0], Decision: Investigate,
	}
	sampleNumber := 0
	report, err := runProjectAnalyze(ctx, writer, req,
		func(ctx context.Context, exact ProjectAnalyzeRequest) (observation ProjectAnalyzeObservation, err error) {
			sampleNumber++
			start := time.Now()
			samples, info, err := readOwnedStatisticsSamples(ctx, db, request)
			logOwnedStatisticsPhase(request, "matched_samples", start, err, "sample_number", sampleNumber)
			if err != nil {
				return observation, err
			}
			return ProjectAnalyzeObservation{
				Request: exact, At: time.Now().UTC(), Sample: samples[0],
				CoveringIndex: info.covering, SampledValueFraction: info.selectedFraction,
			}, nil
		}, func(ctx context.Context, evidence IndexEvidence) (IndexProposal, error) {
			start := time.Now()
			proposal, err := investigate(ctx, evidence)
			logOwnedStatisticsPhase(request, "investigation", start, err)
			return proposal, err
		}, ProjectMaintenanceGuards{
			CheckPermission: func(ctx context.Context, _ ProjectMaintenanceIntent) error { return guards.CheckPermission(ctx) },
			CheckCapacity:   func(ctx context.Context, _ ProjectMaintenanceIntent) error { return guards.CheckCapacity(ctx) },
			RecordIntent: func(ctx context.Context, i ProjectMaintenanceIntent) error {
				start := time.Now()
				err := guards.RecordIntent(ctx, i)
				logOwnedStatisticsPhase(request, "record_intent", start, err)
				return err
			},
		}, func(start time.Time, observed ProjectMaintenanceResult, err error) {
			readBack := !observed.After.At.IsZero()
			logOwnedStatisticsPhase(request, "postgres_action", start, err,
				"write_attempted", observed.WriteAttempted, "confirmed", observed.Confirmed,
				"counter_readback", readBack, "counter_delta", observed.After.Count-observed.Before.Count,
				"last_run_in_window", readBack && !observed.After.LastRun.Before(observed.Before.At) && !observed.After.LastRun.After(observed.After.At))
		})
	return DispatchResult{
		Outcome: report.Outcome, DiagnosticReason: report.Reason, ThreadID: request.RunID,
		Analyze: &report, Evidence: &report.Evidence, Proposal: &report.Proposal, GraphMS: report.GraphMS,
	}, err
}

func logOwnedStatisticsPhase(request OwnedStatisticsRepairRequest, phase string, start time.Time, err error, fields ...any) {
	category := "ok"
	if err != nil {
		category = ownedRepairErrorCategory(err)
		if errors.Is(err, errOwnedStatisticsEvidence) {
			category = "evidence_changed"
		}
	}
	fields = append(fields, "operation", request.Operation, "run_id", request.RunID,
		"phase", phase, "elapsed_ms", elapsedMS(start), "category", category)
	slog.Info("owned statistics repair phase", fields...)
}

func runOwnedExtendedStatistics(ctx context.Context, db *pgx.Conn, request OwnedStatisticsRepairRequest,
	investigate IndexInvestigator, guards OwnedStatisticsRepairGuards) (DispatchResult, error) {
	var report StatisticsReport
	result := DispatchResult{Statistics: &report, ThreadID: request.RunID, Evidence: &report.Evidence, Proposal: &report.Proposal}
	complete := func(outcome, reason string) DispatchResult {
		report.Outcome, report.Reason = outcome, reason
		result.Outcome, result.DiagnosticReason, result.GraphMS = outcome, reason, report.GraphMS
		return result
	}
	start := time.Now()
	samples, info, err := readOwnedStatisticsSamples(ctx, db, request)
	report.BaselineMS = elapsedMS(start)
	logOwnedStatisticsPhase(request, "matched_samples", start, err, "sample_number", 1)
	if err != nil {
		return result, err
	}
	if !info.correlated || !info.covering || !ownedStatisticsEstimateMiss(samples) {
		return complete("insufficient_evidence", "fresh measurements did not establish a joint statistics problem"), nil
	}

	report.Before = StatisticsSample{Samples: samples, Correlated: true}
	evidence := IndexEvidence{
		RunID: request.RunID, At: time.Now().UTC(), Decision: Investigate, TableOID: request.Target.TableOID,
		Column: request.Columns[0], Existing: true, Label: info.names[0],
		ExecutionMS: samples[0].ReadMS,
		Plan: PlanEvidence{Node: samples[0].Plan, ExaminedRows: samples[0].PlanExamined,
			ReturnedRows: samples[0].PlanReturned, SharedBlocks: int64(samples[0].Blocks)},
		// The diagnostic contract's Columns field is reserved for index
		// repairs. This operation binds both keys independently of the model.
		Statistics: &StatisticsEvidence{Correlated: true, Cases: []AnalyzeEvidence{
			{EstimatedRows: samples[0].EstimatedRows, ActualRows: samples[0].PlanReturned},
			{EstimatedRows: samples[1].EstimatedRows, ActualRows: samples[1].PlanReturned},
		}},
	}
	report.Evidence = evidence
	start = time.Now()
	proposal, err := investigate(ctx, evidence)
	report.GraphMS = elapsedMS(start)
	logOwnedStatisticsPhase(request, "investigation", start, err)
	if err != nil {
		return result, err
	}
	if proposal.RunID != evidence.RunID || proposal.TableOID != evidence.TableOID ||
		proposal.Column != evidence.Column || len(proposal.Columns) != 0 || len(proposal.Reason) == 0 || len(proposal.Reason) > 1000 ||
		(proposal.Repair != "" && proposal.Repair != "statistics") {
		return complete("no_change", "the proposed action does not match the authorized statistics fixture"), nil
	}
	report.Proposal = proposal
	if proposal.Outcome == "no_change" {
		return complete("no_change", proposal.Reason), nil
	}
	if proposal.Repair != "statistics" || ValidateDiagnosticProposal(evidence, proposal, time.Now()) != nil {
		return complete("no_change", "the investigation did not authorize joint statistics"), nil
	}

	start = time.Now()
	outcome, reason, err := applyOwnedExtendedStatistics(ctx, db, request, info, guards, &report)
	report.ApplyMS = elapsedMS(start)
	logOwnedStatisticsPhase(request, "postgres_action", start, err, "outcome", outcome,
		"create_ms", report.After.CreateMS, "analyze_ms", report.After.AnalyzeMS, "verify_ms", report.After.VerifyMS)
	if err != nil {
		return result, err
	}
	return complete(outcome, reason), nil
}

func ownedStatisticsEstimateMiss(samples []AnalyzeSample) bool {
	return len(samples) == 2 && samples[0].Plan == "Seq Scan" && samples[1].Plan == "Seq Scan" &&
		samples[0].EstimateError >= 10 && samples[1].EstimateError >= 10
}

func applyOwnedExtendedStatistics(ctx context.Context, db *pgx.Conn, request OwnedStatisticsRepairRequest, info ownedStatisticsCatalog,
	guards OwnedStatisticsRepairGuards, report *StatisticsReport) (outcome, reason string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		if err := lockOwnedStatisticsRelations(ctx, tx, request, true); err != nil {
			return err
		}
		var acquired bool
		if err := tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1,$2)", projectIndexLockNamespace, int32(request.Target.TableOID)).Scan(&acquired); err != nil {
			return err
		}
		if !acquired {
			return errors.New("another Pooltune action is active for this relation")
		}
		current, err := ownedStatisticsCatalogRead(ctx, tx, request)
		if err != nil || !current.covering || !current.correlated || !slices.Equal(current.names, info.names) {
			return errors.Join(errOwnedStatisticsEvidence, err)
		}
		if err := compareOwnedStatisticsEstimates(ctx, tx, request, current, report.Before); err != nil {
			return err
		}
		intent := OwnedExtendedStatisticsIntent{request.RunID, request.Target, slices.Clone(request.Columns), ownedExtendedStatisticsName, current.bytes}
		for _, gate := range []func(context.Context) error{guards.CheckPermission, guards.CheckCapacity,
			func(ctx context.Context) error { return guards.RecordIntent(ctx, intent) }, guards.CheckPermission} {
			if err := gate(ctx); err != nil {
				return err
			}
		}
		table := qualified(request.Target.Schema, request.Target.Relation)
		object := qualified(request.Target.Schema, ownedExtendedStatisticsName)
		columns := pgx.Identifier{current.names[0]}.Sanitize() + "," + pgx.Identifier{current.names[1]}.Sanitize()
		start := time.Now()
		if _, err := tx.Exec(ctx, "CREATE STATISTICS "+object+" (mcv) ON "+columns+" FROM "+table); err != nil {
			return err
		}
		report.After.CreateMS = elapsedMS(start)
		start = time.Now()
		if _, err := tx.Exec(ctx, "ANALYZE "+table+" ("+columns+")"); err != nil {
			return err
		}
		report.After.AnalyzeMS = elapsedMS(start)
		if err := tx.QueryRow(ctx, `SELECT (pg_column_size(most_common_vals)+pg_column_size(most_common_freqs)+
				pg_column_size(most_common_base_freqs))::bigint
			FROM pg_stats_ext WHERE statistics_schemaname=$1 AND statistics_name=$2
				AND schemaname=$1 AND tablename=$3 AND NOT inherited`,
			request.Target.Schema, ownedExtendedStatisticsName, request.Target.Relation).Scan(&report.After.Bytes); err != nil {
			return err
		}
		if report.After.Bytes <= 0 || report.After.Bytes > maxStatisticsBytes {
			return errors.New("joint statistics data exceeded the owned fixture budget")
		}
		start = time.Now()
		sampleTx, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		report.After.Samples, err = sampleOwnedStatistics(ctx, sampleTx, request, current)
		if err = errors.Join(err, sampleTx.Rollback(ctx)); err != nil {
			return err
		}
		report.After.VerifyMS = elapsedMS(start)
		report.After.Applied, report.After.Correlated = true, true
		for i, sample := range report.After.Samples {
			if sample.PlanReturned != report.Before.Samples[i].PlanReturned {
				outcome, reason = "inconclusive", "lookup results changed during verification"
				return errOwnedStatisticsRollback
			}
		}
		outcome, reason = verifyStatistics(report.Before, report.After)
		if outcome != "improved_tested_inputs" {
			return errOwnedStatisticsRollback
		}
		return guards.CheckPermission(ctx)
	})
	if errors.Is(err, errOwnedStatisticsRollback) {
		return outcome, reason, nil
	}
	if err != nil {
		return outcome, reason, fmt.Errorf("owned statistics trial: %w", err)
	}
	report.After.Retained = true
	return outcome, reason, nil
}
