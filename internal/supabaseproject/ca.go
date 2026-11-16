package supabaseproject

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

const officialCAURL = "https://supabase-downloads.s3-ap-southeast-1.amazonaws.com/prod/ssl/prod-ca-2021.crt"

var officialCA = &certificateSource{client: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, endpoint: officialCAURL}
var errCA = errors.New("could not verify the official Supabase certificate. Retry setup")

type certificateSource struct {
	mu       sync.Mutex
	client   *http.Client
	endpoint string
	cached   []byte
	until    time.Time
}

func OfficialCA(ctx context.Context) ([]byte, error) { return officialCA.read(ctx) }
func (s *certificateSource) read(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cached) > 0 && time.Now().Before(s.until) {
		return bytes.Clone(s.cached), nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", s.endpoint, nil)
	if err != nil {
		return nil, errCA
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, errCA
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errCA
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32769))
	if err != nil || len(data) > 32768 {
		return nil, errCA
	}
	until, err := validateCA(data, time.Now())
	if err != nil {
		return nil, err
	}
	s.cached = bytes.Clone(data)
	s.until = minTime(until, time.Now().Add(24*time.Hour))
	return data, nil
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func validateCA(data []byte, now time.Time) (time.Time, error) {
	rest := bytes.TrimSpace(data)
	var until time.Time
	count := 0
	for len(rest) > 0 {
		block, next := pem.Decode(rest)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) > 0 {
			return time.Time{}, errCA
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !cert.IsCA || !cert.BasicConstraintsValid || cert.KeyUsage&x509.KeyUsageCertSign == 0 || now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) {
			return time.Time{}, errCA
		}
		if count == 0 || cert.NotAfter.Before(until) {
			until = cert.NotAfter
		}
		count++
		rest = bytes.TrimSpace(next)
	}
	if count == 0 {
		return time.Time{}, errCA
	}
	return until, nil
}
