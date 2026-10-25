package burst

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"pooltune/internal/admin"
	"pooltune/internal/store"
	"strings"
	"testing"
	"time"
)

func TestManagedRestoreRequiresAuthenticatedFreshCSRFRequest(t *testing.T) {
	c := journalController(t, "azure", nil)
	base := c.State().Revision
	expand(t, c)
	db, e := store.OpenStore(filepath.Join(t.TempDir(), "auth.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	auth, e := admin.NewAuth(db, strings.Repeat("m", 43), time.Now().Add(time.Hour), time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	session, e := auth.Claim(strings.Repeat("m", 43), "managed-test-password-only")
	if e != nil {
		t.Fatal(e)
	}
	origin := "http://127.0.0.1:18888"
	handler, e := admin.NewHandler(admin.Config{Origin: origin, LocalHTTP: true}, auth, DashboardBackend{c}, nil)
	if e != nil {
		t.Fatal(e)
	}
	cmd := command(c, "secure-restore", base)
	request := func(cookie, csrf, from string, body admin.Command) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", origin+"/api/revert", bytes.NewReader(raw))
		r.RemoteAddr = "127.0.0.1:9000"
		r.Header.Set("Origin", from)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", csrf)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "pooltune_admin", Value: cookie})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, x := range []struct {
		cookie, csrf, origin string
		code                 int
	}{{"", session.CSRF, origin, 401}, {session.Token, "bad", origin, 403}, {session.Token, session.CSRF, "http://evil.invalid", 403}} {
		if w := request(x.cookie, x.csrf, x.origin, cmd); w.Code != x.code {
			t.Fatalf("security gate: %d", w.Code)
		}
	}
	if c.State().Effective != 3 {
		t.Fatal("rejected HTTP request mutated state")
	}
	freshHealth(c) // Password hashing can outlast the two-second inventory window.
	if w := request(session.Token, session.CSRF, origin, cmd); w.Code != 200 {
		t.Fatalf("restore failed: %d %s", w.Code, w.Body.String())
	}
	stale := cmd
	stale.IdempotencyKey = "different-tab"
	if w := request(session.Token, session.CSRF, origin, stale); w.Code != 409 {
		t.Fatal("stale HTTP restore accepted")
	}
	v, _ := (DashboardBackend{c}).Read(context.Background(), "revisions")
	raw, _ := json.Marshal(v)
	for _, private := range []string{"private-resource", "secure-restore", "expected", "key"} {
		if strings.Contains(string(raw), private) {
			t.Fatal("history exposed private request metadata")
		}
	}
}
