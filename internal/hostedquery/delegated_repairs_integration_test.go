//go:build integration

package hostedquery

import (
	"context"
	"encoding/pem"
	"errors"
	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
	"pooltune/internal/testpg"
	"sync/atomic"
	"testing"
	"time"
)

func TestDelegatedManagedRepairCatalogAndSecondaryIndexSessions(t *testing.T) {
	ctx, cfg, cert := testpg.Start(t)
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	project, _ := supabaseproject.New("abcdefghijklmnopqrst", "0123456789abcdef")
	marker := "Pooltune managed monitor v1 " + project.ID()
	_, err = admin.Exec(ctx, `CREATE ROLE control LOGIN SUPERUSER PASSWORD 'control';CREATE ROLE `+project.Role()+` LOGIN NOINHERIT CONNECTION LIMIT 2;CREATE SCHEMA `+project.Role()+`;COMMENT ON ROLE `+project.Role()+` IS '`+marker+`';COMMENT ON SCHEMA `+project.Role()+` IS '`+marker+`';`, pgx.QueryExecModeSimpleProtocol)
	if err != nil {
		t.Fatal(err)
	}
	controlCfg := cfg.Copy()
	controlCfg.User = "control"
	controlCfg.Password = "control"
	control, err := pgx.ConnectConfig(ctx, controlCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close(ctx)
	route := managedLocalRoute(t, ctx, control, controlCfg, cert, project)
	_, err = control.Exec(ctx, `ALTER ROLE postgres RENAME TO bootstrap_admin; CREATE ROLE postgres LOGIN; CREATE ROLE cli_login_postgres LOGIN NOINHERIT PASSWORD 'temporary' VALID UNTIL '2099-01-01';GRANT postgres TO cli_login_postgres WITH SET TRUE;CREATE SCHEMA delegated AUTHORIZATION postgres;SET ROLE postgres;CREATE TABLE delegated.items AS SELECT i AS id,i%1000 AS customer FROM generate_series(1,50000)i;CREATE FUNCTION delegated.work() RETURNS int LANGUAGE sql VOLATILE PARALLEL UNSAFE SET max_parallel_workers_per_gather=0 SET work_mem='1MB' AS $$SELECT 42$$;RESET ROLE;`, pgx.QueryExecModeSimpleProtocol)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	credentialTTL := time.Minute
	var denyCredentials atomic.Bool
	denied := errors.New("test authority revoked")
	bound, base, err := supabaseproject.WithDelegatedRepair(ctx, project, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), func(context.Context, string) (supabaseproject.TemporaryLogin, error) {
		if denyCredentials.Load() {
			return supabaseproject.TemporaryLogin{}, denied
		}
		calls++
		return supabaseproject.TemporaryLogin{Role: "cli_login_postgres", Password: "temporary", ExpiresAt: time.Now().Add(credentialTTL)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Use the real TLS fixture route with its canonical project hostname.
	*base = *route
	base.User = "cli_login_postgres"
	base.Password = "provider-resolved"
	db, err := supabaseproject.ConnectRepair(bound, base)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	target := ProjectIndexTarget{Schema: "delegated", Relation: "items"}
	if err = db.QueryRow(ctx, `SELECT (SELECT oid FROM pg_database WHERE datname=current_database()),'delegated.items'::regclass::oid`).Scan(&target.DatabaseOID, &target.TableOID); err != nil {
		t.Fatal(err)
	}
	scope := ProjectRepairAccessScope{Project: project.ID(), Capabilities: []string{"index", "analyze", "statistics", "vacuum"}, Table: &target}
	if _, err = ReadProjectRepairAccess(ctx, db, project, scope); err == nil {
		t.Fatal("unbound delegated catalog accepted")
	}
	report, err := ReadProjectRepairAccess(bound, db, project, scope)
	if err != nil {
		t.Fatal(err)
	}
	if report.Operations[0].Status != "granted" {
		t.Fatalf("native owner not recognized: %+v", report)
	}
	ft := ProjectFunctionMemoryTarget{Project: project.ID(), Schema: "delegated", Function: "work", DatabaseOID: target.DatabaseOID}
	if err = db.QueryRow(ctx, `SELECT 'delegated.work()'::regprocedure::oid`).Scan(&ft.FunctionOID); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadProjectFunctionMemory(bound, db, ft); err != nil {
		t.Fatal(err)
	}
	if _, _, err = readProjectMaintenanceTarget(bound, db, ProjectMaintenanceRequest{Target: target, Operation: "analyze", Columns: []int16{2}}); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin(bound)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readProjectStatisticsState(bound, tx, target, ProjectIndexKey{Number: 2, Name: "customer"}, false)
	_ = tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	evidence := IndexEvidence{RunID: "delegated-index", At: time.Now(), TableOID: target.TableOID, Column: 2, Decision: Investigate, Plan: PlanEvidence{Node: "Seq Scan", ExaminedRows: 50000, ReturnedRows: 1}}
	proposal, err := ScriptedInvestigator(bound, evidence)
	if err != nil {
		t.Fatal(err)
	}
	recorded := false
	guards := ProjectIndexActionGuards{CheckPermission: func(c context.Context, _ ProjectIndexActionIntent) error { return project.VerifyManagedRepair(c, db) }, CheckCapacity: func(context.Context, ProjectIndexActionIntent) error { return nil }, RecordIntent: func(context.Context, ProjectIndexActionIntent) error { recorded = true; return nil }}
	result, err := ApplyProjectIndex(bound, base, target, evidence, proposal, guards)
	if err != nil || !result.WriteAttempted || result.IndexOID == 0 || !recorded || calls < 3 {
		t.Fatalf("delegated native index/secondary pin: %+v %v resolves=%d", result, err, calls)
	}
	t.Run("cron replaces expiring owner sessions", func(t *testing.T) {
		if err := PrepareWorkerState(ctx, control); err != nil {
			t.Fatal(err)
		}
		if err := PrepareManagedCronState(ctx, control); err != nil {
			t.Fatal(err)
		}
		if _, err := control.Exec(ctx, `INSERT INTO pooltune_worker.projects(project_id,monitor_epoch) VALUES($1,1)`, project.ID()); err != nil {
			t.Fatal(err)
		}
		credentialTTL = 6 * time.Second
		defer func() { credentialTTL = time.Minute; denyCredentials.Store(false) }()
		cronConfig := base.Copy()
		cronConfig.RuntimeParams["application_name"] = "delegated-cron-renewal"
		collector := &ManagedProjectCron{Config: cronConfig}
		collectCtx, cancel := context.WithTimeout(bound, 9*time.Second)
		done := make(chan error, 1)
		finished := false
		go func() { done <- collector.Run(collectCtx, controlCfg, project, 1) }()
		defer func() {
			cancel()
			if finished {
				return
			}
			if err := <-done; !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("collector exited unexpectedly: %v", err)
			}
		}()
		var first uint32
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			var pid uint32
			err := control.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE application_name='delegated-cron-renewal' ORDER BY backend_start DESC LIMIT 1`).Scan(&pid)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				t.Fatal(err)
			}
			if pid != 0 {
				if first == 0 {
					first = pid
				} else if first != pid {
					denyCredentials.Store(true)
					err := <-done
					finished = true
					if !errors.Is(err, denied) {
						t.Fatalf("renewal failed to stop revoked authority: %v", err)
					}
					return
				}
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatal("cron retained one delegated owner connection beyond its temporary credential lifetime")
	})
	if _, err = control.Exec(ctx, `REVOKE postgres FROM cli_login_postgres`); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadProjectFunctionMemory(bound, db, ft); err == nil {
		t.Fatal("revoked effective role accepted by action catalog")
	}
}
