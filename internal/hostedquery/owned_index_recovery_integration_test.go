//go:build integration

package hostedquery

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"pooltune/internal/supabaseproject"
	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPendingOwnedIndexRecoveryUsesBoundedClaimsAcrossDisconnects(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	state, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	project, err := supabaseproject.New(OwnedIndexRookeryRef, "abcdef0123456789")
	if err != nil {
		t.Fatal(err)
	}
	epoch, err := ActivateProjectMonitor(ctx, state, project, false)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := state.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO pooltune_worker.investigations (project_id,database_oid,role_oid,query_id,evidence)
		VALUES ($1,1,2,3,'{}'::jsonb), ($1,1,2,4,'{}'::jsonb)`, project.ID())
	scope := ownedIndexRecoveryScope{Database: 1, Role: 2, Query: 3}
	first, err := claimProjectInvestigation(ctx, state, project.ID(), &epoch, scope)
	if err != nil || first == nil || first.Query != 3 || first.RecoveryOnly {
		t.Fatalf("the ordinary claim was not eligible to investigate: %+v %v", first, err)
	}
	if err := recordOwnedIndexIntent(ctx, state, project.ID(), epoch, *first, ProjectIndexActionIntent{RunID: "interrupted"}); err != nil {
		t.Fatal(err)
	}
	reconnect := func() {
		t.Helper()
		priorEpoch := epoch
		if err := DisconnectProjectMonitor(ctx, state, project); err != nil {
			t.Fatal(err)
		}
		for _, disabled := range []int64{priorEpoch, -priorEpoch} {
			if claim, err := claimProjectInvestigation(ctx, state, project.ID(), &disabled, scope); err != nil || claim != nil {
				t.Fatalf("old or disconnected epoch claimed pending action: %+v %v", claim, err)
			}
		}
		var disconnected string
		if err := state.QueryRow(ctx, `SELECT result->>'outcome' FROM pooltune_worker.investigations
			WHERE project_id=$1 AND query_id=4 AND finished_at IS NOT NULL`, project.ID()).Scan(&disconnected); err != nil || disconnected != "project_disconnected" {
			t.Fatalf("ordinary investigation survived disconnect: %q %v", disconnected, err)
		}
		var err error
		epoch, err = ActivateProjectMonitor(ctx, state, project, true)
		if err != nil || epoch <= priorEpoch {
			t.Fatalf("reconnect did not advance permission epoch: %d %v", epoch, err)
		}
	}
	reconnect()
	if err := pgx.BeginFunc(ctx, state, func(tx pgx.Tx) error {
		return retireChangedEnrollments(ctx, tx, Enrollment{Project: project.ID()})
	}); err != nil {
		t.Fatal(err)
	}
	for _, wrong := range [][]ownedIndexRecoveryScope{
		nil, {{Database: 2, Role: 2, Query: 3}}, {{Database: 1, Role: 3, Query: 3}}, {{Database: 1, Role: 2, Query: 4}},
	} {
		if claim, err := claimProjectInvestigation(ctx, state, project.ID(), &epoch, wrong...); err != nil || claim != nil {
			t.Fatalf("wrong recovery authority claimed the pending action: %+v %v", claim, err)
		}
	}
	for attempt := 2; attempt <= 4; attempt++ {
		retry, err := claimProjectInvestigation(ctx, state, project.ID(), &epoch, scope)
		if err != nil || retry == nil || retry.ID != first.ID || retry.Attempt != attempt || !retry.RecoveryOnly {
			t.Fatalf("expected read-only attempt %d for exact pending action: %+v %v", attempt, retry, err)
		}
		if err := finishInvestigation(ctx, state, *retry, DispatchResult{Outcome: "no_change"}, epoch); !errors.Is(err, errClaimLost) {
			t.Fatalf("recovery-only claim published an ordinary result: %v", err)
		}
		if attempt == 2 {
			reconnect()
		} else {
			exec("UPDATE pooltune_worker.investigations SET lease_until=clock_timestamp() WHERE id=$1", retry.ID)
		}
	}

	exec(`INSERT INTO pooltune_worker.investigations (project_id,database_oid,role_oid,query_id,evidence,attempts,lease_until)
		VALUES ($1,1,2,5,'{}'::jsonb,2,clock_timestamp()), ($1,1,2,6,'{}'::jsonb,0,NULL)`, project.ID())
	ordinary, err := claimProjectInvestigation(ctx, state, project.ID(), &epoch, scope)
	if err != nil || ordinary == nil || ordinary.Query != 6 || ordinary.RecoveryOnly {
		t.Fatalf("capped pending action blocked or authorized an unrelated job: %+v %v", ordinary, err)
	}
	var pending, open bool
	if err := state.QueryRow(ctx, `SELECT a.outcome='pending', j.finished_at IS NULL
		FROM pooltune_worker.owned_index_actions a JOIN pooltune_worker.investigations j ON j.id=a.job_id
		WHERE a.job_id=$1`, first.ID).Scan(&pending, &open); err != nil || !pending || !open {
		t.Fatalf("exhaustion discarded the journal or the original query's open-job guard: %v %v %v", pending, open, err)
	}
	var exhausted string
	if err := state.QueryRow(ctx, `SELECT result->>'outcome' FROM pooltune_worker.investigations
		WHERE project_id=$1 AND query_id=5 AND finished_at IS NOT NULL`, project.ID()).Scan(&exhausted); err != nil || exhausted != "retry_exhausted" {
		t.Fatalf("ordinary attempt policy was changed: %q %v", exhausted, err)
	}
}

func TestPublishOwnedIndexRecoveryOnlyForMatchingCurrentClaim(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	state, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := state.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	const project = "synthetic-index-reconciliation"
	epoch := int64(3)
	exec("INSERT INTO pooltune_worker.projects(project_id,monitor_epoch) VALUES($1,$2)", project, epoch)
	exec(`INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence)
		VALUES($1,1,2,3,'{}'::jsonb)`, project)
	first, err := claimProjectInvestigation(ctx, state, project, &epoch)
	if err != nil || first == nil || first.Attempt != 1 {
		t.Fatalf("expected first claim: %+v %v", first, err)
	}
	intent := ProjectIndexActionIntent{RunID: fmt.Sprintf("owned-index-%d-1", first.ID), IndexName: "pooltune_ix_10_2_3"}
	if err := recordOwnedIndexIntent(ctx, state, project, epoch, *first, intent); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	const reason = "A valid index matches the recorded definition. Its performance benefit remains unverified."
	result := DispatchResult{Outcome: "repair_interrupted", IndexRecovery: &ownedIndexRecovery{
		Status: "exact_index_present", Reason: reason, IndexOID: 91, IndexBytes: 16384, intent: raw}}
	assertState := func(outcome string, finished bool) {
		t.Helper()
		var gotOutcome string
		var gotFinished bool
		err := state.QueryRow(ctx, `SELECT a.outcome, j.finished_at IS NOT NULL
			FROM pooltune_worker.owned_index_actions a JOIN pooltune_worker.investigations j
			ON j.id=a.job_id AND j.project_id=a.project_id WHERE a.job_id=$1 AND a.project_id=$2`, first.ID, project).Scan(&gotOutcome, &gotFinished)
		if err != nil || gotOutcome != outcome || gotFinished != finished {
			t.Fatalf("unexpected persisted state: outcome=%q finished=%v error=%v", gotOutcome, gotFinished, err)
		}
	}
	exec("UPDATE pooltune_worker.investigations SET lease_until=clock_timestamp() WHERE id=$1", first.ID)
	if err := finishInvestigation(ctx, state, *first, result, epoch); !errors.Is(err, errClaimLost) {
		t.Fatalf("expired lease published recovery: %v", err)
	}
	assertState("pending", false)
	if err := state.Close(ctx); err != nil {
		t.Fatal(err)
	}
	state, err = pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)
	second, err := claimProjectInvestigation(ctx, state, project, &epoch, ownedIndexRecoveryScope{Database: 1, Role: 2})
	if err != nil || second == nil || second.ID != first.ID || second.Attempt != 2 || !second.RecoveryOnly {
		t.Fatalf("recorded action was not reclaimed: %+v %v", second, err)
	}
	if err := finishInvestigation(ctx, state, *first, result, epoch); !errors.Is(err, errClaimLost) {
		t.Fatalf("replaced claim published recovery: %v", err)
	}
	if err := finishInvestigation(ctx, state, *second, result, epoch-1); !errors.Is(err, errClaimLost) {
		t.Fatalf("stale permission epoch published recovery: %v", err)
	}
	for _, mismatch := range []json.RawMessage{nil, json.RawMessage(`{"RunID":"different"}`)} {
		wrong := *result.IndexRecovery
		wrong.intent = mismatch
		if err := finishInvestigation(ctx, state, *second, DispatchResult{Outcome: result.Outcome, IndexRecovery: &wrong}, epoch); err == nil {
			t.Fatal("missing or mismatched action published a recovery result")
		}
		assertState("pending", false)
	}
	active := *result.IndexRecovery
	active.Status = "action_active"
	if err := finishInvestigation(ctx, state, *second, DispatchResult{Outcome: result.Outcome, IndexRecovery: &active}, epoch); !errors.Is(err, errOwnedIndexActionActive) {
		t.Fatalf("active action published as reconciled: %v", err)
	}
	assertState("pending", false)
	if err := finishInvestigation(ctx, state, *second, result, epoch); err != nil {
		t.Fatal(err)
	}
	assertState("exact_index_present", true)
	tx, err := state.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	jobs, err := readProjectJobs(ctx, tx, project, time.Now().Add(-time.Minute))
	if err != nil || len(jobs) != 1 || jobs[0].Outcome != "repair_interrupted" || jobs[0].Reason != reason ||
		jobs[0].Repair == nil || jobs[0].Repair.Action != "interrupted" || jobs[0].Repair.RecoveryStatus != "exact_index_present" ||
		len(jobs[0].Repair.Metrics) != 0 {
		t.Fatalf("project activity lost recovery state or credited a measurement: %+v %v", jobs, err)
	}
}

func TestInspectOwnedIndexIntentDoesNotRepeatInterruptedAction(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	exec := func(sql string) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	const schema = "pt_hosted_ix_0123456789abcdef"
	const project = "supabase/osdjqwpzgyzwfaxyufze/0123456789abcdef"
	exec(`CREATE ROLE pt_hosted_ix_0123456789abcdef LOGIN PASSWORD 'test-owned-index-recovery';
		CREATE SCHEMA pt_hosted_ix_0123456789abcdef AUTHORIZATION pt_hosted_ix_0123456789abcdef;
		COMMENT ON SCHEMA pt_hosted_ix_0123456789abcdef IS 'Pooltune hosted index fixture supabase/osdjqwpzgyzwfaxyufze/0123456789abcdef';
		CREATE TABLE pt_hosted_ix_0123456789abcdef.workload (id int PRIMARY KEY, account int, amount int);
		INSERT INTO pt_hosted_ix_0123456789abcdef.workload SELECT n, n, n%2 FROM generate_series(1, 32) n;
		ALTER TABLE pt_hosted_ix_0123456789abcdef.workload OWNER TO pt_hosted_ix_0123456789abcdef;`)
	target := ProjectIndexTarget{Schema: schema, Relation: "workload"}
	var ownerOID uint32
	var size int64
	if err := admin.QueryRow(ctx, `SELECT d.oid, c.oid, c.relowner, pg_total_relation_size(c.oid)
		FROM pg_database d, pg_class c WHERE d.datname=current_database()
		AND c.oid='pt_hosted_ix_0123456789abcdef.workload'::regclass`).Scan(&target.DatabaseOID, &target.TableOID, &ownerOID, &size); err != nil {
		t.Fatal(err)
	}
	indexName := fmt.Sprintf("pooltune_ix_%d_2_3", target.TableOID)
	index := qualified(schema, indexName)
	intent := ProjectIndexActionIntent{Target: target, RunID: "owned-index-13-1", IndexName: indexName,
		Keys: []ProjectIndexKey{{Number: 2, Name: "account"}, {Number: 3, Name: "amount"}}, RelationBytes: size}
	ownerConfig := config.Copy()
	ownerConfig.User = schema
	ownerConfig.Password = "test-owned-index-recovery"
	owner, err := pgx.ConnectConfig(ctx, ownerConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close(ctx)
	inspect := func(connection *pgx.Conn, p string, wanted ProjectIndexActionIntent, status string) ownedIndexRecovery {
		t.Helper()
		result, err := inspectOwnedIndexIntent(ctx, connection, p, target, ownerOID, []int16{2, 3}, wanted)
		if err != nil || result.Status != status || result.Reason == "" {
			t.Fatalf("recovery=%+v, error=%v, want %s", result, err, status)
		}
		if status != "exact_index_present" && (result.IndexOID != 0 || result.IndexBytes != 0) {
			t.Fatalf("recovery credited an index while checking %s: %+v", status, result)
		}
		return result
	}
	inspect(owner, project, intent, "index_absent")
	inspect(owner, project, intent, "index_absent")

	var shared bool
	if err := admin.QueryRow(ctx, "SELECT pg_try_advisory_lock($1,$2)", projectIndexLockNamespace, int32(target.TableOID)).Scan(&shared); err != nil || !shared {
		t.Fatalf("blocker could not claim repair lock: %v", err)
	}
	inspect(owner, project, intent, "action_active")
	if err := admin.QueryRow(ctx, "SELECT pg_advisory_unlock($1,$2)", projectIndexLockNamespace, int32(target.TableOID)).Scan(&shared); err != nil || !shared {
		t.Fatalf("blocker could not release repair lock: %v", err)
	}
	inspect(owner, project, intent, "index_absent")

	exec("CREATE INDEX existing_app_index ON " + qualified(schema, target.Relation) + " (account, amount, id)")
	inspect(owner, project, intent, "equivalent_index_present")
	exec("CREATE TABLE " + index + " (sentinel int)")
	inspect(owner, project, intent, "index_name_occupied")
	exec("DROP TABLE " + index)
	exec("CREATE INDEX " + pgx.Identifier{indexName}.Sanitize() + " ON " + qualified(schema, target.Relation) + " (amount, account)")
	inspect(owner, project, intent, "index_name_occupied")
	exec("DROP INDEX " + index)
	exec("DROP INDEX " + qualified(schema, "existing_app_index"))

	// Force a real failed concurrent build. Its catalog row is created before
	// the writer releases the transaction that the next phase must wait for.
	writer, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(ctx)
	if _, err := writer.Exec(ctx, "INSERT INTO "+qualified(schema, target.Relation)+" VALUES (33, 33, 0)"); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, "SET statement_timeout='500ms'"); err != nil {
		t.Fatal(err)
	}
	ddl := "CREATE INDEX " + pgx.Identifier{indexName}.Sanitize() + " ON " + qualified(schema, target.Relation) + " (account, amount)"
	_, err = owner.Exec(ctx, "CREATE INDEX CONCURRENTLY "+pgx.Identifier{indexName}.Sanitize()+" ON "+qualified(schema, target.Relation)+" (account, amount)")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "57014" {
		t.Fatalf("index attempt did not reach timeout: %v", err)
	}
	if err := writer.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	inspect(owner, project, intent, "index_invalid")
	inspect(owner, project, intent, "index_invalid")
	exec("DROP INDEX " + index)

	exec(ddl)
	before := inspect(owner, project, intent, "exact_index_present")
	after := inspect(owner, project, intent, "exact_index_present")
	if before.IndexOID == 0 || before.IndexBytes == 0 || before.IndexOID != after.IndexOID || before.IndexBytes != after.IndexBytes {
		t.Fatalf("recovery changed or failed to identify the catalog entry: %+v, %+v", before, after)
	}
	var actualOID uint32
	if err := admin.QueryRow(ctx, "SELECT to_regclass($1)::oid", index).Scan(&actualOID); err != nil || actualOID != after.IndexOID {
		t.Fatalf("catalog index changed or disappeared: %d %v", actualOID, err)
	}

	for _, tc := range []struct {
		name   string
		change func(*ProjectIndexActionIntent)
	}{
		{"foreign table", func(i *ProjectIndexActionIntent) { i.Target.TableOID++ }},
		{"different database", func(i *ProjectIndexActionIntent) { i.Target.DatabaseOID++ }},
		{"swapped keys", func(i *ProjectIndexActionIntent) {
			i.Keys[0], i.Keys[1] = i.Keys[1], i.Keys[0]
			i.IndexName = fmt.Sprintf("pooltune_ix_%d_3_2", target.TableOID)
		}},
		{"renamed key", func(i *ProjectIndexActionIntent) { i.Keys[0].Name = "renamed" }},
		{"foreign name", func(i *ProjectIndexActionIntent) { i.IndexName = "existing_app_index" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := intent
			changed.Keys = append([]ProjectIndexKey(nil), intent.Keys...)
			tc.change(&changed)
			inspect(owner, project, changed, "target_changed")
		})
	}
	inspect(admin, project, intent, "target_changed")
	inspect(owner, "supabase/osdjqwpzgyzwfaxyufze/fedcba9876543210", intent, "target_changed")
	exec("COMMENT ON SCHEMA " + pgx.Identifier{schema}.Sanitize() + " IS 'another owner'")
	inspect(owner, project, intent, "target_changed")
}
