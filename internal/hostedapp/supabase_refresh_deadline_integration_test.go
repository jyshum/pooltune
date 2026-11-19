//go:build integration

package hostedapp

import (
	"bytes"
	"context"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"pooltune/internal/hostedquery"
	"pooltune/internal/testpg"
	"sync/atomic"
	"testing"
	"time"
)

func TestSupabaseRefreshPersistsAfterRequestDeadline(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t)
	pc, _ := pgxpool.ParseConfig(cfg.ConnString())
	pc.ConnConfig = cfg
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	app, err := New(ctx, pool, Config{Origin: "https://preview.example.com", Key: bytes.Repeat([]byte{23}, 32), Sampling: hostedquery.SamplingPolicy{Interval: time.Minute, Window: 5 * time.Minute}, QueueInterval: time.Second, SupabaseClientID: "client", SupabaseClientSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	user, _, err := app.accounts.Register(ctx, "deadline@example.com", "one exact test passphrase")
	if err != nil {
		t.Fatal(err)
	}
	id, err := app.saveSupabaseGrant(ctx, user.ID, supabaseGrant{Access: "before", Refresh: "old-refresh", ExpiresIn: 1, Type: "Bearer"})
	if err != nil {
		t.Fatal(err)
	}
	var rotated atomic.Bool
	var networkDelay atomic.Int64
	var exchanges atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if delay := time.Duration(networkDelay.Load()); delay > 0 {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(delay):
			}
		}
		exchanges.Add(1)
		if rotated.Load() && r.Form.Get("refresh_token") == "old-refresh" {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"invalid_grant"}`)
			return
		}
		rotated.Store(true)
		fmt.Fprint(w, `{"access_token":"after","refresh_token":"new-refresh","expires_in":3600,"token_type":"Bearer"}`)
	}))
	defer server.Close()
	app.supabase.endpoint = server.URL
	// Simulate delayed local persistence after the provider has already rotated.
	_, err = pool.Exec(ctx, `CREATE FUNCTION pooltune_app.delay_refresh() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.15); RETURN NEW; END $$;CREATE TRIGGER delay_refresh BEFORE UPDATE ON pooltune_app.supabase_connections FOR EACH ROW EXECUTE FUNCTION pooltune_app.delay_refresh()`)
	if err != nil {
		t.Fatal(err)
	}
	limited, cancel := context.WithTimeout(ctx, 70*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, firstErr := app.supabaseToken(limited, user.ID, id)
	if firstErr != nil {
		t.Errorf("rotated token was not persisted after request deadline: %v", firstErr)
	}
	if elapsed := time.Since(started); elapsed > 2070*time.Millisecond {
		t.Errorf("refresh exceeded request plus persistence budget: %v", elapsed)
	}
	if !rotated.Load() {
		t.Fatal("fixture did not rotate token")
	}
	_, err = pool.Exec(ctx, `DROP TRIGGER delay_refresh ON pooltune_app.supabase_connections`)
	if err != nil {
		t.Fatal(err)
	}
	_, retryErr := app.supabaseToken(ctx, user.ID, id)
	if retryErr != nil {
		t.Fatalf("request deadline lost rotated refresh token: first=%v retry=%v", firstErr, retryErr)
	}
	t.Run("commit uses the persistence reserve", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `UPDATE pooltune_app.supabase_connections SET expires_at=now()-interval '1 minute' WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `CREATE CONSTRAINT TRIGGER delay_refresh AFTER UPDATE ON pooltune_app.supabase_connections DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pooltune_app.delay_refresh()`); err != nil {
			t.Fatal(err)
		}
		defer func() { _, _ = pool.Exec(ctx, `DROP TRIGGER delay_refresh ON pooltune_app.supabase_connections`) }()
		limited, cancel := context.WithTimeout(ctx, 70*time.Millisecond)
		defer cancel()
		before := exchanges.Load()
		if _, err := app.supabaseToken(limited, user.ID, id); err != nil {
			t.Fatalf("commit lost rotated token at caller deadline: %v", err)
		}
		if _, err := app.supabaseToken(ctx, user.ID, id); err != nil || exchanges.Load() != before+1 {
			t.Fatalf("retry did not reuse committed rotation: %v", err)
		}
	})
	t.Run("network cannot spend persistence reserve", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `UPDATE pooltune_app.supabase_connections SET expires_at=now()-interval '1 minute' WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
		networkDelay.Store(int64(500 * time.Millisecond))
		defer networkDelay.Store(0)
		limited, cancel := context.WithTimeout(ctx, 70*time.Millisecond)
		defer cancel()
		start := time.Now()
		before := exchanges.Load()
		if _, err := app.supabaseToken(limited, user.ID, id); err != errSupabaseUnavailable {
			t.Fatalf("late network admitted: %v", err)
		}
		if elapsed := time.Since(start); elapsed > 300*time.Millisecond || exchanges.Load() != before {
			t.Fatalf("network used local persistence reserve: %v", elapsed)
		}
	})
	t.Run("local persistence is capped at two seconds", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION pooltune_app.delay_refresh() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(3); RETURN NEW; END $$;CREATE TRIGGER delay_refresh BEFORE UPDATE ON pooltune_app.supabase_connections FOR EACH ROW EXECUTE FUNCTION pooltune_app.delay_refresh()`); err != nil {
			t.Fatal(err)
		}
		defer func() { _, _ = pool.Exec(ctx, `DROP TRIGGER delay_refresh ON pooltune_app.supabase_connections`) }()
		limited, cancel := context.WithTimeout(ctx, 70*time.Millisecond)
		defer cancel()
		start := time.Now()
		if _, err := app.supabaseToken(limited, user.ID, id); err != errSupabaseUnavailable {
			t.Fatalf("persistence outage misclassified: %v", err)
		}
		if elapsed := time.Since(start); elapsed < 1900*time.Millisecond || elapsed > 2300*time.Millisecond {
			t.Fatalf("request plus persistence budget not bounded: %v", elapsed)
		}
		var revoked bool
		if err := pool.QueryRow(ctx, `SELECT revoked FROM pooltune_app.supabase_connections WHERE id=$1`, id).Scan(&revoked); err != nil || revoked {
			t.Fatalf("local persistence failure revoked grant: %v", err)
		}
	})

}
