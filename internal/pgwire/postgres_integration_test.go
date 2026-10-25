//go:build integration

package pgwire

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgproto3"
	"net"
	"pooltune/internal/testpg"
	"strings"
	"testing"
	"time"
)

func realPostgres(t *testing.T) (context.Context, *pgx.ConnConfig, tls.Certificate) {
	return testpg.Start(t)
}

func TestPGTLSAndSCRAMWithRealPostgres(t *testing.T) {
	ctx, config, cert := realPostgres(t)
	addr := net.JoinHostPort(config.Host, fmt.Sprint(config.Port))
	s := testScheduler(t)
	srv, e := NewServer(TargetConfig{Target: "db", Database: "postgres", Upstream: addr, ServerTLS: &tls.Config{Certificates: []tls.Certificate{cert}}, UpstreamTLS: config.TLSConfig, MaxSessions: 4, MaxHandshakes: 4, MaxMessageBytes: 1 << 20, HandshakeTimeout: 3 * time.Second, IOTimeout: 10 * time.Second, Classify: testClassification}, s)
	if e != nil {
		t.Fatal(e)
	}
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	serverCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(serverCtx, l) }()
	defer func() { stop(); <-done }()
	clientConfig := config.Copy()
	clientConfig.User = "fixture_app"
	clientConfig.ChannelBinding = "disable"
	clientConfig.Host = "127.0.0.1"
	tcp := l.Addr().(*net.TCPAddr)
	clientConfig.Port = uint16(tcp.Port)
	c, e := pgx.ConnectConfig(ctx, clientConfig)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close(context.Background())
	var user string
	if e = c.QueryRow(ctx, "select current_user").Scan(&user); e != nil || user != "fixture_app" {
		t.Fatalf("identity: %q %v", user, e)
	}
	if _, e = c.Exec(ctx, "BEGIN"); e != nil {
		t.Fatal(e)
	}
	if s.Stats().Active != 1 {
		t.Fatal("transaction permit not retained")
	}
	if _, e = c.Exec(ctx, "SELECT 1/0"); e == nil {
		t.Fatal("expected SQL failure")
	}
	if s.Stats().Active != 1 {
		t.Fatal("failed transaction permit lost")
	}
	if _, e = c.Exec(ctx, "ROLLBACK"); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = c.QueryRow(ctx, "select $1::int + 1", 41).Scan(&n); e != nil || n != 42 {
		t.Fatalf("unnamed extended query: %d %v", n, e)
	}
	if _, e = c.Exec(ctx, "SELECT 1; SELECT 2", pgx.QueryExecModeSimpleProtocol); e != nil {
		t.Fatal(e)
	}
	// DescribeExec sends Parse/Describe/Sync and then Bind/Execute/Sync.
	if e = c.QueryRow(ctx, "select $1::int", pgx.QueryExecModeDescribeExec, 7).Scan(&n); e != nil || n != 7 {
		t.Fatalf("describe exchange: %d %v", n, e)
	}

	// The frontend must receive description responses before it sends Sync.
	flushClient, err := pgx.ConnectConfig(ctx, clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer flushClient.Close(context.Background())
	wire := flushClient.PgConn().Frontend()
	wire.Send(&pgproto3.Parse{Query: "SELECT 1"})
	wire.Send(&pgproto3.Describe{ObjectType: 'S'})
	wire.Send(&pgproto3.Flush{})
	if err := wire.Flush(); err != nil {
		t.Fatal(err)
	}
	flushClient.PgConn().Conn().SetDeadline(time.Now().Add(3 * time.Second))
	for _, expected := range []string{"parse", "parameters", "rows"} {
		message, err := wire.Receive()
		if err != nil {
			t.Fatalf("Flush before Sync: %v", err)
		}
		actual := ""
		switch message.(type) {
		case *pgproto3.ParseComplete:
			actual = "parse"
		case *pgproto3.ParameterDescription:
			actual = "parameters"
		case *pgproto3.RowDescription:
			actual = "rows"
		}
		if actual != expected {
			t.Fatalf("expected %s, got %T", expected, message)
		}
	}
	if s.Stats().Active != 1 {
		t.Fatal("description did not retain admission")
	}
	wire.Send(&pgproto3.Sync{})
	if err := wire.Flush(); err != nil {
		t.Fatal(err)
	}
	message, err := wire.Receive()
	if err != nil {
		t.Fatal(err)
	}
	if ready, ok := message.(*pgproto3.ReadyForQuery); !ok || ready.TxStatus != 'I' {
		t.Fatalf("expected idle, got %T", message)
	}
	flushClient.PgConn().Conn().SetDeadline(time.Time{})
	flushClient.Close(ctx)
	required := clientConfig.Copy()
	required.ChannelBinding = "require"
	if unsupported, err := pgx.ConnectConfig(ctx, required); err == nil {
		unsupported.Close(ctx)
		t.Fatal("channel-bound SCRAM profile unexpectedly accepted")
	}
	bad := clientConfig.Copy()
	bad.Password = "invalid-fixture-password"
	wrong, e := pgx.ConnectConfig(ctx, bad)
	if e == nil {
		wrong.Close(ctx)
		t.Fatal("wrong password authenticated")
	}
	untrusted := clientConfig.Copy()
	untrusted.TLSConfig = &tls.Config{ServerName: "127.0.0.1", RootCAs: x509.NewCertPool()}
	wrong, e = pgx.ConnectConfig(ctx, untrusted)
	if e == nil {
		wrong.Close(ctx)
		t.Fatal("untrusted TLS certificate accepted")
	}

	running, err := pgx.ConnectConfig(ctx, clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer running.Close(context.Background())
	result := make(chan error, 1)
	go func() { _, err := running.Exec(ctx, "SELECT pg_sleep(5)"); result <- err }()
	eventually(t, func() bool { return s.Stats().Active == 1 })
	if err := running.PgConn().CancelRequest(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("canceled operation reported success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("running cancel did not close client")
	}
	eventually(t, func() bool { return s.Stats().Uncertain == 1 && srv.UncertainCount() == 1 })
	t.Log("Verified local PostgreSQL: TLS on both legs, SCRAM-SHA-256, original role, simple batches, unnamed extended/DescribeExec, failed transaction rollback, invalid credentials and CA rejection.")
	if strings.Contains(user, "postgres") {
		t.Fatal("unexpected elevated identity")
	}
}
