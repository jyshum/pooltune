//go:build integration

// Package testpool starts an isolated real PgBouncer above disposable PostgreSQL.
package testpool

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"pooltune/internal/testpg"
	"testing"
	"time"
)

func Start(t *testing.T) (context.Context, *pgx.ConnConfig, *pgx.ConnConfig, *pgx.ConnConfig, tls.Certificate) {
	t.Helper()
	binary, err := exec.LookPath("pgbouncer")
	if err != nil {
		t.Skip("local PgBouncer unavailable")
	}
	_, monitor, cert := testpg.Start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	db, err := pgx.ConnectConfig(ctx, monitor)
	if err != nil {
		t.Fatal(err)
	}
	// Test password stays in private temporary files and connection objects.
	_, err = db.Exec(ctx, fmt.Sprintf("CREATE ROLE poolapp LOGIN PASSWORD '%s'", monitor.Password))
	db.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	crt := filepath.Join(dir, "server.crt")
	key := filepath.Join(dir, "server.key")
	os.WriteFile(crt, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0600)
	os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(cert.PrivateKey.(*rsa.PrivateKey))}), 0600)
	auth := filepath.Join(dir, "users.txt")
	os.WriteFile(auth, []byte(fmt.Sprintf("\"poolapp\" \"%s\"\n\"pooladmin\" \"%s\"\n", monitor.Password, monitor.Password)), 0600)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	ini := fmt.Sprintf(`[databases]
postgres = host=127.0.0.1 port=%d dbname=postgres
[pgbouncer]
listen_addr = 127.0.0.1
listen_port = %d
unix_socket_dir =
auth_type = scram-sha-256
auth_file = %s
admin_users = pooladmin
pool_mode = transaction
default_pool_size = 2
min_pool_size = 2
reserve_pool_size = 0
max_client_conn = 100
server_idle_timeout = 1
client_tls_sslmode = require
client_tls_cert_file = %s
client_tls_key_file = %s
server_tls_sslmode = verify-full
server_tls_ca_file = %s
log_connections = 0
log_disconnections = 0
`, monitor.Port, port, auth, crt, key, crt)
	file := filepath.Join(dir, "pool.ini")
	os.WriteFile(file, []byte(ini), 0600)
	log, err := os.OpenFile(filepath.Join(dir, "pool.log"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-q", file)
	cmd.Stdout = log
	cmd.Stderr = log
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait(); log.Close() })
	admin := monitor.Copy()
	admin.Port = uint16(port)
	admin.Database = "pgbouncer"
	admin.User = "pooladmin"
	admin.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	app := monitor.Copy()
	app.Port = uint16(port)
	app.User = "poolapp"
	app.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, e := pgx.ConnectConfig(ctx, admin)
		if e == nil {
			c.Close(ctx)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("PgBouncer startup failed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return ctx, admin, app, monitor, cert
}
