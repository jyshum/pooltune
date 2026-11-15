//go:build integration

package hostedquery

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"pooltune/internal/testpg"
)

func TestOwnedCronCounterViewUsesExistingPrivilegesOnly(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements", "-c", "compute_query_id=on")
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	const key = "0123456789abcdef"
	const role = "pt_hosted_cr_" + key
	const guard = "pt_cron_guard_" + key
	const outsider = "counter_outsider"
	const view = guard + ".statement_counters"
	const password = "disposable-cron-counter-only"
	if _, err := admin.Exec(ctx, `CREATE SCHEMA sealed_counters;
		CREATE EXTENSION pg_stat_statements WITH SCHEMA sealed_counters;
		CREATE ROLE `+role+` LOGIN PASSWORD '`+password+`'; CREATE ROLE `+outsider+` LOGIN PASSWORD '`+password+`';
		CREATE SCHEMA `+role+` AUTHORIZATION `+role+`;
		REVOKE ALL ON SCHEMA sealed_counters FROM PUBLIC;
		SET ROLE `+role+`;
		CREATE TABLE `+role+`.healthy(id integer PRIMARY KEY,padding text);
		INSERT INTO `+role+`.healthy VALUES(43,'a'),(90001,'a');
		CREATE FUNCTION `+role+`.pooltune_cron_read() RETURNS integer LANGUAGE plpgsql VOLATILE AS $$ BEGIN RETURN 43; END $$;
		RESET ROLE;`); err != nil {
		t.Fatal(err)
	}
	helperSQL, err := OwnedCronProviderSQL(key, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	viewSQL, err := OwnedCronCountersSQL(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, helperSQL+"\n"+viewSQL); err != nil {
		t.Fatal(err)
	}
	open := func(user string) *pgx.Conn {
		t.Helper()
		config := cfg.Copy()
		config.User, config.Password = user, password
		conn, err := pgx.ConnectConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close(ctx) })
		return conn
	}
	writer := open(role)
	foreign := open(outsider)
	if _, err := writer.Exec(ctx, ownedCronQuery(role)); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(ctx, ownedCronHealthyQuery(role), 43); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(ctx, ownedCronWriteQuery(role), 90001); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(ctx, `SELECT 99`); err != nil {
		t.Fatal(err)
	}
	if _, err := foreign.Exec(ctx, `SELECT 111`); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		conn *pgx.Conn
		name string
	}{
		{writer, "sealed_counters.pg_stat_statements"},
		{foreign, view},
	} {
		var denied *pgconn.PgError
		if _, err := test.conn.Exec(ctx, `SELECT count(*) FROM `+test.name); !errors.As(err, &denied) || denied.Code != "42501" {
			t.Fatalf("unexpected direct access to %s: %v", test.name, err)
		}
	}
	var rows int
	if err := writer.QueryRow(ctx, `SELECT count(DISTINCT lane) FROM `+view).Scan(&rows); err != nil || rows != 3 {
		t.Fatalf("invoker view did not show the three exact fixture lanes: %d %v", rows, err)
	}
	var total, foreignRows int
	if err := writer.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE userid<>current_user::regrole::oid) FROM `+view).
		Scan(&total, &foreignRows); err != nil || total != 3 || foreignRows != 0 {
		t.Fatalf("invoker view leaked non-fixture statistics: %d %d %v", total, foreignRows, err)
	}
	const project = "supabase/" + OwnedIndexRookeryRef + "/" + key
	req := OwnedCronRequest{Target: ProjectCronTarget{Project: project}, Expires: time.Now().Add(time.Hour), Runtime: OwnedCronRuntime{Schema: guard}}
	if err := admin.QueryRow(ctx, `SELECT c.oid,c.relowner,encode(sha256(convert_to(r.ev_action::text,'UTF8')),'hex'),
		p.oid,encode(sha256(convert_to(pg_get_functiondef(p.oid),'UTF8')),'hex')
		FROM pg_class c JOIN pg_rewrite r ON r.ev_class=c.oid AND r.rulename='_RETURN'
		JOIN pg_proc p ON p.oid=to_regprocedure($2) WHERE c.oid=to_regclass($1)`, view, guard+".scheduler_status()").
		Scan(&req.Runtime.CounterViewOID, &req.Runtime.OwnerOID, &req.Runtime.CounterViewDigest,
			&req.Runtime.FunctionOID, &req.Runtime.DefinitionDigest); err != nil {
		t.Fatal(err)
	}
	if err := checkOwnedCronCounterView(ctx, writer, req); err != nil {
		t.Fatalf("restricted caller could not validate its exact invoker view: %v", err)
	}
	var target Counter
	if err := writer.QueryRow(ctx, `SELECT dbid,userid,queryid,boot,reset_at,entry_since FROM `+view+` WHERE lane='target'`).
		Scan(&target.Database, &target.Role, &target.Query, &target.Boot, &target.Reset, &target.Entry); err != nil {
		t.Fatal(err)
	}
	if present, err := currentOwnedCronCounters(ctx, writer, req, "target", target); err != nil || !present {
		t.Fatalf("exact query generation did not match the owned target lane: %t %v", present, err)
	}
	if present, err := currentOwnedCronCounters(ctx, writer, req, "healthy", target); err != nil || present {
		t.Fatalf("target counter passed for a different lane: %t %v", present, err)
	}
	if _, err := admin.Exec(ctx, `ALTER VIEW `+view+` SET (security_invoker=false)`); err != nil {
		t.Fatal(err)
	}
	if err := checkOwnedCronCounterView(ctx, writer, req); err == nil {
		t.Fatal("an altered owner-executed view passed the scoped binding")
	}
	if _, err := admin.Exec(ctx, `ALTER VIEW `+view+` SET (security_invoker=true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `REVOKE SELECT ON sealed_counters.pg_stat_statements_info FROM PUBLIC`); err != nil {
		t.Fatal(err)
	}
	var denied *pgconn.PgError
	if _, err := writer.Exec(ctx, `SELECT count(*) FROM `+view); !errors.As(err, &denied) || denied.Code != "42501" {
		t.Fatalf("invoker view ignored underlying privileges: %v", err)
	}
}
