//go:build integration

package auth

import (
	"errors"
	"sync"
	"testing"
)

func TestGooglePasswordlessMigration(t *testing.T) {
	ctx, pool, store := startAccountStore(t)
	_, oldSession, err := store.Register(ctx, "password@example.com", "existing long passphrase")
	if err != nil {
		t.Fatal(err)
	}
	// Recreate the pre-Google NOT NULL boundary with a live password account.
	if _, err := pool.Exec(ctx, `ALTER TABLE pooltune_app.users DROP CONSTRAINT password_all_or_none,
 ALTER COLUMN password_salt SET NOT NULL, ALTER COLUMN password_hash SET NOT NULL, ALTER COLUMN password_iterations SET NOT NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := New(ctx, pool); err != nil {
		t.Fatalf("legacy migration: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO pooltune_app.users(email) VALUES ('google@example.com')`); err != nil {
		t.Fatalf("passwordless account rejected: %v", err)
	}
	if _, err := New(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UserBySession(ctx, oldSession); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SignIn(ctx, "password@example.com", "existing long passphrase"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SignIn(ctx, "google@example.com", "any password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("passwordless credential result: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE pooltune_app.users SET password_salt = decode(repeat('00',16),'hex') WHERE email = 'google@example.com'`); err == nil {
		t.Fatal("partial credentials allowed")
	}
}

// Account identity must be stable by Google subject, never attached by email alone.
func TestGoogleIdentityAndExplicitLinking(t *testing.T) {
	ctx, _, store := startAccountStore(t)
	owner, ownerSession, err := store.Register(ctx, "owner@example.com", "existing long passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GoogleAccount(ctx, "signup", "owner-sub", owner.Email, 0, ""); !errors.Is(err, ErrGoogleLinkRequired) {
		t.Fatalf("silent link: %v", err)
	}
	if _, _, err := store.GoogleAccount(ctx, "signin", "unknown-sub", "new@example.com", 0, ""); !errors.Is(err, ErrGoogleUnknown) {
		t.Fatalf("unknown identity: %v", err)
	}
	if _, _, err := store.GoogleAccount(ctx, "link", "owner-sub", "different@example.com", owner.ID, ownerSession); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("email mismatch: %v", err)
	}
	linked, token, err := store.GoogleAccount(ctx, "link", "owner-sub", owner.Email, owner.ID, ownerSession)
	if err != nil || linked != owner {
		t.Fatalf("link: %+v %v", linked, err)
	}
	if got, err := store.UserBySession(ctx, token); err != nil || got != owner {
		t.Fatalf("linked session: %+v %v", got, err)
	}
	if got, _, err := store.GoogleAccount(ctx, "signin", "owner-sub", "changed@example.com", 0, ""); err != nil || got != owner {
		t.Fatalf("stable subject: %+v %v", got, err)
	}
	if err := store.SignOut(ctx, ownerSession); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GoogleAccount(ctx, "link", "other-sub", owner.Email, owner.ID, ownerSession); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("revoked session linked: %v", err)
	}
}

func TestGoogleConcurrentRegistration(t *testing.T) {
	ctx, pool, store := startAccountStore(t)
	var wg sync.WaitGroup
	ids := make(chan int64, 6)
	for range 6 {
		wg.Go(func() {
			u, _, err := store.GoogleAccount(ctx, "signup", "one-subject", "one@example.com", 0, "")
			if err != nil {
				t.Errorf("registration: %v", err)
			}
			ids <- u.ID
		})
	}
	wg.Wait()
	close(ids)
	var id int64
	for got := range ids {
		if id != 0 && id != got {
			t.Fatal("identity split")
		}
		id = got
	}
	var users, identities int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM pooltune_app.users), (SELECT count(*) FROM pooltune_app.google_identities)`).Scan(&users, &identities); err != nil || users != 1 || identities != 1 {
		t.Fatalf("counts %d %d: %v", users, identities, err)
	}
}

func TestGoogleFlowBrowserBindingExpiryAndReplay(t *testing.T) {
	ctx, pool, store := startAccountStore(t)
	flow, browser, err := store.BeginGoogleFlow(ctx, "signup", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeGoogleFlow(ctx, flow.State, "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("wrong browser accepted")
	}
	reopened, err := New(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.ConsumeGoogleFlow(ctx, flow.State, browser)
	if err != nil || got.Nonce != flow.Nonce || got.Verifier != flow.Verifier || got.Mode != "signup" {
		t.Fatalf("flow: %+v %v", got, err)
	}
	if _, err := store.ConsumeGoogleFlow(ctx, flow.State, browser); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("replay accepted")
	}
	flow, browser, err = store.BeginGoogleFlow(ctx, "signin", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE pooltune_app.google_flows SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeGoogleFlow(ctx, flow.State, browser); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("expired flow accepted")
	}
}

func TestGoogleFlowConcurrentConsumption(t *testing.T) {
	ctx, _, store := startAccountStore(t)
	flow, browser, err := store.BeginGoogleFlow(ctx, "signin", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 8)
	for range 8 {
		go func() { _, err := store.ConsumeGoogleFlow(ctx, flow.State, browser); results <- err }()
	}
	successes := 0
	for range 8 {
		if err := <-results; err == nil {
			successes++
		} else if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("consumed %d times", successes)
	}
}
