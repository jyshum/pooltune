//go:build integration

package hostedquery

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"pooltune/internal/labdb"
	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestDedicatedEvidenceLoginLocal(t *testing.T) {
	_, config, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	testDedicatedEvidenceLogin(t, config, false)
}

func TestDedicatedEvidenceLoginRookery(t *testing.T) {
	dsn := os.Getenv("POOLTUNE_EVIDENCE_ROOKERY_DSN")
	if dsn == "" {
		t.Skip("explicit Rookery credentials required")
	}
	config, err := labdb.Config(dsn, true)
	if err != nil {
		t.Fatal(err)
	}
	testDedicatedEvidenceLogin(t, config, true)
}

func testDedicatedEvidenceLogin(t *testing.T, config *pgx.ConnConfig, rookery bool) {
	t.Helper()
	ctx, stop := context.WithTimeout(context.Background(), 25*time.Second)
	defer stop()
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(labdb.Diagnostic("connect bootstrap", err))
	}
	defer labdb.Close(admin)
	if !rookery {
		if _, err := admin.Exec(ctx, "CREATE EXTENSION pg_stat_statements"); err != nil {
			t.Fatal(err)
		}
	}
	if err := trialCapacity(ctx, admin); err != nil {
		t.Fatal(err)
	}
	random := make([]byte, 56)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	id := "pt_login_" + hex.EncodeToString(random[:8])
	password := base64.RawURLEncoding.EncodeToString(random[8:40])
	verifier, err := fixtureSCRAMVerifier(password, random[40:])
	if err != nil {
		t.Fatal(err)
	}
	var statsSchema string
	err = admin.QueryRow(ctx, `
		SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace
		WHERE e.extname='pg_stat_statements'
	`).Scan(&statsSchema)
	if err != nil {
		t.Fatal(err)
	}
	role := pgx.Identifier{id}.Sanitize()
	table, unrelated := qualified(id, "lookup_test"), qualified(id, "unrelated")
	stats := pgx.Identifier{statsSchema}.Sanitize()
	database := pgx.Identifier{config.Database}.Sanitize()
	dedicated := config.Copy()
	dedicated.User, dedicated.Password = id, password
	if rookery {
		_, project, ok := strings.Cut(config.User, ".")
		if !ok {
			t.Fatal("missing Rookery pooler project suffix")
		}
		dedicated.User += "." + project
	}

	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		connection, err := pgx.ConnectConfig(cleanup, config)
		if err != nil {
			t.Errorf("cleanup %s: %v", id, labdb.Diagnostic("reconnect bootstrap", err))
			return
		}
		defer labdb.Close(connection)
		err = pgx.BeginFunc(cleanup, connection, func(tx pgx.Tx) error {
			var schemaExists, roleExists, owned bool
			if err := tx.QueryRow(cleanup, `
				SELECT to_regnamespace($1) IS NOT NULL,
				       EXISTS (SELECT FROM pg_roles WHERE rolname=$1),
				       EXISTS (SELECT FROM pg_namespace WHERE nspname=$1
				               AND nspowner=current_user::regrole::oid
				               AND obj_description(oid,'pg_namespace')=$2)
			`, id, "pooltune dedicated test "+id).Scan(&schemaExists, &roleExists, &owned); err != nil {
				return err
			}
			if !schemaExists && !roleExists {
				return nil
			}
			if !owned || !roleExists {
				return errors.New("dedicated fixture ownership did not match")
			}
			return runLoginStatements(cleanup, tx,
				"SET LOCAL lock_timeout='500ms'",
				"DROP TABLE "+table+", "+unrelated+" RESTRICT",
				"DROP SCHEMA "+role+" RESTRICT",
				"REVOKE USAGE ON SCHEMA "+stats+" FROM "+role,
				"REVOKE CONNECT ON DATABASE "+database+" FROM "+role,
				"DROP ROLE "+role,
			)
		})
		if err != nil {
			t.Errorf("cleanup %s: %v", id, labdb.Diagnostic("drop dedicated fixture", err))
			return
		}
		var remains bool
		err = connection.QueryRow(cleanup, "SELECT to_regnamespace($1) IS NOT NULL OR EXISTS (SELECT FROM pg_roles WHERE rolname=$1)", id).Scan(&remains)
		if err != nil || remains {
			t.Errorf("fixture %s remained after cleanup: %v, %v", id, remains, err)
		}
	}()

	if err := pgx.BeginFunc(ctx, admin, func(tx pgx.Tx) error {
		expiry := time.Now().UTC().Add(2 * time.Minute).Format(time.RFC3339)
		return runLoginStatements(ctx, tx,
			"SET LOCAL statement_timeout='5s'",
			"SET LOCAL lock_timeout='250ms'",
			"CREATE ROLE "+role+" LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS CONNECTION LIMIT 2 PASSWORD '"+verifier+"' VALID UNTIL '"+expiry+"'",
			"GRANT CONNECT ON DATABASE "+database+" TO "+role,
			"CREATE SCHEMA "+role,
			"COMMENT ON SCHEMA "+role+" IS 'pooltune dedicated test "+id+"'",
			"CREATE TABLE "+table+" (lookup integer NOT NULL)",
			"CREATE TABLE "+unrelated+" (lookup integer NOT NULL)",
			"INSERT INTO "+table+" SELECT n FROM generate_series(1,30000) n",
			"ANALYZE "+table,
			"GRANT USAGE ON SCHEMA "+role+", "+stats+" TO "+role,
			"GRANT SELECT ON "+table+" TO "+role,
		)
	}); err != nil {
		t.Fatal(labdb.Diagnostic("create dedicated fixture", err))
	}
	client, err := pgx.ConnectConfig(ctx, dedicated)
	if err != nil {
		t.Fatal(labdb.Diagnostic("connect dedicated login", err))
	}
	defer labdb.Close(client)
	var binding EnrolledLookup
	binding.Schema, binding.Table, binding.Column = id, "lookup_test", "lookup"
	var session, effective string
	err = client.QueryRow(ctx, `
		SELECT session_user,current_user,oid,current_user::regrole::oid,to_regclass($1)::oid
		FROM pg_database WHERE datname=current_database()
	`, table).Scan(&session, &effective, &binding.Database, &binding.Role, &binding.TableOID)
	if err != nil || session != id || effective != id {
		t.Fatalf("expected dedicated login identity: %v", err)
	}
	statement := "SELECT lookup FROM " + table + " WHERE lookup OPERATOR(pg_catalog.=) $1"
	var row int32
	if err := client.QueryRow(ctx, statement, int32(19)).Scan(&row); err != nil || row != 19 {
		t.Fatalf("synthetic lookup: %d, %v", row, err)
	}
	var generation [3]time.Time
	err = client.QueryRow(ctx, fmt.Sprintf(`
		SELECT queryid,pg_postmaster_start_time(),stats_reset,stats_since FROM %s CROSS JOIN %s
		WHERE userid=$1 AND toplevel AND query=$2
	`, qualified(statsSchema, "pg_stat_statements"), qualified(statsSchema, "pg_stat_statements_info")),
		binding.Role, statement).Scan(&binding.Query, &generation[0], &generation[1], &generation[2])
	if err != nil {
		t.Fatal(err)
	}
	collect := func() (*EstimatedLookupEvidence, error) {
		var evidence *EstimatedLookupEvidence
		err := pgx.BeginTxFunc(ctx, client, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
			var err error
			evidence, err = collectEnrolledLookup(ctx, tx, binding, generation, 19)
			return err
		})
		return evidence, err
	}
	var calls int64
	readCalls := func() {
		t.Helper()
		err := client.QueryRow(ctx, "SELECT calls FROM "+qualified(statsSchema, "pg_stat_statements")+"(false) WHERE userid=$1 AND queryid=$2 AND toplevel",
			binding.Role, binding.Query).Scan(&calls)
		if err != nil || calls != 1 {
			t.Fatalf("expected only the traffic driver's execution: calls=%d, %v", calls, err)
		}
	}
	readCalls()
	start := time.Now()
	evidence, err := collect()
	if err != nil || evidence == nil || evidence.Estimated.Node != "Seq Scan" || evidence.CoveringIndex {
		t.Fatalf("dedicated evidence = %+v, %v", evidence, err)
	}
	t.Logf("dedicated login collected estimate and index check in %.3f ms", elapsedMS(start))
	readCalls()
	for _, sql := range []string{"SELECT * FROM " + unrelated, "INSERT INTO " + table + " VALUES (0)"} {
		if _, err := client.Exec(ctx, sql); !isSQLState(err, "42501") {
			t.Fatalf("expected permission denial, got %v", err)
		}
	}
	if _, err := admin.Exec(ctx, "REVOKE SELECT ON "+table+" FROM "+role); err != nil {
		t.Fatal(err)
	}
	if evidence, err := collect(); err != nil || evidence != nil {
		t.Fatalf("revoked grant yielded evidence: %+v, %v", evidence, err)
	}
	if err := client.QueryRow(ctx, statement, int32(19)).Scan(&row); !isSQLState(err, "42501") {
		t.Fatalf("open session could still access the revoked table: %v", err)
	}
	if _, err := admin.Exec(ctx, "ALTER ROLE "+role+" NOLOGIN PASSWORD NULL"); err != nil {
		t.Fatal(err)
	}
	var probe int
	if err := client.QueryRow(ctx, "SELECT 1").Scan(&probe); err != nil || probe != 1 {
		t.Fatalf("expected NOLOGIN to leave the existing session open: %v", err)
	}
	retry, err := pgx.ConnectConfig(ctx, dedicated)
	if err == nil {
		defer labdb.Close(retry)
		t.Fatal("disabled login still authenticated on a new connection")
	}
	var denial *pgconn.PgError
	if !errors.As(err, &denial) {
		t.Fatal(labdb.Diagnostic("verify explicit login denial", err))
	}
	if denial.Code != "28000" && denial.Code != "28P01" &&
		!(rookery && isPoolerLoginDenial(denial)) {

		t.Fatal(labdb.Diagnostic("verify explicit login denial", err))
	}
	if isPoolerLoginDenial(denial) {
		t.Log("grant revocation blocked the open session's enrolled lookup; a fresh connection was rejected with Supavisor EAUTHQUERY")
	} else {
		t.Logf("grant revocation blocked the open session's enrolled lookup; a fresh connection was rejected with SQLSTATE %s", denial.Code)
	}
}

func runLoginStatements(ctx context.Context, tx pgx.Tx, statements ...string) error {
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// Send a SCRAM verifier in role DDL so a cleartext password cannot enter SQL logs.
func fixtureSCRAMVerifier(password string, salt []byte) (string, error) {
	const iterations = 4096
	salted, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	if err != nil {
		return "", err
	}
	key := func(label string) []byte {
		mac := hmac.New(sha256.New, salted)
		mac.Write([]byte(label))
		return mac.Sum(nil)
	}
	stored := sha256.Sum256(key("Client Key"))
	encode := base64.StdEncoding.EncodeToString
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s", iterations, encode(salt), encode(stored[:]), encode(key("Server Key"))), nil
}

func isSQLState(err error, state string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == state
}

func isPoolerLoginDenial(err *pgconn.PgError) bool {
	return err.Code == "XX000" && err.Message == "(EAUTHQUERY) user not found in the database"
}
