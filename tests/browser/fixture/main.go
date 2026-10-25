//go:build integration

// Controlled browser fixture. It models authorization responses; it is not Supabase.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"github.com/golang-jwt/jwt/v5"
	"log"
	"net/http"
	"pooltune/internal/domain"
	"pooltune/internal/httpingress"
	"pooltune/internal/policy"
	"pooltune/internal/scheduler"
	"sync/atomic"
	"time"
)

func main() {
	startAdminFixture()
	startTuningFixture()
	startBurstFixture()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	verifier, err := httpingress.NewVerifier("fixture", "authenticated", map[string]*ecdsa.PublicKey{"test": &key.PublicKey}, time.Now().Add(time.Hour))
	if err != nil {
		log.Fatal(err)
	}
	token := func(subject string) string {
		t := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.RegisteredClaims{Issuer: "fixture", Audience: jwt.ClaimStrings{"authenticated"}, Subject: subject, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))})
		t.Header["kid"] = "test"
		s, _ := t.SignedString(key)
		return s
	}
	var upstreamCalls atomic.Int64
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "http://127.0.0.1:12160")
		w.Header().Set("Access-Control-Allow-Headers", "authorization,content-type,apikey")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/auth/v1/token" {
			json.NewEncoder(w).Encode(map[string]string{"access_token": token("alice")})
			return
		}
		upstreamCalls.Add(1)
		id := verifier.Verify(r.Header.Get("Authorization"), time.Now())
		if !id.Verified {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"code":"PGRST301"}`)
			return
		}
		if r.URL.Query().Get("tenant") == "bob" {
			w.WriteHeader(403)
			fmt.Fprint(w, `{"code":"42501"}`)
			return
		}
		if r.URL.Path == "/rest/v1/slow" {
			time.Sleep(250 * time.Millisecond)
		}
		if r.URL.Path == "/rest/v1/hang" {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "started")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		if r.URL.Path == "/rest/v1/stream" {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "first")
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
			fmt.Fprint(w, "second")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Range", "0-0/1")
		w.Header().Set("Preference-Applied", "return=representation")
		if r.Method == "POST" {
			w.WriteHeader(201)
		}
		fmt.Fprint(w, `[{"id":1,"tenant":"alice"}]`)
	})
	p, err := policy.Compile(policy.Policy{SchemaVersion: "1", Revision: "p1", GuardrailVersion: "g1", ClassWeights: policy.ClassWeights{Critical: 1, Normal: 1, Background: 1}}, policy.Guardrails{Version: "g1", MaxReserveFraction: .9, MinClassWeight: 1, MaxClassWeight: 10}, policy.Registry{Fallback: policy.Fallback{QueueWait: 100 * time.Millisecond, QueueLimit: 2, ConcurrencyCap: 1}})
	if err != nil {
		log.Fatal(err)
	}
	s, err := scheduler.New(scheduler.Config{MaxActive: 1, MaxQueued: 2, MaxQueuedBytes: 1 << 20, MaxTenants: 10, MaxWorkloads: 10, MaxTenantQueued: 2, MaxTenantActive: 1, RatePerSecond: 1000, RateBurst: 100}, scheduler.Snapshot{Limits: domain.LimitSnapshot{Target: "fixture", Path: domain.PathREST, Revision: "c1", Effective: 1, CohortBudgets: map[string]int{"stable": 1}, GuardrailVersion: "g1", Epoch: 1}, Policies: map[string]*policy.CompiledPolicy{"stable": p}})
	if err != nil {
		log.Fatal(err)
	}
	h, err := httpingress.NewRESTHandler(httpingress.TargetConfig{Target: "fixture", Upstream: "http://127.0.0.1:12161", LocalTest: true, MaxRequests: 4, MaxBodyBytes: 1 << 20, MaxHeaderBytes: 16 << 10, Timeout: 3 * time.Second, AllowedOrigins: []string{"http://127.0.0.1:12160"}, Verify: verifier.Verify, Classify: func(c domain.RequestContext) domain.Classification {
		out := policy.Classify(p, c)
		out.Cohort = "stable"
		return out
	}}, s)
	if err != nil {
		log.Fatal(err)
	}
	app := http.NewServeMux()
	app.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
	app.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"scheduler": s.Stats(), "upstream_calls": upstreamCalls.Load()})
	})
	app.HandleFunc("/sdk.js", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, "dist/supabase-fetch.js") })
	app.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><title>Pooltune browser fixture</title><script type="module">import {createPooltuneFetch} from '/sdk.js';window.route=createPooltuneFetch('http://127.0.0.1:12161','http://127.0.0.1:12162');window.ready=true;</script>`)
	})
	serve := func(addr string, h http.Handler) {
		server := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: time.Second, ReadTimeout: 4 * time.Second, WriteTimeout: 4 * time.Second, MaxHeaderBytes: 16 << 10}
		if err := server.ListenAndServe(); err != nil {
			log.Fatal(err)
		}
	}
	go serve("127.0.0.1:12161", upstream)
	go serve("127.0.0.1:12162", h)
	serve("127.0.0.1:12160", app)
}
