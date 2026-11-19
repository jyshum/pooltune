//go:build integration

package hostedapp

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testFreeBudgets(t *testing.T, ctx context.Context, db *pgxpool.Pool, a *App, owner int64, project string, epoch int64) {
	t.Helper()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	reserve := func(id string, cost int64) (*int64, error) {
		var receipt *int64
		err := db.QueryRow(ctx, "SELECT pooltune_app.reserve_free_model($1,$2,$3)", id, epoch, cost).Scan(&receipt)
		return receipt, err
	}
	check := func(id string, cost int64, allowed bool) {
		t.Helper()
		r, err := reserve(id, cost)
		if err != nil || (r != nil) != allowed {
			t.Fatalf("reserve %d: allowed=%v, err=%v", cost, r != nil, err)
		}
	}
	// Simulate an upgrade from the daily counter on this disposable database.
	exec("DELETE FROM pooltune_app.free_plan_rollout")
	exec("INSERT INTO pooltune_app.model_budgets VALUES ($1,(now() AT TIME ZONE 'UTC')::date,3)", project)
	for range 2 {
		if err := a.projects.prepare(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var migratedCredits, migratedCost int64
	if err := db.QueryRow(ctx, "SELECT credits,account_micros FROM pooltune_app.free_plan_usage($1)", owner).Scan(&migratedCredits, &migratedCost); err != nil || migratedCredits != 3 || migratedCost != 0 {
		t.Fatalf("migrated credits=%d, cost=%d, err=%v", migratedCredits, migratedCost, err)
	}
	exec("DELETE FROM pooltune_app.free_model_usage WHERE owner_id=$1", owner)

	// A second project shares the owner's allowance. The encrypted credential is
	// never used by this test; only the active epoch is needed for admission.
	exec(`INSERT INTO pooltune_app.projects (id,ref,name,owner_id,epoch,secret)
 SELECT 'second', 'otherref','Other project',owner_id,epoch,secret FROM pooltune_app.projects WHERE id=$1`, project)
	// Last month’s credits expire, but both the account and the pool retain its spend.
	exec(`INSERT INTO pooltune_app.free_model_usage(owner_id,month,credits,micros,settled)
 VALUES ($1, date_trunc('month', now() AT TIME ZONE 'UTC')::date - interval '1 month',50,200000,true),
 (-1, current_date,1,44750000,true)`, owner)
	if err := a.projects.prepare(ctx); err != nil {
		t.Fatal(err)
	}
	check(project, 49950, true)
	check("second", 51, false)
	// Release unused dollars after a provider usage receipt, retaining the credit.
	exec("UPDATE pooltune_app.free_model_usage SET micros=49900, settled=true WHERE NOT settled")
	for range 48 {
		check(project, 1, true)
	}
	// 49 credits used. Concurrent reservations race for the final shared slot.
	type result struct {
		receipt *int64
		err     error
	}
	results := make(chan result, 8)
	for i := range 8 {
		id := project
		if i%2 != 0 {
			id = "second"
		}
		go func() { r, err := reserve(id, 52); results <- result{r, err} }()
	}
	accepted := 0
	for range 8 {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.receipt != nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d simultaneous final requests", accepted)
	}
	check("second", 1, false)
	// Free some money to isolate the credit limit.
	exec("UPDATE pooltune_app.free_model_usage SET micros=micros-1 WHERE micros=49900")
	check(project, 1, false)
	exec("UPDATE pooltune_app.free_model_usage SET micros=micros+1 WHERE micros=49899")
	// A different account with an empty current month still shares the full pool.
	exec(`INSERT INTO pooltune_app.projects(id,ref,name,owner_id,epoch,secret)
 SELECT 'other', 'thirdref','Other account',u.id,p.epoch,p.secret FROM pooltune_app.projects p
 JOIN pooltune_app.users u ON u.id<>p.owner_id WHERE p.id=$1 LIMIT 1`, project)
	check("other", 1, false)
	// Isolate the account dollar limit from the global limit.
	exec("DELETE FROM pooltune_app.free_model_usage WHERE id=(SELECT min(id) FROM pooltune_app.free_model_usage WHERE micros=1)")
	exec("UPDATE pooltune_app.free_model_usage SET micros=micros-100 WHERE micros=44750000")
	check("second", 2, false)
	check("second", 1, true)
	exec("UPDATE pooltune_app.free_model_usage SET micros=micros+100 WHERE micros=44749900")
}
