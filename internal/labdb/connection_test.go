package labdb

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConnectionRoutes(t *testing.T) {
	t.Setenv("POOLTUNE_LAB_ROOKERY_CA_FILE", testCA(t))
	local := "postgres://lab:test@127.0.0.1:5432/state?sslmode=disable"
	remote := "postgres://postgres.osdjqwpzgyzwfaxyufze:test@aws-0-us-west-2.pooler.supabase.com:5432/postgres?sslmode=verify-full"
	if config, err := Config(local, false); err != nil || config.Host != "127.0.0.1" ||
		config.Database != "state" || config.Password != "test" || len(config.Fallbacks) != 0 {
		t.Fatal("local fixture route is unavailable")
	}

	config, err := Config(remote, true)
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != RookeryHost || config.User != RookeryUser || config.Port != 5432 ||
		config.TLSConfig == nil || config.TLSConfig.ServerName != RookeryHost ||
		config.TLSConfig.InsecureSkipVerify || config.TLSConfig.RootCAs == nil || len(config.Fallbacks) != 0 {
		t.Fatal("Rookery must use hostname and certificate validation with no fallback")
	}

	for _, test := range []struct {
		name, dsn string
		rookery   bool
	}{
		{"remote state", remote, false},
		{"unselected remote", remote, false},
		{"other project", strings.Replace(remote, "osdjqwpzgyzwfaxyufze", "otherproject", 1), true},
		{"other user", strings.Replace(remote, "postgres.", "service.", 1), true},
		{"other host", strings.Replace(remote, "aws-0-us-west-2", "aws-0-us-east-1", 1), true},
		{"other port", strings.Replace(remote, ":5432", ":6543", 1), true},
		{"other database", strings.Replace(remote, "/postgres?", "/other?", 1), true},
		{"downgrade", strings.Replace(remote, "verify-full", "require", 1), true},
		{"ambiguous ssl", local + "&sslmode=verify-full", false},
		{"fallback", strings.Replace(local, "127.0.0.1", "127.0.0.1,elsewhere", 1), false},
		{"query redirect", local + "&host=elsewhere", false},
		{"query options", local + "&options=x", false},
		{"missing password", strings.Replace(local, "lab:test", "lab", 1), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Config(test.dsn, test.rookery); err == nil {
				t.Fatal("accepted invalid lab route")
			} else if strings.Contains(err.Error(), "lab:test") || strings.Contains(err.Error(), ":test@") {
				t.Fatal("error included a DSN")
			}
		})
	}
}

func testCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	return certFile
}
