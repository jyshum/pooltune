//go:build integration

// Package testpg starts a disposable real PostgreSQL instance with TLS/SCRAM.
package testpg

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"github.com/jackc/pgx/v5"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func Start(t *testing.T) (context.Context, *pgx.ConnConfig, tls.Certificate) {
	t.Helper()
	initdb, e := exec.LookPath("initdb")
	if e != nil {
		t.Skip("local PostgreSQL binaries unavailable")
	}
	postgres, e := exec.LookPath("postgres")
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	secret := make([]byte, 24)
	rand.Read(secret)
	password := base64.RawURLEncoding.EncodeToString(secret)
	pwfile := filepath.Join(dir, "password")
	if e = os.WriteFile(pwfile, []byte(password), 0600); e != nil {
		t.Fatal(e)
	}
	data := filepath.Join(dir, "data")
	cmd := exec.Command(initdb, "-D", data, "-U", "postgres", "--auth-local=trust", "--auth-host=scram-sha-256", "--pwfile="+pwfile, "--no-locale")
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("initdb: %v %s", e, out)
	}
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Pooltune isolated fixture"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, e := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certfile, keyfile := filepath.Join(data, "server.crt"), filepath.Join(data, "server.key")
	os.WriteFile(certfile, certPEM, 0600)
	os.WriteFile(keyfile, keyPEM, 0600)
	cert, e := tls.X509KeyPair(certPEM, keyPEM)
	if e != nil {
		t.Fatal(e)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certPEM)
	reserve, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	addr := reserve.Addr().String()
	_, port, _ := net.SplitHostPort(addr)
	reserve.Close()
	log, e := os.Create(filepath.Join(dir, "postgres.log"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { log.Close() })
	pg := exec.Command(postgres, "-D", data, "-h", "127.0.0.1", "-p", port, "-k", "", "-c", "ssl=on", "-c", "max_connections=60")
	pg.Stdout = log
	pg.Stderr = log
	if e = pg.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { pg.Process.Signal(os.Interrupt); pg.Wait() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	config, e := pgx.ParseConfig("postgres://postgres:" + password + "@" + addr + "/postgres?sslmode=require")
	if e != nil {
		t.Fatal(e)
	}
	config.TLSConfig = &tls.Config{RootCAs: roots, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12}
	config.Fallbacks = nil
	config.DefaultQueryExecMode = pgx.QueryExecModeExec
	var admin *pgx.Conn
	eventually(t, func() bool {
		admin, e = pgx.ConnectConfig(ctx, config)
		if e != nil {
			time.Sleep(10 * time.Millisecond)
		}
		return e == nil
	})
	t.Cleanup(func() { admin.Close(context.Background()) })
	if _, e = admin.Exec(ctx, "CREATE ROLE fixture_app LOGIN PASSWORD '"+password+"'"); e != nil {
		t.Fatal(e)
	}
	admin.Close(context.Background())
	return ctx, config, cert
}

func eventually(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("isolated PostgreSQL did not start")
}
