//go:build integration

package hostedapp

import (
	"bytes"
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http/httptest"
	"net/url"
	"pooltune/internal/hostedapp/auth"
	"pooltune/internal/hostedquery"
	"pooltune/internal/testpg"
	"strings"
	"testing"
	"time"
)

func TestMonitoringTokenReplacementIsEncryptedOwnerBoundAndRevalidated(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t)
	pc, e := pgxpool.ParseConfig(cfg.ConnString())
	if e != nil {
		t.Fatal(e)
	}
	pc.ConnConfig = cfg
	pool, e := pgxpool.NewWithConfig(ctx, pc)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	app, e := New(ctx, pool, Config{Origin: "https://preview.example.com", InviteCode: "private-token-test-code", Key: bytes.Repeat([]byte{23}, 32), Sampling: hostedquery.SamplingPolicy{Interval: time.Minute, Window: 5 * time.Minute}, QueueInterval: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	user, _, e := app.accounts.Register(ctx, "token@example.com", "a sufficiently long password")
	if e != nil {
		t.Fatal(e)
	}
	other, _, e := app.accounts.Register(ctx, "other-token@example.com", "another sufficiently long password")
	if e != nil {
		t.Fatal(e)
	}
	id, e := app.projects.create(ctx, user.ID, "Token project", "abcdefghijklmnopqrst")
	if e != nil {
		t.Fatal(e)
	}
	row, e := app.projects.find(ctx, user.ID, id)
	if e != nil {
		t.Fatal(e)
	}
	original := projectSecrets{Automatic: true, Password: "monitor-test-password", RepairPassword: "owner-test-password", LogsToken: "old-token", RepairsPaused: true, Capabilities: []string{"memory"}}
	sealed, e := app.vault.seal(row, original)
	if e != nil {
		t.Fatal(e)
	}
	if e = app.projects.activate(ctx, row, sealed); e != nil {
		t.Fatal(e)
	}
	row, _ = app.projects.find(ctx, user.ID, id)
	calls := 0
	valid := true
	verify := func(_ context.Context, ref, token string) error {
		calls++
		if ref != row.Ref || token != "new-secret-token" {
			t.Fatal("wrong project/token validated")
		}
		if !valid {
			return errors.New("sensitive provider body must not leak")
		}
		return nil
	}
	post := func(as auth.User) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://preview.example.com/app/projects/"+id+"/monitoring-token", strings.NewReader(url.Values{"monitoring_token": {"new-secret-token"}}.Encode()))
		r.SetPathValue("id", id)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		app.replaceMonitoringTokenWith(w, r, as, verify)
		if strings.Contains(w.Body.String(), "secret-token") || strings.Contains(w.Body.String(), "sensitive provider") {
			t.Fatal("credential/provider response exposed")
		}
		return w
	}
	if w := post(other); w.Code != 404 || calls != 0 {
		t.Fatal("another account reached credential validation")
	}
	valid = false
	if w := post(user); w.Code != 400 {
		t.Fatal("unverified token accepted")
	}
	unchanged, _ := app.projects.find(ctx, user.ID, id)
	if unchanged.Epoch != row.Epoch {
		t.Fatal("failed check changed permissions")
	}
	valid = true
	if w := post(user); w.Code != 303 {
		t.Fatal("valid replacement rejected", w.Code)
	}
	updated, _ := app.projects.find(ctx, user.ID, id)
	saved, e := app.vault.open(updated, updated.Secret)
	if e != nil {
		t.Fatal(e)
	}
	if updated.Epoch != row.Epoch+1 || saved.LogsToken != "new-secret-token" || saved.Password != original.Password || saved.RepairPassword != original.RepairPassword || !saved.RepairsPaused || len(saved.Capabilities) != 1 {
		t.Fatal("replacement lost existing permissions or credentials")
	}
	if bytes.Contains(updated.Secret, []byte("new-secret-token")) {
		t.Fatal("token stored in plaintext")
	}
	saved.DisconnectPending = true
	sealed, _ = app.vault.seal(updated, saved)
	if e = app.projects.replaceActivePermissions(ctx, updated, sealed); e != nil {
		t.Fatal(e)
	}
	if w := post(user); w.Code != 409 {
		t.Fatal("cleanup-only credential changed")
	}
}
