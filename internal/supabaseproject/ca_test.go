package supabaseproject

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOfficialCARejectsInvalidAndCachesVerifiedCertificate(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	now := time.Now()
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Fixture CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, _ := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	valid := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	body := []byte("not a certificate")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = w.Write(body) }))
	defer server.Close()
	source := &certificateSource{client: server.Client(), endpoint: server.URL}
	if _, err := source.read(context.Background()); err == nil {
		t.Fatal("invalid CA accepted")
	}
	body = valid
	if _, err := source.read(context.Background()); err != nil {
		t.Fatal(err)
	}
	body = []byte("unavailable")
	if _, err := source.read(context.Background()); err != nil || calls != 2 {
		t.Fatalf("verified CA not cached %d %v", calls, err)
	}
	cert.NotAfter = now.Add(-time.Minute)
	der, _ = x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if _, err := validateCA(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), now); err == nil {
		t.Fatal("expired CA accepted")
	}
	cert.NotAfter = now.Add(time.Hour)
	cert.IsCA = false
	der, _ = x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if _, err := validateCA(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), now); err == nil {
		t.Fatal("leaf accepted as CA")
	}
}

func TestOfficialCAWaitDoesNotOutliveSetupCancellation(t *testing.T) {
	entered, release := make(chan struct{}, 1), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		fmt.Fprint(w, "invalid fixture certificate")
	}))
	defer server.Close()
	source := &certificateSource{client: server.Client(), endpoint: server.URL}
	done := make(chan struct{})
	go func() { _, _ = source.read(context.Background()); close(done) }()
	<-entered
	go func() { time.Sleep(150 * time.Millisecond); close(release) }()
	defer func() { <-done }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := source.read(ctx); err == nil || time.Since(start) > 120*time.Millisecond {
		t.Fatalf("CA cache wait exceeded canceled setup budget: %v %v", time.Since(start), err)
	}
}
