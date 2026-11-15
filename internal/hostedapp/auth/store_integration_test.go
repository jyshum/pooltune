//go:build integration

package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"pooltune/internal/testpg"
)

func TestAccountSessionLifecycle(t *testing.T) {
	ctx, pool, store := startAccountStore(t)

	const email = "pilot@example.com"
	const password = " a precise passphrase "
	account, firstSession, err := store.Register(ctx, "  Pilot@Example.com  ", password)
	if err != nil || account.ID <= 0 || account.Email != email || firstSession == "" {
		t.Fatalf("register canonical account: %v", err)
	}
	if _, _, err := store.Register(ctx, "PILOT@example.com", "some other passphrase"); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate canonical email: %v", err)
	}
	if _, _, err := store.Register(ctx, "Bad <invalid@example.com>", password); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("accepted display-name address: %v", err)
	}
	if _, _, err := store.SignIn(ctx, email, "a precise passphrase"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("sign-in accepted password without its original spaces: %v", err)
	}
	if _, _, err := store.SignIn(ctx, "missing@example.com", password); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown sign-in was not generic: %v", err)
	}

	reopened, err := New(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if owner, err := reopened.UserBySession(ctx, firstSession); err != nil || owner != account {
		t.Fatalf("find persisted session owner: %v", err)
	}
	verified, secondSession, err := reopened.SignIn(ctx, email, password)
	if err != nil || verified != account || secondSession == "" || secondSession == firstSession {
		t.Fatalf("sign-in did not issue a fresh session: %v", err)
	}

	if err := store.SignOut(ctx, firstSession); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.UserBySession(ctx, firstSession); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("signed-out session remained usable: %v", err)
	}
	if owner, err := store.UserBySession(ctx, secondSession); err != nil || owner != account {
		t.Fatalf("sign-out revoked an unrelated session: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE pooltune_app.sessions SET expires_at = clock_timestamp() - interval '1 second' WHERE user_id = $1", account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UserBySession(ctx, secondSession); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expired session remained usable: %v", err)
	}
}

func TestAccountSignInThrottleSurvivesNewStore(t *testing.T) {
	ctx, pool, store := startAccountStore(t)

	const email = "throttled@example.com"
	const password = "long correct passphrase"
	if _, _, err := store.Register(ctx, email, password); err != nil {
		t.Fatal(err)
	}
	const simultaneousAttempts = 9
	results := make(chan error, simultaneousAttempts)
	for i := 0; i < simultaneousAttempts; i++ {
		go func() {
			_, _, err := store.SignIn(ctx, email, "long incorrect passphrase")
			results <- err
		}()
	}
	limited := 0
	for i := 0; i < simultaneousAttempts; i++ {
		switch err := <-results; {
		case errors.Is(err, ErrRateLimited):
			limited++
		case errors.Is(err, ErrInvalidCredentials):
		default:
			t.Fatalf("unexpected sign-in error: %v", err)
		}
	}
	if limited != 1 {
		t.Fatalf("rate limited %d concurrent attempt(s), expected one", limited)
	}

	reopened, err := New(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := reopened.SignIn(ctx, email, password); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("account throttle did not survive reopening: %v", err)
	}
	if _, _, err := reopened.Register(ctx, "other@example.com", password); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reopened.SignIn(ctx, "other@example.com", password); err != nil {
		t.Fatalf("account throttle affected a different account: %v", err)
	}
}

func startAccountStore(t *testing.T) (context.Context, *pgxpool.Pool, *Store) {
	t.Helper()
	ctx, conn, _ := testpg.Start(t)
	config, err := pgxpool.ParseConfig(conn.ConnString())
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig = conn
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := New(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, pool, store
}
