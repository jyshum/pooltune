//go:build integration

package hostedquery

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
)

func TestOwnedCronRuntimePinsDefinitionAndPrincipal(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	operator, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer operator.Close(ctx)
	const installation = "0123456789abcdef"
	const role = "pt_hosted_cr_" + installation
	const foreign = "pt_hosted_cr_foreign"
	const schema = "pt_cron_guard_" + installation
	const project = "supabase/" + OwnedIndexRookeryRef + "/" + installation
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	if _, err := operator.Exec(ctx, `CREATE ROLE `+role+` LOGIN PASSWORD 'test-password'; CREATE ROLE `+foreign+` LOGIN`); err != nil {
		t.Fatal(err)
	}
	sql, err := OwnedCronProviderSQL(installation, expires)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Exec(ctx, sql); err != nil {
		t.Fatal(err)
	}
	req := OwnedCronRequest{Target: ProjectCronTarget{Project: project}, Expires: expires}
	req.Runtime.Schema = schema
	if err := operator.QueryRow(ctx, `SELECT current_user::regrole::oid,p.oid,
		encode(sha256(convert_to(pg_get_functiondef(p.oid),'UTF8')),'hex') FROM pg_proc p
		WHERE p.oid=to_regprocedure($1)`, schema+".scheduler_status()").
		Scan(&req.Runtime.OwnerOID, &req.Runtime.FunctionOID, &req.Runtime.DefinitionDigest); err != nil {
		t.Fatal(err)
	}
	readerConfig := config.Copy()
	readerConfig.User = role
	readerConfig.Password = "test-password"
	reader, err := pgx.ConnectConfig(ctx, readerConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(ctx)
	if err := checkOwnedCronRuntime(ctx, reader, req); err != nil {
		t.Fatalf("exact helper identity refused: %v", err)
	}
	if _, _, err := readOwnedCronRuntime(ctx, reader, req); err == nil {
		t.Fatal("a database with no real Cron scheduler passed readiness")
	}
	for _, permission := range []string{
		`GRANT CREATE ON SCHEMA ` + schema + ` TO ` + role,
		`GRANT EXECUTE ON FUNCTION ` + schema + `.scheduler_status() TO ` + foreign,
	} {
		if _, err := operator.Exec(ctx, permission); err != nil {
			t.Fatal(err)
		}
		if err := checkOwnedCronRuntime(ctx, reader, req); err == nil {
			t.Fatalf("broader helper authority was accepted after %s", permission)
		}
		if _, err := operator.Exec(ctx, `REVOKE CREATE ON SCHEMA `+schema+` FROM `+role+`; REVOKE EXECUTE ON FUNCTION `+schema+`.scheduler_status() FROM `+foreign); err != nil {
			t.Fatal(err)
		}
	}
	wrong := req
	wrong.Runtime.FunctionOID++
	if err := checkOwnedCronRuntime(ctx, reader, wrong); err == nil {
		t.Fatal("a different routine OID passed as the installed helper")
	}
	wrong = req
	wrong.Expires = expires.Add(time.Second)
	if err := checkOwnedCronRuntime(ctx, reader, wrong); err == nil {
		t.Fatal("adjusting the configured lifetime bypassed the pinned SQL body")
	}
}
