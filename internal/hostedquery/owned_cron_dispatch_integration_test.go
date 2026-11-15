//go:build integration

package hostedquery

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
)

func TestDispatchOwnedCronResumesExactClaimWithoutRerunningModelOrWrite(t *testing.T) {
	ctx, local, state, epoch, origin, req, intent := newOwnedCronCheckpoint(t, time.Now().Add(-14*time.Minute))
	const key = "0123456789abcdef"
	project, err := supabaseproject.New(OwnedIndexRookeryRef, key)
	if err != nil {
		t.Fatal(err)
	}
	schema := project.OwnedRepairProbeRole("cron")
	req.Candidate = ProjectIndexTarget{Schema: schema, Relation: "candidate", TableOID: 12, DatabaseOID: origin.Database}
	req.Target.Name = schema + "_target"
	req.Target.CommandDigest = ownedCronDigest("CALL " + qualified(schema, cronProcedure) + "(200)")
	req.ReadOID, req.ProcedureOID = 13, 14
	req.ReadDigest, req.ProcedureDigest = ownedCronDigest("test read"), ownedCronDigest("test procedure")
	writer := local.Copy()
	writer.Host, writer.Port, writer.User = "db."+OwnedIndexRookeryRef+".supabase.co", 5432, schema
	writer.Fallbacks = nil
	writer.TLSConfig = &tls.Config{ServerName: writer.Host, RootCAs: x509.NewCertPool()}
	var modelCalls, admissions, recordedActions, phases int
	fixture := &OwnedProjectCron{Config: writer, Request: req,
		Investigate: func(context.Context, IndexEvidence) (IndexProposal, error) {
			modelCalls++
			return IndexProposal{}, nil
		},
		AdmitModel: func(context.Context) error { admissions++; return nil },
	}
	if !fixture.Matches(project) {
		t.Fatal("test route did not bind exact Rookery fixture")
	}
	if _, err := state.Exec(ctx, `DELETE FROM pooltune_worker.owned_project_actions WHERE job_id=$1`, origin.ID); err != nil {
		t.Fatal(err)
	}
	origin.Generation = []time.Time{req.Observed.Boot, req.Observed.Reset, req.Observed.Entry}
	origin.queueEvidence, err = json.Marshal(struct {
		Objective  float64     `json:"objective_ms"`
		Generation []time.Time `json:"counter_generation"`
	}{3, origin.Generation})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.Exec(ctx, `UPDATE pooltune_worker.investigations SET evidence=$2::jsonb WHERE id=$1`, origin.ID, string(origin.queueEvidence)); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{project.Role()}.Sanitize()+`;
		CREATE TABLE cron_monitor_samples(database_oid oid, role_oid oid, query_id bigint,
			boot timestamptz, reset_at timestamptz, entry_since timestamptz, mean_execution_ms double precision, calls bigint);
		CREATE FUNCTION `+qualified(project.Role(), "snapshot")+`() RETURNS SETOF cron_monitor_samples
		LANGUAGE SQL STABLE AS 'SELECT * FROM cron_monitor_samples'`); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Exec(ctx, `INSERT INTO cron_monitor_samples VALUES($1,$2,$3,$4,$5,$6,3,100)`,
		origin.Database, origin.Role, origin.Query, origin.Generation[0], origin.Generation[1], origin.Generation[2]); err != nil {
		t.Fatal(err)
	}
	monitor := func(ctx context.Context) (*pgx.Conn, func(), error) {
		conn, err := pgx.ConnectConfig(ctx, local)
		return conn, func() { _ = conn.Close(ctx) }, err
	}
	run := func(ctx context.Context, _ *pgx.ConnConfig, current OwnedCronRequest, prior *OwnedCronAction,
		read OwnedCronPeriodReader, investigate IndexInvestigator, guards OwnedCronGuards) (OwnedCronDecision, error) {
		phases++
		if current.Target != req.Target || !sameCronCounter(current.Observed, req.Observed) {
			t.Fatalf("dispatch used an unbound request: %+v", current)
		}
		if _, err := read(ctx, current, current.FirstCycle); err != nil {
			t.Fatal(err)
		}
		switch phases {
		case 1:
			return deferOwnedCron(CronReport{JobID: current.JobID, Fixture: schema}, "awaiting_evidence", time.Now().Add(time.Hour)), nil
		case 2:
			if prior != nil || investigate == nil || guards.AdmitModel == nil {
				t.Fatal("evidence continuation missed its first authorized action")
			}
			if err := guards.AdmitModel(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := investigate(ctx, IndexEvidence{}); err != nil {
				t.Fatal(err)
			}
			intent.Intent.Target, intent.Observed = current.Target, current.Observed
			intent.Report.JobID, intent.Report.Fixture = current.JobID, schema
			if err := guards.RecordIntent(ctx, intent); err != nil {
				t.Fatal(err)
			}
			if err := guards.CheckPermission(ctx, intent.Intent); err != nil {
				t.Fatal(err)
			}
			recordedActions++
			receipt := intent
			receipt.After = ProjectCronSnapshot{Schedule: cronAlternate, Fingerprint: intent.Intent.Before.Fingerprint, Generation: "new"}
			future := intent.Intent.FirstFutureCycle
			receipt.Report.CommitAt = future.Add(-18 * time.Second)
			receipt.Report.PostWitness = []CronRun{
				{ID: 91, JobID: current.Target.WitnessJobID, PID: 1, Start: future.Add(-17 * time.Second), End: future.Add(-16 * time.Second), Status: "succeeded"},
				{ID: 92, JobID: current.Target.WitnessJobID, PID: 2, Start: future.Add(-15 * time.Second), End: future.Add(-14 * time.Second), Status: "succeeded"},
			}
			if err := guards.RecordAcknowledgement(ctx, receipt); err != nil {
				t.Fatal(err)
			}
			return deferOwnedCron(receipt.Report, "awaiting_verification", receipt.Report.VerifyAfter), nil
		default:
			if prior == nil || prior.Report.CommitAt.IsZero() || investigate != nil || guards.AdmitModel != nil || guards.RecordIntent != nil {
				t.Fatalf("verification did not use an inspection-only action: %+v", prior)
			}
			return ownedCronResult(prior.Report, "improved_tested_inputs", "verified from later evidence"), nil
		}
	}
	if result, err := fixture.dispatchWith(ctx, state, project, epoch, origin, nil, monitor, run); result != nil || err != nil || phases != 0 {
		t.Fatalf("initial claim bypassed normal evidence: %+v %v", result, err)
	}
	windows := &ObservedLookupLatency{SharedBlockAccesses: []int64{1, 1, 1, 1, 1}}
	if result, err := fixture.dispatchWith(ctx, state, project, epoch, origin, windows, monitor, run); err != nil || result == nil || result.Outcome != "awaiting_evidence" {
		t.Fatalf("initial dispatch: %+v %v", result, err)
	}
	if job, err := claimProjectInvestigation(ctx, state, project.ID(), &epoch); err != nil || job != nil {
		t.Fatalf("evidence continuation ran early: %+v %v", job, err)
	}
	if _, err := state.Exec(ctx, `UPDATE pooltune_worker.investigations SET not_before=clock_timestamp() WHERE parent_id=$1`, origin.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := claimProjectInvestigation(ctx, state, project.ID(), &epoch)
	if err != nil || claim == nil {
		t.Fatalf("claim evidence continuation: %+v %v", claim, err)
	}
	if result, err := fixture.dispatchWith(ctx, state, project, epoch, *claim, nil, monitor, run); err != nil || result == nil || result.Outcome != "awaiting_verification" {
		t.Fatalf("first action dispatch: %+v %v", result, err)
	}
	restarted, err := pgx.ConnectConfig(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close(ctx)
	claim, err = claimProjectInvestigation(ctx, restarted, project.ID(), &epoch)
	if err != nil || claim == nil {
		t.Fatalf("claim verification continuation: %+v %v", claim, err)
	}
	result, err := fixture.dispatchWith(ctx, restarted, project, epoch, *claim, nil, monitor, run)
	if err != nil || result == nil || result.Outcome != "improved_tested_inputs" || phases != 3 || modelCalls != 1 || admissions != 1 || recordedActions != 1 {
		t.Fatalf("verification repeated work or lost the original action: %+v %v, phases %d model %d admission %d actions %d",
			result, err, phases, modelCalls, admissions, recordedActions)
	}
}
