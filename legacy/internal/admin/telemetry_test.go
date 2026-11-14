package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTelemetryRequiresSession(t *testing.T) {
	a, claim := authFixture(t)
	session, e := a.Claim(claim, "a sufficiently long password")
	if e != nil {
		t.Fatal(e)
	}
	h, e := NewHandler(Config{Origin: "http://127.0.0.1:18080", LocalHTTP: true}, a, &fakeBackend{revision: "private-value"}, nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, signed := range []bool{false, true} {
		r := httptest.NewRequest("GET", "http://127.0.0.1:18080/api/telemetry", nil)
		r.RemoteAddr = "127.0.0.1:1234"
		if signed {
			r.AddCookie(&http.Cookie{Name: "pooltune_admin", Value: session.Token})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if !signed && (w.Code != 401 || strings.Contains(w.Body.String(), "private-value")) {
			t.Fatal("unauthenticated history")
		}
		if signed && w.Code != 200 {
			t.Fatalf("authenticated history status %d", w.Code)
		}
	}
}
