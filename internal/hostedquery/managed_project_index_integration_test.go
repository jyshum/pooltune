//go:build integration

package hostedquery

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
	"pooltune/internal/testpg"
)

// Route the canonical managed endpoint to an isolated TLS PostgreSQL server.
// The issued test certificate includes that endpoint, so verification stays on.
func managedLocalRoute(t *testing.T, ctx context.Context, db *pgx.Conn, config *pgx.ConnConfig, ca tls.Certificate, project supabaseproject.Project) *pgx.ConnConfig {
	t.Helper()
	host := "db." + project.Ref() + ".supabase.co"
	parent, err := x509.ParseCertificate(ca.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{host}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	key := ca.PrivateKey.(*rsa.PrivateKey)
	der, err := x509.CreateCertificate(rand.Reader, leaf, parent, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	var dir string
	if err := db.QueryRow(ctx, `SHOW data_directory`).Scan(&dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "server.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `SELECT pg_reload_conf()`); err != nil {
		t.Fatal(err)
	}
	route := config.Copy()
	route.Host = host
	route.Port = 5432
	route.TLSConfig = route.TLSConfig.Clone()
	route.TLSConfig.ServerName = host
	address := net.JoinHostPort(config.Host, fmt.Sprint(config.Port))
	route.LookupFunc = func(context.Context, string) ([]string, error) { return []string{"127.0.0.1"}, nil }
	route.DialFunc = func(ctx context.Context, network, addressIgnored string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	// Reload is asynchronous; check the exact verified route before the trial.
	deadline := time.Now().Add(time.Second)
	for {
		conn, err := pgx.ConnectConfig(ctx, route)
		if err == nil {
			conn.Close(ctx)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return route
}

func TestManagedIndexRunsOnDiscoveredOrdinaryTable(t *testing.T) {
	ctx, config, ca := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	db, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	exec := func(sql string) {
		t.Helper()
		if _, err := db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	project, _ := supabaseproject.New("abcdefghijklmnopqrst", "0123456789abcdef")
	exec(`CREATE SCHEMA extensions;CREATE EXTENSION pg_stat_statements WITH SCHEMA extensions;GRANT USAGE ON SCHEMA extensions TO PUBLIC`)
	exec(project.InstallSQL())
	exec(project.AutomaticDiscoverySQL())
	exec(project.DiscoveredIndexEvidenceSQL())
	marker := "Pooltune managed monitor v1 " + project.ID()
	exec(fmt.Sprintf(`ALTER ROLE %s LOGIN PASSWORD 'local-test'; COMMENT ON ROLE %s IS '%s';COMMENT ON SCHEMA %s IS '%s';CREATE SCHEMA billing;CREATE TABLE billing.orders(id integer,account integer,region integer);INSERT INTO billing.orders SELECT i,i,i%%10 FROM generate_series(1,30000)i;CREATE TABLE billing.health(id integer PRIMARY KEY,n integer);INSERT INTO billing.health VALUES(1,0);GRANT USAGE ON SCHEMA billing TO fixture_app;GRANT SELECT ON ALL TABLES IN SCHEMA billing TO fixture_app;GRANT UPDATE ON billing.health TO fixture_app;ANALYZE billing.orders`, project.Role(), project.Role(), marker, project.Role(), marker))
	monitorConfig := config.Copy()
	monitorConfig.User = project.Role()
	monitorConfig.Password = "local-test"
	monitorConfig.RuntimeParams["default_transaction_read_only"] = "on"
	monitorConfig.RuntimeParams["statement_timeout"] = "2s"
	monitor, err := pgx.ConnectConfig(ctx, monitorConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer monitor.Close(ctx)
	appConfig := config.Copy()
	appConfig.User = "fixture_app"
	app, err := pgx.ConnectConfig(ctx, appConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(ctx)
	const query = `SELECT account FROM billing.orders WHERE account=$1 AND region=$2`
	var value int
	if err := app.QueryRow(ctx, query, 7, 7).Scan(&value); err != nil {
		t.Fatal(err)
	}
	var role uint32
	var queryID int64
	if err := db.QueryRow(ctx, `SELECT userid,queryid FROM extensions.pg_stat_statements WHERE query=$1 AND userid='fixture_app'::regrole`, query).Scan(&role, &queryID); err != nil {
		t.Fatal(err)
	}
	observed, err := project.ReadDiscoveredIndexEvidence(ctx, monitor, role, queryID)
	if err != nil {
		t.Fatal(err)
	}
	exec("CREATE DATABASE pooltune_state")
	stateConfig := config.Copy()
	stateConfig.Database = "pooltune_state"
	state, err := pgx.ConnectConfig(ctx, stateConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	epoch, err := ActivateProjectMonitor(ctx, state, project, false)
	if err != nil {
		t.Fatal(err)
	}
	claim := investigationClaim{Project: project.ID(), Attempt: 1, InvestigationJob: InvestigationJob{queryKey: queryKey{Database: observed.DatabaseOID, Role: role, Query: queryID}, Generation: []time.Time{observed.Boot, observed.ResetAt, observed.EntrySince}}}
	if err := state.QueryRow(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence,attempts,lease_until)VALUES($1,$2,$3,$4,'{}',1,clock_timestamp()+interval '30 seconds')RETURNING id`, project.ID(), observed.DatabaseOID, role, queryID).Scan(&claim.ID); err != nil {
		t.Fatal(err)
	}
	route := managedLocalRoute(t, ctx, db, config, ca, project)
	exec("CREATE ROLE test_control LOGIN SUPERUSER PASSWORD 'test-control-password'")
	controlConfig := config.Copy()
	controlConfig.User = "test_control"
	controlConfig.Password = "test-control-password"
	control, err := pgx.ConnectConfig(ctx, controlConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close(ctx)
	if _, err := control.Exec(ctx, "ALTER ROLE postgres RENAME TO bootstrap_admin; CREATE ROLE postgres LOGIN PASSWORD '"+config.Password+"'; GRANT fixture_app,pg_read_all_stats TO postgres; ALTER TABLE billing.orders OWNER TO postgres; GRANT USAGE,CREATE ON SCHEMA billing TO postgres", pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatal(err)
	}
	trafficCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		for {
			if trafficCtx.Err() != nil {
				done <- nil
				return
			}
			var n int
			if err := app.QueryRow(trafficCtx, `SELECT n FROM billing.health WHERE id=$1`, 1).Scan(&n); err != nil {
				done <- err
				return
			}
			if _, err := app.Exec(trafficCtx, `UPDATE billing.health SET n=n+1 WHERE id=$1`, 1); err != nil {
				done <- err
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	defer func() { stop(); <-done }()
	calls := 0
	worker := &ManagedProjectIndex{Config: route, Capabilities: []string{"index"}, Investigate: func(ctx context.Context, e IndexEvidence) (IndexProposal, error) {
		calls++
		return ScriptedInvestigator(ctx, e)
	}, AdmitModel: func(context.Context) error { return nil }}
	result := DispatchResult{Outcome: "insufficient_evidence"}
	handled, err := worker.run(ctx, state, monitor, project, epoch, claim, &ObservedLookupLatency{ObjectiveMS: observed.ObjectiveMS}, &result)
	if err != nil || !handled || result.Repair == nil || !result.Repair.IndexValid || calls != 1 {
		t.Fatalf("managed flow failed: handled=%v result=%+v err=%v calls=%d", handled, result, err, calls)
	}
	if result.Repair.Measurement != "managed_sampled_input" || result.Repair.Before.ReadMS <= 0 || result.Repair.After.ReadMS <= 0 {
		t.Fatalf("missing actual sample proof: %+v", result.Repair)
	}
	if err := finishInvestigation(ctx, state, claim, result, epoch); err != nil {
		t.Fatal(err)
	}
	var outcome string
	if err := state.QueryRow(ctx, `SELECT outcome FROM pooltune_worker.owned_index_actions WHERE job_id=$1`, claim.ID).Scan(&outcome); err != nil || outcome == "pending" {
		t.Fatalf("unsettled action %q %v", outcome, err)
	}
}
