//go:build integration

package hostedquery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"pooltune/internal/labdb"
	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestEnrolledEvidenceLocal(t *testing.T) {
	_, config, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	ctx, stop := context.WithTimeout(context.Background(), 15*time.Second)
	defer stop()
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer labdb.Close(conn)
	if _, err := conn.Exec(ctx, "CREATE EXTENSION pg_stat_statements"); err != nil {
		t.Fatal(err)
	}
	exerciseEnrolledEvidence(t, ctx, conn)
}

func TestEnrolledEvidenceRookery(t *testing.T) {
	spec := os.Getenv("POOLTUNE_EVIDENCE_ROOKERY_DSN")
	if spec == "" {
		t.Skip("explicit Rookery credentials required")
	}
	config, err := labdb.Config(spec, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 25*time.Second)
	defer stop()
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(labdb.Diagnostic("connect evidence test", err))
	}
	defer labdb.Close(conn)
	exerciseEnrolledEvidence(t, ctx, conn)
}

func exerciseEnrolledEvidence(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	if err := trialCapacity(ctx, conn); err != nil {
		t.Fatal(err)
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	id := "pt_evidence_" + hex.EncodeToString(nonce[:])
	name := pgx.Identifier{id}.Sanitize()
	table, unrelated := qualified(id, "lookup_test"), qualified(id, "unrelated")
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if err := tx.Rollback(cleanup); err != nil {
			t.Error(err)
			return
		}
		var remains bool
		if err := conn.QueryRow(cleanup, "SELECT to_regnamespace($1) IS NOT NULL OR EXISTS (SELECT FROM pg_roles WHERE rolname=$1)", id).Scan(&remains); err != nil || remains {
			t.Errorf("fixture or role remained: %v, %v", remains, err)
		}
	}()
	var stats string
	if err := tx.QueryRow(ctx, "SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='pg_stat_statements'").Scan(&stats); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"SET LOCAL lock_timeout='250ms'",
		"SET LOCAL statement_timeout='5s'",
		"SET LOCAL transaction_timeout='22s'",
		"CREATE ROLE " + name + " NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS",
		"GRANT " + name + " TO CURRENT_USER WITH SET TRUE",
		"CREATE SCHEMA " + name,
		"CREATE TABLE " + table + " (lookup integer NOT NULL)",
		"CREATE TABLE " + unrelated + " (lookup integer NOT NULL)",
		"INSERT INTO " + table + " SELECT n FROM generate_series(1,30000) n",
		"ANALYZE " + table,
		"GRANT USAGE ON SCHEMA " + name + " TO " + name,
		"GRANT SELECT ON " + table + " TO " + name,
		"GRANT USAGE ON SCHEMA " + pgx.Identifier{stats}.Sanitize() + " TO " + name,
		"SET LOCAL ROLE " + name,
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	var binding EnrolledLookup
	binding.Schema, binding.Table, binding.Column = id, "lookup_test", "lookup"
	if err := tx.QueryRow(ctx, "SELECT oid,current_user::regrole::oid,to_regclass($1)::oid FROM pg_database WHERE datname=current_database()", table).Scan(&binding.Database, &binding.Role, &binding.TableOID); err != nil {
		t.Fatal(err)
	}
	var input int32
	if err := tx.QueryRow(ctx, "SELECT lookup FROM "+table+" WHERE lookup OPERATOR(pg_catalog.=) $1", int32(19)).Scan(&input); err != nil || input != 19 {
		t.Fatalf("driver lookup: %d, %v", input, err)
	}
	var boot, reset, entry time.Time
	if err := tx.QueryRow(ctx, "SELECT queryid,pg_postmaster_start_time(),stats_reset,stats_since FROM "+qualified(stats, "pg_stat_statements")+" CROSS JOIN "+qualified(stats, "pg_stat_statements_info")+" WHERE userid=$1 AND toplevel AND query=$2", binding.Role, "SELECT lookup FROM "+table+" WHERE lookup OPERATOR(pg_catalog.=) $1").Scan(&binding.Query, &boot, &reset, &entry); err != nil {
		t.Fatal(err)
	}
	generation := [3]time.Time{boot, reset, entry}
	start := time.Now()
	first, err := collectEnrolledLookup(ctx, tx, binding, generation, 19)
	if err != nil || first == nil || first.CoveringIndex || first.Estimated.Node != "Seq Scan" || first.Estimated.OutputRows != 1 {
		t.Fatalf("missing unindexed evidence: %+v, %v", first, err)
	}
	t.Logf("restricted estimate and catalog check: %.3f ms", elapsedMS(start))
	var sawCalls int64
	if err := tx.QueryRow(ctx, "SELECT calls FROM "+qualified(stats, "pg_stat_statements")+"(false) WHERE userid=$1 AND queryid=$2 AND toplevel", binding.Role, binding.Query).Scan(&sawCalls); err != nil || sawCalls != 1 {
		t.Fatalf("lookup executed again: %d, %v", sawCalls, err)
	}
	stale := generation
	stale[2] = stale[2].Add(-time.Second)
	if result, err := collectEnrolledLookup(ctx, tx, binding, stale, 19); err != nil || result != nil {
		t.Fatalf("stale evidence: %+v, %v", result, err)
	}
	other := binding
	other.Table = "unrelated"
	if result, err := collectEnrolledLookup(ctx, tx, other, generation, 19); err != nil || result != nil {
		t.Fatalf("unenrolled evidence: %+v, %v", result, err)
	}
	wrongRole := binding
	wrongRole.Role++
	if result, err := collectEnrolledLookup(ctx, tx, wrongRole, generation, 19); err != nil || result != nil {
		t.Fatalf("wrong role yielded evidence: %+v, %v", result, err)
	}
	wrongQuery := binding
	wrongQuery.Query ^= 1
	if result, err := collectEnrolledLookup(ctx, tx, wrongQuery, generation, 19); err != nil || result != nil {
		t.Fatalf("wrong query yielded evidence: %+v, %v", result, err)
	}
	var canRead, canWrite bool
	if err := tx.QueryRow(ctx, "SELECT has_table_privilege($1::text,'SELECT'),has_table_privilege($2::text,'INSERT')", unrelated, table).Scan(&canRead, &canWrite); err != nil || canRead || canWrite {
		t.Fatalf("scope: read unrelated=%v, write fixture=%v, err=%v", canRead, canWrite, err)
	}
	for _, sql := range []string{"SELECT * FROM " + unrelated, "INSERT INTO " + table + " VALUES (0)"} {
		probe, err := tx.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, denied := probe.Exec(ctx, sql)
		if err := probe.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		var permission *pgconn.PgError
		if !errors.As(denied, &permission) || permission.Code != "42501" {
			t.Fatalf("expected fixture privilege denial, got %v", denied)
		}
	}
	if _, err := tx.Exec(ctx, "RESET ROLE; CREATE INDEX ON "+table+" (lookup) WHERE lookup>100; SET LOCAL ROLE "+name); err != nil {
		t.Fatal(err)
	}
	partial, err := collectEnrolledLookup(ctx, tx, binding, generation, 19)
	if err != nil || partial == nil || partial.CoveringIndex {
		t.Fatalf("partial index matched the full-index check: %+v, %v", partial, err)
	}
	if _, err := tx.Exec(ctx, "RESET ROLE; CREATE INDEX ON "+table+" (lookup); SET LOCAL ROLE "+name); err != nil {
		t.Fatal(err)
	}
	second, err := collectEnrolledLookup(ctx, tx, binding, generation, 19)
	if err != nil || second == nil || !second.CoveringIndex || second.Estimated.Node == "Seq Scan" {
		t.Fatalf("indexed evidence: %+v, %v", second, err)
	}
	if _, err := tx.Exec(ctx, "RESET ROLE; REVOKE SELECT ON "+table+" FROM "+name+"; SET LOCAL ROLE "+name); err != nil {
		t.Fatal(err)
	}
	if result, err := collectEnrolledLookup(ctx, tx, binding, generation, 19); err != nil || result != nil {
		t.Fatalf("revoked read: %+v, %v", result, err)
	}
}
