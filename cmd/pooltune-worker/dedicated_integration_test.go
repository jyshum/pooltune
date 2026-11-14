//go:build integration

package main

import (
	"testing"

	"pooltune/internal/hostedquery"
	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
)

func TestDedicatedDetectorMatchesAuthenticatedRole(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	target, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close(ctx)
	var role uint32
	if err := target.QueryRow(ctx, "SELECT session_user::regrole::oid").Scan(&role); err != nil {
		t.Fatal(err)
	}
	enrollment := hostedquery.Enrollment{Queries: []hostedquery.QueryObjective{{Role: role}, {Role: role}}}
	if err := verifyDedicatedDetector(ctx, target, enrollment); err != nil {
		t.Fatal(err)
	}
	enrollment.Queries[1].Role = role + 1
	if err := verifyDedicatedDetector(ctx, target, enrollment); err == nil {
		t.Fatal("accepted another role's statements")
	}
}
