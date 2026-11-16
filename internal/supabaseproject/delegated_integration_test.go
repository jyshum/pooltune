//go:build integration

package supabaseproject

import (
	"context"
	"encoding/pem"
	"github.com/jackc/pgx/v5"
	"net"
	"pooltune/internal/testpg"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestDelegatedConnectorIdentityRenewalAndRevocation(t *testing.T) {
	ctx, cfg, cert := testpg.Start(t)
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	p, _ := New("abcdefghijklmnopqrst", "0123456789abcdef")
	_, err = admin.Exec(ctx, `CREATE ROLE control LOGIN SUPERUSER PASSWORD 'control'; CREATE ROLE `+p.Role()+` LOGIN NOINHERIT CONNECTION LIMIT 2; CREATE SCHEMA `+p.Role()+`; COMMENT ON ROLE `+p.Role()+` IS '`+p.bootstrapMarker()+`'; COMMENT ON SCHEMA `+p.Role()+` IS '`+p.bootstrapMarker()+`'`, pgx.QueryExecModeSimpleProtocol)
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
	defer control.Close(context.Background())
	_, err = control.Exec(ctx, `ALTER ROLE postgres RENAME TO bootstrap_admin; CREATE ROLE postgres LOGIN; CREATE ROLE cli_login_postgres LOGIN NOINHERIT PASSWORD 'first' VALID UNTIL '2099-01-01'; GRANT postgres TO cli_login_postgres WITH SET TRUE;`, pgx.QueryExecModeSimpleProtocol)
	if err != nil {
		t.Fatal(err)
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	route, err := DirectConfig(p.Ref(), "cli_login_postgres", "placeholder", ca)
	if err != nil {
		t.Fatal(err)
	}
	route.TLSConfig = cfg.TLSConfig.Clone()
	route.TLSConfig.ServerName = "db." + p.Ref() + ".supabase.co"
	// The fixture certificate has only a loopback SAN. Keep real certificate verification against it in the test route.
	route.TLSConfig.ServerName = "127.0.0.1"
	route.LookupFunc = func(context.Context, string) ([]string, error) { return []string{cfg.Host}, nil }
	route.DialFunc = func(c context.Context, network, address string) (net.Conn, error) {
		return cfg.DialFunc(c, network, net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port))))
	}
	var issued atomic.Int32
	var rotated atomic.Bool
	get := func(c context.Context, stale string) (TemporaryLogin, error) {
		issued.Add(1)
		if stale != "" {
			rotated.Store(true)
		}
		password := "first"
		if rotated.Load() {
			password = "second"
		}
		return TemporaryLogin{Role: "cli_login_postgres", Password: password, ExpiresAt: time.Now().Add(time.Minute)}, nil
	}
	bound := withDelegatedTestRoute(ctx, p, get, route)
	db, err := ConnectRepair(bound, route)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	var current, session string
	if err = db.QueryRow(ctx, `SELECT current_user,session_user`).Scan(&current, &session); err != nil || current != "postgres" || session != "cli_login_postgres" {
		t.Fatalf("wrong identity %q %q %v", current, session, err)
	}
	if err = p.VerifyManagedRepair(ctx, db); err == nil {
		t.Fatal("delegated identity accepted without connector authority")
	}
	if err = p.VerifyManagedRepair(bound, db); err != nil {
		t.Fatal(err)
	}
	_, err = control.Exec(ctx, `ALTER ROLE cli_login_postgres PASSWORD 'second'`)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ConnectRepair(bound, db.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(context.Background())
	if issued.Load() < 2 {
		t.Fatal("secondary connection did not resolve credentials")
	}
	if _, err = control.Exec(ctx, `REVOKE postgres FROM cli_login_postgres`); err != nil {
		t.Fatal(err)
	}
	if err = p.VerifyManagedRepair(bound, db); err == nil {
		t.Fatal("revoked membership accepted on open connection")
	}
	if _, err = ConnectRepair(bound, route); err == nil {
		t.Fatal("revoked membership accepted on new connection")
	}

	for _, invalid := range []TemporaryLogin{
		{Role: "postgres", Password: "second", ExpiresAt: time.Now().Add(time.Minute)},
		{Role: "cli_login_attacker", Password: "second", ExpiresAt: time.Now().Add(time.Minute)},
		{Role: "cli_login_postgres", Password: "second", ExpiresAt: time.Now().Add(-time.Second)},
	} {
		bad := withDelegatedTestRoute(ctx, p, func(context.Context, string) (TemporaryLogin, error) { return invalid, nil }, route)
		if _, err := ConnectRepair(bad, route); err == nil {
			t.Fatal("invalid temporary credential admitted")
		}
	}
	if _, err = control.Exec(ctx, `GRANT postgres TO cli_login_postgres WITH SET TRUE;ALTER ROLE cli_login_postgres VALID UNTIL '2000-01-01'`, pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatal(err)
	}
	if err = p.VerifyManagedRepair(bound, db); err == nil {
		t.Fatal("expired login admitted on existing session")
	}
}

func withDelegatedTestRoute(ctx context.Context, p Project, get func(context.Context, string) (TemporaryLogin, error), route *pgx.ConnConfig) context.Context {
	return context.WithValue(ctx, delegatedContextKey{}, &delegatedConnector{project: p, credentials: get, config: route})
}
