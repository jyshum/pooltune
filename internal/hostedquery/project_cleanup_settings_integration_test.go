//go:build integration

package hostedquery

import (
	"context"
	"errors"
	"testing"
	"time"

	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
)

func TestProjectCleanupSettingsChangesAndRestoresOneOption(t *testing.T) {
	ctx, config, _ := testpg.Start(t, "-c", "autovacuum_naptime=1h")
	admin := openMonitorConn(t, ctx, config)
	ownerConfig := config.Copy()
	ownerConfig.User = "fixture_app"
	owner := openMonitorConn(t, ctx, ownerConfig)
	exec := func(db *pgx.Conn, sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(admin, `CREATE SCHEMA billing AUTHORIZATION fixture_app`)
	exec(owner, `CREATE TABLE billing."Live Orders" (id int PRIMARY KEY, total int) WITH
		(fillfactor=90, autovacuum_analyze_threshold=100000000, autovacuum_vacuum_insert_threshold=-1);
		CREATE TABLE billing."Explicit Orders" (id int PRIMARY KEY, total int) WITH
		(fillfactor=90, autovacuum_analyze_threshold=100000000, autovacuum_vacuum_insert_threshold=-1, autovacuum_vacuum_scale_factor='0.2000');
		CREATE TABLE billing.unrelated (id int) WITH (autovacuum_vacuum_scale_factor=0.48);
		CREATE TABLE billing.journal (run_id text, before_option text, after_option text, restoring bool);
		INSERT INTO billing."Live Orders" SELECT n, 0 FROM generate_series(1,4000) n;
		INSERT INTO billing."Explicit Orders" SELECT n, 0 FROM generate_series(1,4000) n;
		ANALYZE billing."Live Orders", billing."Explicit Orders"`)
	readTarget := func(name string) ProjectIndexTarget {
		t.Helper()
		target := ProjectIndexTarget{Schema: "billing", Relation: name}
		if err := admin.QueryRow(ctx, `SELECT oid, to_regclass($1)::oid FROM pg_database WHERE datname=current_database()`, qualified(target.Schema, name)).Scan(&target.DatabaseOID, &target.TableOID); err != nil {
			t.Fatal(err)
		}
		return target
	}
	target := readTarget("Live Orders")
	read := func() ProjectCleanupSettingsObservation {
		t.Helper()
		obs, err := ReadProjectCleanupSettings(ctx, owner, target)
		if err != nil {
			t.Fatal(err)
		}
		return obs
	}
	update := func(first, last int) {
		t.Helper()
		exec(owner, `UPDATE `+qualified(target.Schema, target.Relation)+` SET total=total+1 WHERE id BETWEEN $1 AND $2`, first, last)
		exec(owner, `SELECT pg_stat_force_next_flush()`)
	}
	option := func(target ProjectIndexTarget, name string) string {
		t.Helper()
		var value string
		err := admin.QueryRow(ctx, `SELECT COALESCE((SELECT option_value FROM pg_options_to_table(reloptions) WHERE option_name=$2),'')
			FROM pg_class WHERE oid=$1`, target.TableOID, name).Scan(&value)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	const setting = "autovacuum_vacuum_scale_factor"
	denied := errors.New("denied by test")
	guards := ProjectCleanupSettingsGuards{
		CheckPermission: func(_ context.Context, intent ProjectCleanupSettingsIntent) error {
			if intent.Target != target {
				return denied
			}
			return nil
		},
		CheckCapacity: func(_ context.Context, relationBytes int64, _ ProjectCleanupSettingsIntent) error {
			if relationBytes <= 0 {
				return denied
			}
			return nil
		},
		RecordIntent: func(ctx context.Context, i ProjectCleanupSettingsIntent) error {
			if option(target, setting) != i.BeforeOption {
				return errors.New("DDL preceded durable intent")
			}
			_, err := owner.Exec(ctx, `INSERT INTO billing.journal VALUES ($1,$2,$3,$4)`, i.RunID, i.BeforeOption, i.AfterOption, i.Restoring)
			return err
		},
	}
	update(1, 700)
	request := ProjectCleanupSettingsRequest{RunID: "inherited", Earlier: read()}
	time.Sleep(1050 * time.Millisecond)
	update(701, 740)
	request.Later = read()
	for _, failure := range []func(*ProjectCleanupSettingsGuards){
		func(g *ProjectCleanupSettingsGuards) {
			g.CheckPermission = func(context.Context, ProjectCleanupSettingsIntent) error { return denied }
		},
		func(g *ProjectCleanupSettingsGuards) {
			g.CheckCapacity = func(context.Context, int64, ProjectCleanupSettingsIntent) error { return denied }
		},
		func(g *ProjectCleanupSettingsGuards) {
			g.RecordIntent = func(context.Context, ProjectCleanupSettingsIntent) error { return denied }
		},
	} {
		g := guards
		failure(&g)
		if result, err := ApplyProjectCleanupSettings(ctx, ownerConfig, request, g); !errors.Is(err, denied) || result.WriteAttempted {
			t.Fatalf("denied setting change reached DDL: %+v %v", result, err)
		}
	}
	if result, err := ApplyProjectCleanupSettings(ctx, config, request, guards); err == nil || result.WriteAttempted {
		t.Fatalf("wrong native owner reached DDL: %+v %v", result, err)
	}
	wrong := request
	wrong.Earlier.Target.Relation = "unrelated"
	wrong.Later.Target = wrong.Earlier.Target
	if result, err := ApplyProjectCleanupSettings(ctx, ownerConfig, wrong, guards); err == nil || result.WriteAttempted {
		t.Fatalf("misbound catalog identity reached DDL: %+v %v", result, err)
	}
	stale := request
	stale.Earlier.At = stale.Earlier.At.Add(-time.Minute)
	stale.Later.At = stale.Later.At.Add(-time.Minute)
	if result, err := ApplyProjectCleanupSettings(ctx, ownerConfig, stale, guards); err == nil || result.WriteAttempted {
		t.Fatalf("stale backlog reached DDL: %+v %v", result, err)
	}
	if found := option(target, setting); found != "" {
		t.Fatalf("denied cleanup changed option to %q", found)
	}
	applied, err := ApplyProjectCleanupSettings(ctx, ownerConfig, request, guards)
	if err != nil || !applied.Confirmed || !applied.WriteAttempted || applied.Intent.BeforeOption != "" || applied.Intent.AfterOption != "0.15" || option(target, setting) != "0.15" {
		t.Fatalf("unexpected cleanup result: %+v %v", applied, err)
	}
	exec(owner, `ALTER TABLE billing."Live Orders" SET (fillfactor=82)`)
	restored, err := RestoreProjectCleanupSettings(ctx, ownerConfig, applied.Intent, guards)
	if err != nil || !restored.Confirmed || option(target, setting) != "" || option(target, "fillfactor") != "82" || option(readTarget("unrelated"), setting) != "0.48" {
		t.Fatalf("restore did not preserve inherited default and other settings: %+v %v", restored, err)
	}
	target = readTarget("Explicit Orders")
	update(1, 700)
	request = ProjectCleanupSettingsRequest{RunID: "explicit", Earlier: read()}
	time.Sleep(1050 * time.Millisecond)
	update(701, 740)
	request.Later = read()
	applied, err = ApplyProjectCleanupSettings(ctx, ownerConfig, request, guards)
	if err != nil || !applied.Confirmed || applied.Intent.BeforeOption != "0.2000" {
		t.Fatalf("explicit setting state was lost: earlier=%+v later=%+v result=%+v err=%v", request.Earlier, request.Later, applied, err)
	}
	exec(owner, `ALTER TABLE billing."Explicit Orders" SET (autovacuum_vacuum_scale_factor=0.17)`)
	if result, err := RestoreProjectCleanupSettings(ctx, ownerConfig, applied.Intent, guards); !errors.Is(err, ErrProjectCleanupSettingsChanged) || result.WriteAttempted || option(target, setting) != "0.17" {
		t.Fatalf("stale restore overwrote an independent change: %+v %v", result, err)
	}
	exec(owner, `ALTER TABLE billing."Explicit Orders" SET (autovacuum_vacuum_scale_factor=0.15)`)
	restored, err = RestoreProjectCleanupSettings(ctx, ownerConfig, applied.Intent, guards)
	if err != nil || !restored.Confirmed || option(target, setting) != "0.2000" {
		t.Fatalf("explicit previous option was not restored exactly: %+v %v", restored, err)
	}
}
