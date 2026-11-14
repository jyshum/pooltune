package httpingress

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"pooltune/internal/domain"
	"pooltune/internal/measurement"
	"pooltune/internal/observation"
	"pooltune/internal/scheduler"
	"testing"
	"time"
)

func TestRESTOutcomesAndVerifiedCohortIdentity(t *testing.T) {
	for _, mode := range []string{"success", "denied", "unknown", "rejected", "expired"} {
		t.Run(mode, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "unknown" {
					c, _, _ := w.(http.Hijacker).Hijack()
					c.Close()
					return
				}
				if mode == "denied" {
					w.WriteHeader(403)
				}
				io.WriteString(w, "{}")
			}))
			defer up.Close()
			s := fixtureScheduler(t)
			cfg := fixtureConfig(up.URL)
			cfg.Timeout = 30 * time.Millisecond
			cfg.Verify = func(string, time.Time) Identity {
				return Identity{Verified: true, Subject: "alice", ExpiresAt: time.Now().Add(time.Hour)}
			}
			if mode == "expired" {
				cfg.Verify = func(string, time.Time) Identity {
					return Identity{Verified: true, Subject: "alice", ExpiresAt: time.Now().Add(-time.Second)}
				}
			}
			cfg.Classify = nil
			cfg.ClassifyIdentity = func(c domain.RequestContext, id Identity) domain.Classification {
				if id.Subject != "alice" || !id.Verified || !c.TrustedTenant {
					t.Error("verified cohort identity lost")
				}
				out := classify(c)
				out.ActivationRevision = "c1"
				return out
			}
			var events []observation.Event
			cfg.Observe = func(e observation.Event) { events = append(events, e) }
			if mode == "rejected" {
				p, e := s.Acquire(context.Background(), classify(domain.RequestContext{}))
				if e != nil {
					t.Fatal(e)
				}
				defer s.Release(p, scheduler.Finished)
			}
			h, e := NewRESTHandler(cfg, s)
			if e != nil {
				t.Fatal(e)
			}
			defer h.Close()
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/rest/v1/items", nil))
			expected := 3
			if mode == "rejected" {
				expected = 2
			}
			if len(events) != expected {
				t.Fatalf("events: %+v", events)
			}
			first, last := events[0], events[len(events)-1]
			if first.Kind != observation.Arrived || first.ID == 0 || last.ID != first.ID || last.ArrivedAt != first.ArrivedAt || last.Classification.ActivationRevision != "c1" || last.FinishedAt.Before(first.ArrivedAt) {
				t.Fatalf("invalid event lineage: %+v", events)
			}
			switch mode {
			case "unknown":
				if last.Kind != observation.Indeterminate || s.Stats().Uncertain != 1 {
					t.Fatal("ambiguous outcome released")
				}
			case "rejected":
				if last.Kind != observation.AdmissionRejected || !last.TimedOut {
					t.Fatal("timeout lost")
				}
			default:
				if last.Kind != observation.Completed || last.Failed != (mode != "success") || last.AcquiredAt.IsZero() || s.Stats().Active != 0 {
					t.Fatalf("wrong completion: %+v", last)
				}
			}
		})
	}
}
func TestPrivatePostgRESTSocketMapping(t *testing.T) {
	dir, e := os.MkdirTemp("/tmp", "pt-http-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "postgrest.sock")
	ln, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	defer ln.Close()
	os.Chmod(socket, 0600)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.RequestURI != "/items?x=a%2Fb" || r.Header.Get("Authorization") != "Bearer original" {
			t.Error("local mapping changed credentials/query")
		}
		io.WriteString(w, "ok")
	})}
	go srv.Serve(ln)
	defer srv.Close()
	cfg := fixtureConfig("")
	cfg.LocalPostgRESTSocket = socket
	h, e := NewRESTHandler(cfg, fixtureScheduler(t))
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close()
	r := httptest.NewRequest("GET", "/rest/v1/items?x=a%2Fb", nil)
	r.Header.Set("Authorization", "Bearer original")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "ok" {
		t.Fatal("private upstream failed")
	}
	os.Chmod(socket, 0666)
	if _, e = NewRESTHandler(cfg, fixtureScheduler(t)); e == nil {
		t.Fatal("public socket accepted")
	}
	os.Chmod(socket, 0600)
	cfg.Upstream = "https://example.com"
	if _, e = NewRESTHandler(cfg, fixtureScheduler(t)); e == nil {
		t.Fatal("ambiguous upstream accepted")
	}
}

func TestUnverifiedIdentityClearedBeforeCohortCallback(t *testing.T) {
	cfg := fixtureConfig("http://127.0.0.1:1")
	cfg.Verify = func(string, time.Time) Identity { return Identity{Subject: "forged"} }
	cfg.ClassifyIdentity = func(c domain.RequestContext, id Identity) domain.Classification {
		if id.Subject != "" || c.TrustedTenant {
			t.Error("unverified identity reached cohort routing")
		}
		return classify(c)
	}
	h, e := NewRESTHandler(cfg, fixtureScheduler(t))
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/rest/v1/items", nil))
}

func TestRESTOverloadInvalidatesOutcomeWindow(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; io.WriteString(w, "ok") }))
	defer up.Close()
	cfg := fixtureConfig(up.URL)
	cfg.MaxRequests = 1
	collector, e := measurement.New([]string{"api"}, 10)
	if e != nil {
		t.Fatal(e)
	}
	start := time.Now()
	if e = collector.Begin("c1", start, time.Second); e != nil {
		t.Fatal(e)
	}
	cfg.Classify = func(c domain.RequestContext) domain.Classification {
		v := classify(c)
		v.ActivationRevision = "c1"
		return v
	}
	cfg.Observe = collector.Record
	s := fixtureScheduler(t)
	if err := s.EnableCapacity([]string{"api"}); err != nil {
		t.Fatal(err)
	}
	h, e := NewRESTHandler(cfg, s)
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/rest/v1/items", nil))
	}()
	<-entered
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/rest/v1/items", nil))
	close(release)
	<-done
	if w.Code != 503 {
		t.Fatal("ingress overload not bounded")
	}
	window, e := collector.Snapshot(start.Add(time.Second))
	if e != nil {
		t.Fatal(e)
	}
	if window.Complete {
		t.Fatal("overload omitted from complete outcome window")
	}
	if s.CapacityMetrics().Omitted == 0 {
		t.Fatal("request-slot overflow omitted from growth evidence")
	}

}

func TestHTTPErrorsAreNotSuccessfulCapacityEvidence(t *testing.T) {
	for _, code := range []int{403, 429, 503} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code); io.WriteString(w, "{}") }))
			defer up.Close()
			s := fixtureScheduler(t)
			if err := s.EnableCapacity([]string{"api"}); err != nil {
				t.Fatal(err)
			}
			h, err := NewRESTHandler(fixtureConfig(up.URL), s)
			if err != nil {
				t.Fatal(err)
			}
			defer h.Close()
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/rest/v1/items", nil))
			time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second)) + 10*time.Millisecond)
			m := s.CapacityMetrics()
			g := m.Groups[0]
			if g.Successes != 0 || g.Terminals != 1 {
				t.Fatalf("HTTP failure counted as success: %+v", g)
			}
			want := uint64(0)
			if code == 429 || code == 503 {
				want = 1
			}
			if g.OverloadFailures != want {
				t.Fatal("overload evidence missing", g)
			}
		})
	}
}
