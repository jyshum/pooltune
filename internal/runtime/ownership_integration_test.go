//go:build integration

package runtime

import (
	"github.com/jackc/pgx/v5"
	"pooltune/internal/durable"
	"pooltune/internal/testpg"
	"sync"
	"testing"
)

func TestLocalDatabaseOwnershipConcurrentAcrossIndependentState(t *testing.T) {
	ctx, db, _ := testpg.Start(t)
	results := make(chan *durable.Files, 2)
	var start sync.WaitGroup
	start.Add(1)
	for i := 0; i < 2; i++ {
		go func() { start.Wait(); owner, _ := acquireLocalDatabaseOwner(ctx, db); results <- owner }()
	}
	start.Done()
	var winner *durable.Files
	successes := 0
	for i := 0; i < 2; i++ {
		if owner := <-results; owner != nil {
			successes++
			if winner != nil {
				owner.Close()
			} else {
				winner = owner
			}
		}
	}
	if winner != nil {
		defer winner.Close()
	}
	if successes != 1 {
		t.Fatalf("concurrent database owners: %d", successes)
	}
	if duplicate, e := acquireLocalDatabaseOwner(ctx, db.Copy()); e == nil {
		duplicate.Close()
		t.Fatal("independent state bypassed database ownership")
	}
	admin, e := pgx.ConnectConfig(ctx, db)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = admin.Exec(ctx, "CREATE DATABASE another_database"); e != nil {
		t.Fatal(e)
	}
	admin.Close(ctx)
	otherDatabase := db.Copy()
	otherDatabase.Database = "another_database"
	if other, e := acquireLocalDatabaseOwner(ctx, otherDatabase); e == nil {
		other.Close()
		t.Fatal("same physical PostgreSQL cluster acquired a second budget through another database")
	}
	winner.Close()
	replacement, e := acquireLocalDatabaseOwner(ctx, db)
	if e != nil {
		t.Fatal("released local ownership not reusable")
	}
	replacement.Close()
	admin, e = pgx.ConnectConfig(ctx, db)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = admin.Exec(ctx, "REVOKE EXECUTE ON FUNCTION pg_control_system() FROM PUBLIC"); e != nil {
		t.Fatal(e)
	}
	admin.Close(ctx)
	unprivileged := db.Copy()
	unprivileged.User = "fixture_app"
	if owner, e := acquireLocalDatabaseOwner(ctx, unprivileged); e == nil {
		owner.Close()
		t.Fatal("unverified cluster identity accepted")
	}
}
