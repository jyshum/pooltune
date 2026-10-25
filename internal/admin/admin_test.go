package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"pooltune/internal/store"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func authFixture(t *testing.T) (*Auth, string) {
	t.Helper()
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	db, e := store.OpenStore(filepath.Join(dir, "control.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	claim := strings.Repeat("x", 43)
	a, e := NewAuth(db, claim, time.Now().Add(time.Hour), time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	return a, claim
}
func TestClaimAtomicAndSessionRevocation(t *testing.T) {
	a, claim := authFixture(t)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := a.Claim(claim, "a sufficiently long password"); e == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal("claim not one-time")
	}
	session, e := a.Login("a sufficiently long password")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.Session(session.Token); e != nil {
		t.Fatal(e)
	}
	if e = a.Logout(session.Token, false); e != nil {
		t.Fatal(e)
	}
	if _, e = a.Session(session.Token); e == nil {
		t.Fatal("logged-out cookie still works")
	}
}

type fakeBackend struct {
	mu       sync.Mutex
	revision string
	calls    int
}

func (b *fakeBackend) Read(context.Context, string) (any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return map[string]any{"revision": b.revision, "frozen": false}, nil
}
func (b *fakeBackend) Execute(_ context.Context, action string, c Command) (any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if c.ExpectedRevision != b.revision {
		return nil, ErrConflict
	}
	b.calls++
	b.revision = "r2"
	return map[string]string{"revision": b.revision, "status": "confirmed"}, nil
}
func TestCrossOriginMutationAndStaleEdits(t *testing.T) {
	a, claim := authFixture(t)
	s, e := a.Claim(claim, "a sufficiently long password")
	if e != nil {
		t.Fatal(e)
	}
	b := &fakeBackend{revision: "r1"}
	h, e := NewHandler(Config{Origin: "http://127.0.0.1:18080", LocalHTTP: true}, a, b, nil)
	if e != nil {
		t.Fatal(e)
	}
	request := func(origin, revision string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://127.0.0.1:18080/api/freeze", strings.NewReader(`{"expected_revision":"`+revision+`","idempotency_key":"command1"}`))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", s.CSRF)
		r.AddCookie(&http.Cookie{Name: "pooltune_admin", Value: s.Token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request("http://evil.example", "r1"); w.Code != 403 || b.calls != 0 {
		t.Fatal("cross-origin mutation")
	}
	if w := request("http://127.0.0.1:18080", "r1"); w.Code != 200 || b.calls != 1 {
		t.Fatal("valid command denied")
	}
	if w := request("http://127.0.0.1:18080", "r1"); w.Code != 409 || b.calls != 1 {
		t.Fatal("stale edit accepted")
	}
}
func TestUnauthenticatedStateCannotEchoSecrets(t *testing.T) {
	a, _ := authFixture(t)
	h, e := NewHandler(Config{Origin: "http://127.0.0.1:18080", LocalHTTP: true}, a, &fakeBackend{revision: "private-value"}, nil)
	if e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1:18080/api/state", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 || strings.Contains(w.Body.String(), "private-value") {
		t.Fatal("private state leaked")
	}
}

func TestClaimAndSessionsPersistWithoutReset(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	path := filepath.Join(dir, "control.db")
	db, e := store.OpenStore(path)
	if e != nil {
		t.Fatal(e)
	}
	token := strings.Repeat("x", 43)
	a, e := NewAuth(db, token, time.Now().Add(time.Hour), time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	session, e := a.Claim(token, "persistent administrator password")
	if e != nil {
		t.Fatal(e)
	}
	db.Close()
	restored, e := store.OpenStore(path)
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	a, e = NewAuth(restored, token, time.Now().Add(time.Hour), time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	if claimed, e := a.Claimed(); e != nil || !claimed {
		t.Fatal("restart forgot claim")
	}
	if _, e = a.Session(session.Token); e != nil {
		t.Fatal("restart lost live session")
	}
	if _, e = a.Claim(token, "another sufficiently long password"); e == nil {
		t.Fatal("restart reopened setup")
	}
	second, e := a.Login("persistent administrator password")
	if e != nil {
		t.Fatal(e)
	}
	if e = a.Logout(session.Token, true); e != nil {
		t.Fatal(e)
	}
	if _, e = a.Session(second.Token); e == nil {
		t.Fatal("logout-all kept another session")
	}
}
func TestExpiredBootstrapRejected(t *testing.T) {
	dir := t.TempDir()
	db, e := store.OpenStore(filepath.Join(dir, "control.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	claim := strings.Repeat("x", 43)
	if _, e = NewAuth(db, claim, time.Now().Add(-time.Second), time.Hour); e == nil {
		t.Fatal("expired bootstrap accepted")
	}
}
