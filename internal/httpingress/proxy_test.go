package httpingress

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"github.com/golang-jwt/jwt/v5"
	"io"
	"net/http"
	"net/http/httptest"
	"pooltune/internal/domain"
	"pooltune/internal/policy"
	"pooltune/internal/scheduler"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixtureScheduler(t *testing.T) *scheduler.Scheduler {
	t.Helper()
	p, e := policy.Compile(policy.Policy{SchemaVersion: "1", Revision: "p1", GuardrailVersion: "g1", ClassWeights: policy.ClassWeights{Critical: 1, Normal: 1, Background: 1}}, policy.Guardrails{Version: "g1", MaxReserveFraction: .9, MinClassWeight: 1, MaxClassWeight: 10}, policy.Registry{Fallback: policy.Fallback{QueueWait: time.Second, QueueLimit: 10, ConcurrencyCap: 10}})
	if e != nil {
		t.Fatal(e)
	}
	s, e := scheduler.New(scheduler.Config{MaxActive: 10, MaxQueued: 10, MaxQueuedBytes: 1 << 20, MaxTenants: 10, MaxWorkloads: 10, MaxTenantQueued: 10, MaxTenantActive: 10, RatePerSecond: 1000, RateBurst: 100}, scheduler.Snapshot{Limits: domain.LimitSnapshot{Target: "db", Path: domain.PathREST, Revision: "c1", CapacityRevision: "c1", Effective: 1, CohortBudgets: map[string]int{"stable": 1}, GuardrailVersion: "g1", Epoch: 1}, Policies: map[string]*policy.CompiledPolicy{"stable": p}})
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func classify(c domain.RequestContext) domain.Classification {
	return domain.Classification{Class: domain.ClassNormal, Cohort: "stable", Workload: "api", FairnessKey: "fallback", Revision: "p1", QueueDeadline: time.Now().Add(time.Second), ConcurrencyCap: 10, QueueLimit: 10}
}
func fixtureConfig(up string) TargetConfig {
	return TargetConfig{Target: "db", Upstream: up, LocalTest: true, MaxRequests: 4, MaxBodyBytes: 1024, MaxHeaderBytes: 8192, Timeout: 2 * time.Second, AllowedOrigins: []string{"https://app.example"}, Classify: classify}
}
func TestProxyPreservesAuthorizationAndAPI(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer original" || r.Header.Get("Apikey") != "public-key" {
			t.Error("credentials changed")
		}
		if r.RequestURI != "/rest/v1/rpc/pay?x=a%2Fb" || r.Method != "POST" {
			t.Error("routing changed")
		}
		if r.Header.Get("X-Hop") != "" || r.Header.Get("X-Forwarded-For") != "" {
			t.Error("untrusted forwarding header")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "payload" {
			t.Error("body changed")
		}
		w.Header().Set("Content-Range", "0-0/1")
		w.WriteHeader(403)
		w.Write([]byte(`{"code":"42501"}`))
	}))
	defer upstream.Close()
	s := fixtureScheduler(t)
	h, e := NewRESTHandler(fixtureConfig(upstream.URL), s)
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close()
	r := httptest.NewRequest("POST", "http://gateway/rest/v1/rpc/pay?x=a%2Fb", strings.NewReader("payload"))
	r.Header = http.Header{"Authorization": {"Bearer original"}, "Apikey": {"public-key"}, "Origin": {"https://app.example"}, "Connection": {"X-Hop"}, "X-Hop": {"bad"}, "X-Forwarded-For": {"spoof"}}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 || w.Header().Get("Content-Range") != "0-0/1" || w.Header().Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Fatalf("response: %d %v", w.Code, w.Header())
	}
	if s.Stats().Active != 0 {
		t.Fatal("completed response leaked permit")
	}
}
func TestBoundsAndCORSDoNotReachUpstream(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer upstream.Close()
	h, e := NewRESTHandler(fixtureConfig(upstream.URL), fixtureScheduler(t))
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close()
	for _, tc := range []struct {
		method, path, origin, body string
		status                     int
	}{{"GET", "/auth/v1/token", "", "", 404}, {"GET", "/rest/v1/x", "https://evil.example", "", 403}, {"POST", "/rest/v1/x", "", strings.Repeat("x", 1025), 413}, {"GET", "/rest/v1/a%2f..%2f..%2fauth/v1", "", "", 400}, {"OPTIONS", "/rest/v1/x", "https://app.example", "", 204}} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Origin", tc.origin)
		if tc.method == "OPTIONS" {
			r.Header.Set("Access-Control-Request-Method", "POST")
			r.Header.Set("Access-Control-Request-Headers", "authorization,apikey,prefer")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: %d", tc.path, w.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("rejected request forwarded")
	}
}
func TestJWTVerificationAndExpiryAfterQueue(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	now := time.Now()
	verifier, e := NewVerifier("https://issuer.example", "authenticated", map[string]*ecdsa.PublicKey{"k1": &key.PublicKey}, now.Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.RegisteredClaims{Issuer: "https://issuer.example", Audience: jwt.ClaimStrings{"authenticated"}, Subject: "alice", ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour))})
	token.Header["kid"] = "k1"
	signed, _ := token.SignedString(key)
	id := verifier.Verify("Bearer "+signed, now)
	if id.Subject != "alice" || !id.Verified {
		t.Fatal("valid identity rejected")
	}
	if verifier.Verify("Bearer "+signed, now.Add(2*time.Hour)).Verified || verifier.Verify("Bearer "+signed+"x", now).Verified {
		t.Fatal("invalid token elevated")
	}
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer up.Close()
	s := fixtureScheduler(t)
	held, e := s.Acquire(context.Background(), classify(domain.RequestContext{}))
	if e != nil {
		t.Fatal(e)
	}
	cfg := fixtureConfig(up.URL)
	cfg.Verify = func(string, time.Time) Identity {
		return Identity{Verified: true, Subject: "alice", ExpiresAt: time.Now().Add(20 * time.Millisecond)}
	}
	h, e := NewRESTHandler(cfg, s)
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close()
	done := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/rest/v1/x", nil))
		done <- w.Code
	}()
	deadline := time.Now().Add(time.Second)
	for s.Stats().Queued == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond)
	s.Release(held, scheduler.Finished)
	if status := <-done; status != 401 || calls.Load() != 0 {
		t.Fatalf("expired token forwarded: %d", status)
	}
}

func TestUncertainWriteIsNotRetriedOrReleased(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.Copy(io.Discard, r.Body)
		c, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		c.Close()
	}))
	defer up.Close()
	s := fixtureScheduler(t)
	h, e := NewRESTHandler(fixtureConfig(up.URL), s)
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/rest/v1/items", strings.NewReader(`{"amount":1}`)))
	if w.Code != 502 || calls.Load() != 1 || s.Stats().Uncertain != 1 {
		t.Fatalf("ambiguous write: status=%d calls=%d stats=%+v", w.Code, calls.Load(), s.Stats())
	}
}

func TestUnverifiedClaimsCannotSupplySchedulingIdentity(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer up.Close()
	cfg := fixtureConfig(up.URL)
	cfg.Verify = func(string, time.Time) Identity { return Identity{Subject: "admin", Verified: false} }
	cfg.Classify = func(c domain.RequestContext) domain.Classification {
		if c.Principal != "" || c.Tenant != "" || c.TrustedTenant {
			t.Error("unverified identity trusted")
		}
		return classify(c)
	}
	h, e := NewRESTHandler(cfg, fixtureScheduler(t))
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close()
	r := httptest.NewRequest("GET", "/rest/v1/items", nil)
	r.Header.Set("X-Priority", "critical")
	r.Header.Set("X-Tenant", "admin")
	h.ServeHTTP(httptest.NewRecorder(), r)
}
