package hostedquery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	trialRuns          = 10
	maxTrialIndexBytes = 8 * 1024 * 1024
)

type IndexInvestigator func(context.Context, IndexEvidence) (IndexProposal, error)

type IndexTrialReport struct {
	JobID        int64         `json:"job_id,omitempty"`
	RunID        string        `json:"run_id"`
	Outcome      string        `json:"outcome"`
	Reason       string        `json:"reason"`
	Evidence     IndexEvidence `json:"evidence"`
	Proposal     IndexProposal `json:"proposal"`
	Before       TrialSample   `json:"before"`
	After        TrialSample   `json:"after"`
	IndexValid   bool          `json:"index_valid"`
	HealthyBuilt bool          `json:"healthy_index_built"`
	IndexBytes   int64         `json:"index_bytes"`
	DetectWallMS float64       `json:"detect_wall_ms"`
	ModelWallMS  float64       `json:"investigation_wall_ms"`
	BuildWallMS  float64       `json:"build_wall_ms"`
	VerifyWallMS float64       `json:"verify_wall_ms"`
	TotalWallMS  float64       `json:"total_wall_ms"`
}

type TrialInput struct {
	Input      int32   `json:"input"`
	ReadMS     float64 `json:"read_mean_ms"`
	ReadMAD    float64 `json:"read_mad_ms"`
	HealthyMS  float64 `json:"healthy_mean_ms"`
	HealthyMAD float64 `json:"healthy_mad_ms"`
	WriteMS    float64 `json:"write_mean_ms"`
	WriteMAD   float64 `json:"write_mad_ms"`
}

type TrialSample struct {
	ReadMS          float64      `json:"read_mean_ms"`
	ReadMAD         float64      `json:"read_mad_ms"`
	HealthyMS       float64      `json:"healthy_mean_ms"`
	HealthyMAD      float64      `json:"healthy_mad_ms"`
	WriteMS         float64      `json:"write_mean_ms"`
	WriteMAD        float64      `json:"write_mad_ms"`
	Reads           int          `json:"read_calls"`
	HealthyReads    int          `json:"healthy_read_calls"`
	Writes          int          `json:"write_calls"`
	Errors          int          `json:"errors"`
	Blocks          float64      `json:"blocks_per_read"`
	Plan            string       `json:"plan"`
	PlanReturned    int64        `json:"returned_rows"`
	PlanExamined    int64        `json:"examined_rows"`
	Inputs          []TrialInput `json:"inputs"`
	HealthyBreaches int          `json:"healthy_objective_breaches"`
	Breaches        int          `json:"candidate_objective_breaches"`
}

func RunIndexTrial(ctx context.Context, target, state *pgx.Conn, investigate IndexInvestigator, identity string) (IndexTrialReport, error) {
	return runIndexTrial(ctx, target, state, investigate, identity, nil)
}

// DrillIndexTrialRecovery leaves the synthetic fixture pending after a successful
// build so a fresh process can inspect it. A timeout leaves the same pending state.
func DrillIndexTrialRecovery(ctx context.Context, target, state *pgx.Conn, investigate IndexInvestigator, identity string, ready func()) (IndexTrialReport, error) {
	return runIndexTrial(ctx, target, state, investigate, identity, func() error {
		ready()
		wait := time.NewTimer(20 * time.Second)
		defer wait.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wait.C:
			return errors.New("recovery drill pause expired; fixture is pending")
		}
	})
}

// ScriptedInvestigator provides a deterministic recommendation for fixture qualification without a model credential.
func ScriptedInvestigator(_ context.Context, e IndexEvidence) (IndexProposal, error) {
	return IndexProposal{Outcome: "propose", RunID: e.RunID, TableOID: e.TableOID, Column: e.Column, Reason: "Selective sequential scan on the enrolled synthetic lookup."}, nil
}

func runIndexTrial(ctx context.Context, target, state *pgx.Conn, investigate IndexInvestigator, identity string, afterBuild func() error) (report IndexTrialReport, err error) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	if investigate == nil || identity == "" {
		return report, errors.New("fixture needs an investigator and authorized target identity")
	}
	if err := prepareJournal(ctx, state); err != nil {
		return report, fmt.Errorf("prepare journal: %w", err)
	}
	if err := lockTrial(ctx, target); err != nil {
		return report, err
	}
	defer func() { err = errors.Join(err, releaseTrialLock(target)) }()
	identity = journalTarget(target, identity)
	if err := reconcileTrials(ctx, target, state, identity); err != nil {
		return report, err
	}
	if err := trialCapacity(ctx, target); err != nil {
		return report, err
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return report, err
	}
	report.RunID = hex.EncodeToString(nonce[:])
	if _, err := state.Exec(ctx, "INSERT INTO "+journalTable+" (id,target,status) VALUES($1,$2,'setup')", report.RunID, identity); err != nil {
		return report, fmt.Errorf("record fixture intent: %w", err)
	}
	if err := createTrial(ctx, target, state, report.RunID); err != nil {
		return report, err
	}
	expected, err := expectedFixture(ctx, state, report.RunID)
	if err != nil {
		return report, err
	}
	detectStart := time.Now()
	report.Before, err = sampleTrial(ctx, target, report.RunID, expected)
	if err != nil {
		return report, fmt.Errorf("record before evidence: %w", err)
	}
	report.DetectWallMS = elapsedMS(detectStart)
	if report.Before.HealthyBreaches >= 3 {
		return report, errors.New("healthy comparison breached the lab objective")
	}
	report.Evidence, err = trialEvidence(ctx, target, report.RunID, report.Before)
	if err != nil {
		return report, err
	}
	if report.Evidence.Decision == Investigate {
		modelStart := time.Now()
		report.Proposal, err = investigate(ctx, report.Evidence)
		report.ModelWallMS = elapsedMS(modelStart)
		if err != nil {
			return report, fmt.Errorf("investigate fixture: %w", err)
		}
	}
	report, err = applyIndexTrial(ctx, target, state, report, afterBuild)
	if err != nil {
		return report, err
	}
	return finishTrial(ctx, target, state, report, started)
}

func applyIndexTrial(ctx context.Context, target, state *pgx.Conn, report IndexTrialReport, afterBuild func() error) (IndexTrialReport, error) {
	schema := trialSchema(report.RunID)
	if reason := qualifyIndex(report.Evidence, report.Proposal, time.Now()); reason != nil {
		report.Outcome, report.Reason = "no_change", reason.Error()
		return report, nil
	}
	if err := trialCapacity(ctx, target); err != nil {
		return report, err
	}
	latest, err := trialEvidence(ctx, target, report.RunID, report.Before)
	if err != nil {
		return report, err
	}
	latest.At, latest.RunID = report.Evidence.At, report.Evidence.RunID
	if err := qualifyIndex(latest, report.Proposal, time.Now()); err != nil {
		report.Outcome, report.Reason = "no_change", err.Error()
		return report, nil
	}
	expected, err := expectedFixture(ctx, state, report.RunID)
	if err != nil {
		return report, err
	}
	err = validateSampleFixture(ctx, target, report.RunID, expected)
	if err != nil || expected.Candidate != report.Proposal.TableOID {
		return report, errors.Join(errors.New("enrolled fixture changed before build"), err)
	}
	existing, err := inspectTrialIndex(ctx, target, schema)
	if err != nil || existing.Exists {
		return report, errors.Join(errors.New("reserved trial index name is occupied"), err)
	}
	building := "building"
	if report.JobID != 0 {
		building = "queued_building"
	}
	if err := writeTrialStatus(ctx, state, report.RunID, building, nil, &report); err != nil {
		return report, err
	}
	buildStart := time.Now()
	if err := buildTrialIndex(ctx, target, schema); err != nil {
		return report, fmt.Errorf("build index (journal pending): %w", err)
	}
	report.BuildWallMS = elapsedMS(buildStart)
	if afterBuild != nil {
		if err := afterBuild(); err != nil {
			return report, err
		}
	}
	index, err := inspectTrialIndex(ctx, target, schema)
	if err != nil {
		return report, err
	}
	if !index.Valid || !index.Ready || !index.Matches || index.Bytes > maxTrialIndexBytes {
		return report, errors.New("trial index failed catalog or size validation")
	}
	report.IndexValid, report.IndexBytes = true, index.Bytes
	verifyStart := time.Now()
	report.After, err = sampleTrial(ctx, target, report.RunID, expected)
	if err != nil {
		report.Outcome, report.Reason = "failed", "verification could not complete the measured fixture workload"
		return report, errors.Join(fmt.Errorf("verify outcome: %w", err),
			writeTrialStatus(ctx, state, report.RunID, building, nil, &report))
	}
	if err := target.QueryRow(ctx, "SELECT count(*)<>1 FROM pg_index WHERE indrelid=$1", expected.Healthy).Scan(&report.HealthyBuilt); err != nil {
		return report, err
	}
	if report.HealthyBuilt {
		return report, errors.New("healthy comparison index catalog changed")
	}
	report.VerifyWallMS = elapsedMS(verifyStart)
	report.Outcome, report.Reason = compareTrial(report.Before, report.After)
	return report, nil
}

func finishTrial(ctx context.Context, target, state *pgx.Conn, report IndexTrialReport, started time.Time) (IndexTrialReport, error) {
	status := "verified"
	if !report.IndexValid {
		status = "declined"
	}
	if err := writeTrialStatus(ctx, state, report.RunID, status, nil, &report); err != nil {
		return report, err
	}
	if _, err := cleanTrial(ctx, target, state, report.RunID); err != nil {
		return report, err
	}
	report.TotalWallMS = elapsedMS(started)
	return report, writeTrialStatus(ctx, state, report.RunID, "complete", nil, &report)
}

func releaseTrialLock(target *pgx.Conn) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return unlockTrial(ctx, target)
}

func elapsedMS(start time.Time) float64 {
	return float64(time.Since(start)) / float64(time.Millisecond)
}

func trialCapacity(ctx context.Context, target *pgx.Conn) error {
	var permitted bool
	err := target.QueryRow(ctx, `
		SELECT NOT pg_is_in_recovery() AND pg_has_role(current_user,'pg_read_all_stats','USAGE')
		 AND NOT EXISTS (SELECT FROM pg_locks WHERE NOT granted)
		 AND (SELECT count(*) FROM pg_stat_activity WHERE backend_type='client backend'
		        AND state='active' AND pid<>pg_backend_pid()) <= 2
	`).Scan(&permitted)
	if err != nil {
		return err
	}
	if !permitted {
		return errors.New("fixture deferred: activity visibility, locks or capacity unavailable")
	}
	return nil
}

func createTrial(ctx context.Context, target, state *pgx.Conn, id string) error {
	schema := trialSchema(id)
	return pgx.BeginFunc(ctx, target, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout='5s'"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout='250ms'"); err != nil {
			return err
		}
		name := pgx.Identifier{schema}.Sanitize()
		if _, err := tx.Exec(ctx, "CREATE SCHEMA "+name); err != nil {
			return err
		}
		// id is generated from random bytes and hex-encoded by the local worker.
		if _, err := tx.Exec(ctx, "COMMENT ON SCHEMA "+name+" IS 'pooltune trial "+id+"'"); err != nil {
			return err
		}
		if err := seedFixtureTables(ctx, tx, schema); err != nil {
			return err
		}
		ids, err := loadFixture(ctx, tx, id)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(ids)
		if err != nil {
			return err
		}
		tag, err := state.Exec(ctx, "UPDATE "+journalTable+" SET objects=$2::jsonb,index_name=$3,index_definition=$4 WHERE id=$1 AND status='setup'",
			id, string(raw), trialIndex, trialDefinition)
		if err == nil && tag.RowsAffected() != 1 {
			return errors.New("fixture intent disappeared before commit")
		}
		return err
	})
}

func trialEvidence(ctx context.Context, target *pgx.Conn, id string, sample TrialSample) (IndexEvidence, error) {
	e := IndexEvidence{RunID: id, At: time.Now().UTC(), Decision: NoChange, Label: "lookup",
		Plan: PlanEvidence{Node: sample.Plan, ExaminedRows: sample.PlanExamined, ReturnedRows: sample.PlanReturned,
			SharedBlocks: int64(sample.Blocks)}}
	if sample.Breaches >= 3 {
		e.Decision = Investigate
	}
	catalog, err := collectIndexCatalog(ctx, target, id)
	e.TableOID, e.Column, e.Existing = catalog.TableOID, catalog.Column, catalog.Existing
	return e, err
}

func collectIndexCatalog(ctx context.Context, target trialReader, id string) (IndexEvidence, error) {
	e := IndexEvidence{Label: "lookup"}
	err := target.QueryRow(ctx, `
		SELECT c.oid, a.attnum, EXISTS (SELECT FROM pg_index i JOIN pg_class ix ON ix.oid=i.indexrelid
		  JOIN pg_am am ON am.oid=ix.relam WHERE i.indrelid=c.oid AND i.indisvalid
		  AND i.indisready AND i.indpred IS NULL AND i.indkey[0]=a.attnum AND am.amname='btree')
		FROM pg_class c JOIN pg_namespace n ON c.relnamespace=n.oid
		  JOIN pg_attribute a ON a.attrelid=c.oid
		WHERE n.nspname=$1 AND obj_description(n.oid,'pg_namespace')=$2 AND c.relname='candidate'
		  AND c.relkind='r' AND NOT c.relispartition
		  AND a.attname='lookup' AND a.attnum>0 AND NOT a.attisdropped AND a.atttypid='int4'::regtype
		  AND pg_relation_size(c.oid) <= 25165824
	`, trialSchema(id), "pooltune trial "+id).Scan(&e.TableOID, &e.Column, &e.Existing)
	return e, err
}

type trialIndexState struct {
	Exists, Valid, Ready, Matches bool
	Bytes                         int64
}

func inspectTrialIndex(ctx context.Context, target trialReader, schema string) (trialIndexState, error) {
	var index trialIndexState
	err := target.QueryRow(ctx, `
		SELECT true, i.indisvalid, i.indisready, c.relname='candidate' AND c.relnamespace=n.oid
		  AND NOT i.indisunique AND i.indnkeyatts=1 AND i.indnatts=1
		  AND i.indpred IS NULL AND i.indexprs IS NULL AND a.attname='lookup' AND am.amname='btree'
		  AND i.indoption[0]=0 AND i.indclass[0]=(SELECT oid FROM pg_opclass WHERE opcname='int4_ops' AND opcmethod=am.oid AND opcnamespace='pg_catalog'::regnamespace), pg_relation_size(ix.oid)
		FROM pg_class ix JOIN pg_namespace n ON ix.relnamespace=n.oid
		  JOIN pg_index i ON i.indexrelid=ix.oid JOIN pg_class c ON c.oid=i.indrelid
		  JOIN pg_am am ON am.oid=ix.relam JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum=i.indkey[0]
		WHERE n.nspname=$1 AND ix.relname=$2
	`, schema, trialIndex).Scan(&index.Exists, &index.Valid, &index.Ready, &index.Matches, &index.Bytes)
	if errors.Is(err, pgx.ErrNoRows) {
		// A same-name object of another kind is a collision, never an absent index.
		var collision bool
		err = target.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", qualified(schema, trialIndex)).Scan(&collision)
		if err == nil && collision {
			return index, errors.New("index name is occupied by an unexpected object")
		}
	}
	return index, err
}

func buildTrialIndex(ctx context.Context, target *pgx.Conn, schema string) (err error) {
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		for _, reset := range []string{"RESET lock_timeout", "RESET statement_timeout", "RESET maintenance_work_mem", "RESET max_parallel_maintenance_workers"} {
			_, resetErr := target.Exec(cleanup, reset)
			err = errors.Join(err, resetErr)
		}
	}()
	for _, setting := range []string{"SET lock_timeout='250ms'", "SET statement_timeout='5s'", "SET maintenance_work_mem='16MB'", "SET max_parallel_maintenance_workers=0"} {
		if _, err := target.Exec(ctx, setting); err != nil {
			return err
		}
	}
	_, err = target.Exec(ctx, "CREATE INDEX CONCURRENTLY "+pgx.Identifier{trialIndex}.Sanitize()+" ON "+
		qualified(schema, "candidate")+" USING btree (lookup)")
	return err
}
