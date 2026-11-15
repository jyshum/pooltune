package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"pooltune/internal/graphrunner"
	"pooltune/internal/hostedquery"
	"pooltune/internal/supabaseproject"

	"github.com/jackc/pgx/v5"
)

type ownedRepairSettings struct {
	Installation, Operation string
	Database, Role          uint32
	Query                   int64
	Automatic               bool
	Statistics              hostedquery.OwnedStatisticsRepairRequest
	Vacuum                  hostedquery.OwnedVacuumRequest
	Memory                  hostedquery.OwnedMemoryRequest
}

func readOwnedCronFixture(stateDSN string) (*hostedquery.OwnedProjectCron, error) {
	binding := os.Getenv("POOLTUNE_OWNED_CRON_FIXTURE")
	if binding == "" {
		return nil, nil
	}
	invalid := errors.New("the owned Rookery Cron fixture configuration is invalid")
	var settings struct {
		Installation string
		Query        int64
		Request      hostedquery.OwnedCronRequest
	}
	if os.Getenv("POOLTUNE_STAGING_PRIVATE_STATE") != "1" || json.Unmarshal([]byte(binding), &settings) != nil {
		return nil, invalid
	}
	project, err := supabaseproject.New(hostedquery.OwnedIndexRookeryRef, settings.Installation)
	if err != nil {
		return nil, invalid
	}
	writer, err := supabaseproject.DirectConfig(project.Ref(), project.OwnedRepairProbeRole("cron"),
		os.Getenv("POOLTUNE_OWNED_CRON_PASSWORD"), []byte(os.Getenv("POOLTUNE_OWNED_CRON_CA")))
	if err != nil {
		return nil, invalid
	}
	diagnose, err := graphrunner.DiagnosticProcess(stateDSN)
	if err != nil {
		return nil, invalid
	}
	fixture := &hostedquery.OwnedProjectCron{Config: writer, Request: settings.Request, QueryID: settings.Query,
		Investigate: graphrunner.AdaptDiagnostic(diagnose, nil)}
	if !fixture.Matches(project) {
		return nil, invalid
	}
	return fixture, nil
}

func readOwnedRepairFixture(stateDSN string) (*hostedquery.OwnedProjectRepair, error) {
	binding := os.Getenv("POOLTUNE_OWNED_REPAIR_FIXTURE")
	if binding == "" {
		return nil, nil
	}
	invalid := errors.New("the owned Rookery repair fixture configuration is invalid")
	var settings ownedRepairSettings
	if os.Getenv("POOLTUNE_STAGING_PRIVATE_STATE") != "1" || json.Unmarshal([]byte(binding), &settings) != nil {
		return nil, invalid
	}
	project, err := supabaseproject.New(hostedquery.OwnedIndexRookeryRef, settings.Installation)
	if err != nil {
		return nil, invalid
	}
	role := project.OwnedRepairProbeRole(settings.Operation)
	target := settings.Statistics.Target
	switch settings.Operation {
	case "analyze", "statistics":
		if settings.Statistics.GuardTableOID == 0 {
			return nil, invalid
		}
	case "vacuum":
		target = settings.Vacuum.Workload.Target
	case "memory":
		target = settings.Memory.Workload.Target
	default:
		return nil, invalid
	}
	if target.Schema != role || target.DatabaseOID != settings.Database || target.TableOID == 0 {
		return nil, invalid
	}
	writer, err := supabaseproject.DirectConfig(project.Ref(), role, os.Getenv("POOLTUNE_OWNED_REPAIR_PASSWORD"),
		[]byte(os.Getenv("POOLTUNE_OWNED_REPAIR_CA")))
	if err != nil {
		return nil, invalid
	}
	diagnose, err := graphrunner.DiagnosticProcess(stateDSN)
	if err != nil {
		return nil, invalid
	}
	fixture := &hostedquery.OwnedProjectRepair{Installation: settings.Installation, Operation: settings.Operation,
		Database: settings.Database, Role: settings.Role, Query: settings.Query, Automatic: settings.Automatic,
		Config: writer, Investigate: graphrunner.AdaptDiagnostic(diagnose, nil)}
	fixture.Run = func(ctx context.Context, invocation hostedquery.OwnedRepairInvocation, investigate hostedquery.IndexInvestigator,
		guards hostedquery.OwnedRepairGuards) (hostedquery.DispatchResult, error) {
		switch settings.Operation {
		case "vacuum":
			request := settings.Vacuum
			bindOwnedWorkload(&request.Workload, invocation)
			report, err := hostedquery.RunOwnedProjectVacuum(ctx, writer, request, investigate, hostedquery.ProjectMaintenanceGuards{
				CheckPermission: func(ctx context.Context, _ hostedquery.ProjectMaintenanceIntent) error {
					return guards.CheckPermission(ctx)
				},
				CheckCapacity: func(ctx context.Context, _ hostedquery.ProjectMaintenanceIntent) error {
					return guards.CheckCapacity(ctx)
				},
				RecordIntent: func(ctx context.Context, intent hostedquery.ProjectMaintenanceIntent) error {
					return guards.RecordIntent(ctx, intent)
				},
			})
			return hostedquery.DispatchResult{Outcome: report.Outcome, DiagnosticReason: report.Reason, Vacuum: &report,
				Proposal: &report.Proposal, Evidence: &report.Evidence, GraphMS: report.GraphMS}, err
		case "memory":
			request := settings.Memory
			bindOwnedWorkload(&request.Workload, invocation)
			request.Function.Project = invocation.Project
			report, err := hostedquery.RunOwnedProjectMemory(ctx, writer, request, investigate, hostedquery.ProjectFunctionMemoryGuards{
				CheckPermission: func(ctx context.Context, _ hostedquery.ProjectFunctionMemoryIntent) error {
					return guards.CheckPermission(ctx)
				},
				CheckCapacity: func(ctx context.Context, _ hostedquery.ProjectFunctionMemoryIntent) error {
					return guards.CheckCapacity(ctx)
				},
				RecordIntent: func(ctx context.Context, intent hostedquery.ProjectFunctionMemoryIntent) error {
					return guards.RecordIntent(ctx, intent)
				},
			})
			return hostedquery.DispatchResult{Outcome: report.Outcome, DiagnosticReason: report.Reason, Memory: &report,
				Proposal: &report.Proposal, Evidence: &report.Evidence, GraphMS: report.GraphMS}, err
		}
		request := settings.Statistics
		request.Operation, request.ProjectID, request.RunID = settings.Operation, invocation.Project, invocation.RunID
		request.OwnerRoleOID, request.QueryID = invocation.Role, invocation.Query
		return hostedquery.RunOwnedStatisticsRepair(ctx, writer, request, investigate, hostedquery.OwnedStatisticsRepairGuards{
			CheckPermission: guards.CheckPermission, CheckCapacity: guards.CheckCapacity, RecordIntent: guards.RecordIntent,
		})
	}
	if settings.Operation == "vacuum" {
		fixture.Capacity = func(ctx context.Context, writer *pgx.Conn) error {
			var clear bool
			function := pgx.Identifier{role, "vacuum_conditions_clear"}.Sanitize()
			err := writer.QueryRow(ctx, "SELECT "+function+"()").Scan(&clear)
			if err == nil && !clear {
				return errors.New("the owned vacuum visibility conditions are not clear")
			}
			return err
		}
	}
	if !fixture.Matches(project) {
		return nil, invalid
	}
	return fixture, nil
}

func bindOwnedWorkload(workload *hostedquery.OwnedRepairWorkload, invocation hostedquery.OwnedRepairInvocation) {
	workload.Project, workload.RunID, workload.RoleOID, workload.QueryID = invocation.Project, invocation.RunID, invocation.Role, invocation.Query
	workload.Decision = hostedquery.Investigate
}
