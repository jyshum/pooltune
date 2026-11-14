//go:build integration

package hostedquery

import (
	"errors"
	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
	"testing"
	"time"
)

func TestDelayedContinuationIsAtomicOwnedAndDoesNotBlockDueWork(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t)
	s, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	if err = PrepareWorkerState(ctx, s); err != nil {
		t.Fatal(err)
	}
	_, err = s.Exec(ctx, `INSERT INTO pooltune_worker.projects(project_id) VALUES('cron'); INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence) VALUES('cron',1,2,3,'{}'),('cron',1,2,4,'{}')`)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := claimInvestigation(ctx, s, "cron")
	if err != nil || parent == nil {
		t.Fatal(err)
	}
	child, err := continueInvestigation(ctx, s, *parent, DispatchResult{Outcome: "awaiting_evidence"}, time.Now().Add(time.Hour))
	if err != nil || child == 0 {
		t.Fatalf("handoff %d %v", child, err)
	}
	if _, err = continueInvestigation(ctx, s, *parent, DispatchResult{Outcome: "awaiting_evidence"}, time.Now()); !errors.Is(err, errClaimLost) {
		t.Fatalf("stale parent handed off twice: %v", err)
	}
	due, err := claimInvestigation(ctx, s, "cron")
	if err != nil || due == nil || due.Query != 4 {
		t.Fatalf("future child blocked due job: %+v %v", due, err)
	}
	if err = finishInvestigation(ctx, s, *due, DispatchResult{Outcome: "no_change"}); err != nil {
		t.Fatal(err)
	}
	s.Close(ctx)
	s, err = pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	if next, err := claimInvestigation(ctx, s, "cron"); err != nil || next != nil {
		t.Fatalf("restart claimed future child: %+v %v", next, err)
	}
	_, err = s.Exec(ctx, "UPDATE pooltune_worker.investigations SET not_before=now()-interval '1 second' WHERE id=$1", child)
	if err != nil {
		t.Fatal(err)
	}
	next, err := claimInvestigation(ctx, s, "cron")
	if err != nil || next == nil || next.ID != child || next.ParentID != parent.ID {
		t.Fatalf("lost continuation: %+v %v", next, err)
	}
}

func TestCronPendingActionRemainsDiscoverableAfterClaimExhaustion(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t)
	s, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	if err = PrepareWorkerState(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err = prepareJournal(ctx, s); err != nil {
		t.Fatal(err)
	}
	_, err = s.Exec(ctx, `INSERT INTO pooltune_worker.projects(project_id) VALUES('cron'); INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence,attempts,lease_until) VALUES('cron',1,2,3,'{}',2,now()-interval '1 second'); INSERT INTO pooltune_lab_state.index_runs(id,target,status,report) VALUES('000000000000000000000001','local','cron_pending','{"fixture":"000000000000000000000001","job_id":1}')`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = claimInvestigation(ctx, s, "cron"); err != nil {
		t.Fatal(err)
	}
	if err = ensureCronRecoveryClaim(ctx, s, "cron", "000000000000000000000001"); err != nil {
		t.Fatal(err)
	}
	if err = ensureCronRecoveryClaim(ctx, s, "cron", "000000000000000000000001"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = s.QueryRow(ctx, "SELECT count(*) FROM pooltune_worker.investigations WHERE finished_at IS NULL").Scan(&n); err != nil || n != 1 {
		t.Fatalf("reconciliation lost or duplicated: %d %v", n, err)
	}
	job, err := claimInvestigation(ctx, s, "cron")
	if err != nil || job == nil || job.ParentID != 1 {
		t.Fatalf("missing recovery claim %+v %v", job, err)
	}
}
