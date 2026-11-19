//go:build integration

package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"pooltune/internal/testpg"
)

func TestCustomerIndexRevertRejectsDriftAndRecoversCommittedDrop(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t)
	db, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	pc, err := pgxpool.ParseConfig(cfg.ConnString())
	if err != nil {
		t.Fatal(err)
	}
	pc.ConnConfig = cfg
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := PrepareWorkerState(ctx, db); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	const project = "owner-1/installation"
	exec("INSERT INTO pooltune_worker.projects(project_id,monitor_epoch) VALUES($1,3),('other',3)", project)
	exec("CREATE SCHEMA billing; CREATE TABLE billing.orders(account integer); INSERT INTO billing.orders SELECT generate_series(1,1000); SET search_path=pg_catalog")
	target := ProjectIndexTarget{Schema: "billing", Relation: "orders"}
	if err := db.QueryRow(ctx, "SELECT oid,'billing.orders'::regclass::oid FROM pg_database WHERE datname=current_database()").Scan(&target.DatabaseOID, &target.TableOID); err != nil {
		t.Fatal(err)
	}
	var job int64
	if err := db.QueryRow(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence,attempts,finished_at) VALUES($1,$2,2,3,'{}',1,now()) RETURNING id`, project, target.DatabaseOID).Scan(&job); err != nil {
		t.Fatal(err)
	}
	intent := ProjectIndexActionIntent{RunID: fmt.Sprintf("managed-index-%d-1", job), Target: target, IndexName: fmt.Sprintf("pooltune_ix_%d_1", target.TableOID), Keys: []ProjectIndexKey{{Number: 1, Name: "account"}}, RelationBytes: 10000}
	index := qualified("billing", intent.IndexName)
	create := "CREATE INDEX " + pgx.Identifier{intent.IndexName}.Sanitize() + " ON billing.orders(account)"
	exec(create)
	report := IndexTrialReport{RunID: intent.RunID, JobID: job, Outcome: "improved_tested_inputs", Measurement: "managed_sampled_input", IndexValid: true}
	if err := db.QueryRow(ctx, "SELECT to_regclass($1)::oid", index).Scan(&report.CreatedIndexOID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, managedIndexVersionSQL, report.CreatedIndexOID).Scan(&report.CreatedIndexVersion); err != nil {
		t.Fatal(err)
	}
	asJSON := func(v any) string {
		t.Helper()
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	save := func(r IndexTrialReport) {
		exec(`UPDATE pooltune_worker.investigations SET result=jsonb_build_object('outcome','improved_tested_inputs','repair',$2::jsonb) WHERE id=$1`, job, asJSON(r))
	}
	exec(`INSERT INTO pooltune_worker.owned_index_actions(job_id,project_id,intent,outcome)VALUES($1,$2,$3,'improved_tested_inputs')`, job, project, asJSON(intent))
	calls := 0
	revert := func(p string, epoch int64, lostResponse bool) error {
		return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			err := RevertManagedProjectIndex(ctx, tx, pool, p, epoch, job, func() (context.Context, *pgx.Conn, error) {
				calls++
				conn, err := pgx.ConnectConfig(ctx, cfg)
				return ctx, conn, err
			})
			if lostResponse && err == nil {
				return errors.New("interrupted before application commit")
			}
			return err
		})
	}
	read := func() ProjectJob {
		t.Helper()
		activity, err := ReadProjectActivity(ctx, pool, project, time.Now().Add(-time.Hour))
		if err != nil || len(activity.Jobs) != 1 {
			t.Fatalf("activity %+v %v", activity, err)
		}
		return activity.Jobs[0]
	}
	legacy := report
	legacy.CreatedIndexVersion = ""
	save(legacy)
	if read().CanRevert || revert(project, 3, false) == nil || calls != 0 {
		t.Fatal("legacy change admitted without object receipt")
	}
	save(report)
	if !read().CanRevert {
		t.Fatal("valid change missing from activity")
	}
	if revert("other", 3, false) == nil || revert(project, 4, false) == nil || calls != 0 {
		t.Fatal("wrong project or stale epoch reached target")
	}
	exec("UPDATE pooltune_worker.projects SET last_disconnected_at=statement_timestamp() WHERE project_id=$1", project)
	if read().CanRevert || revert(project, 3, false) == nil || calls != 0 {
		t.Fatal("a past installation's change reached the customer database")
	}
	exec("UPDATE pooltune_worker.projects SET last_disconnected_at=NULL WHERE project_id=$1", project)
	held, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := held.Exec(ctx, "SELECT project_id FROM pooltune_worker.projects WHERE project_id=$1 FOR UPDATE", project); err != nil {
		t.Fatal(err)
	}
	if err := revert(project, 3, false); err == nil || calls != 0 {
		t.Fatal("a concurrent operation reached the customer database")
	}
	if err := held.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	exec("ALTER INDEX " + index + " SET (fillfactor=80)")
	if err := revert(project, 3, false); !errors.Is(err, ErrProjectIndexTargetChanged) {
		t.Fatalf("index alteration wasn't rejected: %v", err)
	}
	exec("ALTER INDEX " + index + " RESET (fillfactor)")
	blockedJournal, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blockedJournal.Exec(ctx, "SELECT job_id FROM pooltune_worker.owned_index_actions WHERE job_id=$1 FOR UPDATE", job); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := revert(project, 3, false); err == nil || time.Since(started) > 4*time.Second {
		t.Fatalf("journal contention exceeded native lock budget or committed: %v, %s", err, time.Since(started))
	}
	if err := blockedJournal.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	released, cancel := context.WithTimeout(ctx, time.Second)
	var kept bool
	err = db.QueryRow(released, "SELECT to_regclass($1) IS NOT NULL AND EXISTS(SELECT FROM billing.orders)", index).Scan(&kept)
	cancel()
	if err != nil || !kept {
		t.Fatalf("blocked receipt left the table locked or removed its index: %v", err)
	}
	exec("UPDATE pooltune_worker.projects SET agent_paused=true,monitor_epoch=-3 WHERE project_id=$1", project)
	if err := revert(project, 3, true); err == nil || err.Error() != "interrupted before application commit" {
		t.Fatalf("expected lost acknowledgement, got %v", err)
	}
	var removed bool
	if err := db.QueryRow(ctx, "SELECT to_regclass($1) IS NULL", index).Scan(&removed); err != nil || !removed {
		t.Fatalf("native revert did not commit: %v", err)
	}
	exec(create)
	if err := revert(project, 3, false); err != nil {
		t.Fatal(err)
	}
	reconciledCalls := calls
	if err := revert(project, 3, false); err != nil || calls != reconciledCalls {
		t.Fatalf("settled retry touched target: %v", err)
	}
	got := read()
	if got.CanRevert || !got.Reverted {
		t.Fatalf("incorrect status after revert: %+v", got)
	}
	if err := db.QueryRow(ctx, "SELECT to_regclass($1)::oid <> $2::oid", index, report.CreatedIndexOID).Scan(&removed); err != nil || !removed {
		t.Fatalf("replacement missing after retry: %v", err)
	}
}
