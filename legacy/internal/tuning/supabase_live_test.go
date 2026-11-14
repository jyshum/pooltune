//go:build managedlive

package tuning

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSupabaseManagedChangeSafety(t *testing.T) {
	root := os.Getenv("POOLTUNE_MANAGED_WORK")
	if root == "" || os.Getenv("POOLTUNE_MANAGED_WRITE") != "approved-test" {
		t.Skip("isolated test authorization required")
	}
	ref := liveTargetEnv(t, "POOLTUNE_TEST_SUPABASE_PROJECT")
	raw, e := os.ReadFile(filepath.Join(root, "private-cloud-credentials/supabase-management-token.txt"))
	if e != nil {
		t.Fatal("private management token")
	}
	token := strings.TrimSpace(string(raw))
	raw, e = os.ReadFile(filepath.Join(root, "private-cloud-credentials/supabase-db-password.txt"))
	if e != nil {
		t.Fatal("private database password")
	}
	password := strings.TrimSpace(string(raw))
	api := func(method string, size int) (int, error) {
		var body io.Reader
		if method == "PATCH" {
			b, _ := json.Marshal(map[string]int{"default_pool_size": size})
			body = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, "https://api.supabase.com/v1/projects/"+ref+"/config/database/pooler", body)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		c := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		r, e := c.Do(req)
		if e != nil {
			return 0, ErrUnsafe
		}
		defer r.Body.Close()
		b, e := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if e != nil || r.StatusCode != 200 {
			return 0, ErrUnsafe
		}
		var rows []struct {
			Size int `json:"default_pool_size"`
		}
		if json.Unmarshal(b, &rows) == nil && len(rows) == 1 {
			return rows[0].Size, nil
		}
		var row struct {
			Size int `json:"default_pool_size"`
		}
		if json.Unmarshal(b, &row) != nil {
			return 0, ErrUnsafe
		}
		return row.Size, nil
	}
	original, e := api("GET", 0)
	if e != nil || original != 10 {
		t.Fatal("unexpected baseline")
	}
	type point struct {
		Seconds          float64
		Backends, Active int
	}
	type result struct {
		Original, Restored  int
		AckSeconds          float64
		Samples             []point
		Successes, Failures int
		Errors              map[string]int
		Complete            bool
	}
	out := result{Original: original, Errors: map[string]int{}}
	var mu sync.Mutex
	save := func() {
		b := encodeLiveEvidence(&mu, &out)
		os.WriteFile(filepath.Join(root, "managed-support/supabase-persistent-change.json"), b, 0600)
	}
	defer save()
	changed := false
	defer func() {
		if changed {
			_, e = api("PATCH", original)
			if e == nil {
				out.Restored, _ = api("GET", 0)
			}
		}
	}()
	cfg, e := pgx.ParseConfig("host=aws-0-us-west-2.pooler.supabase.com port=6543 user=postgres." + ref + " dbname=postgres sslmode=verify-full sslrootcert=" + filepath.Join(root, "supabase-ca.crt") + " connect_timeout=8 application_name=pooltune_managed_trial")
	if e != nil {
		t.Fatal("SQL config")
	}
	cfg.Password = password
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	mc := cfg.Copy()
	mc.Port = 5432
	monitor, e := pgx.ConnectConfig(ctx, mc)
	if e != nil {
		t.Fatal("monitor unavailable")
	}
	defer monitor.Close(context.Background())
	var others, slots int
	if monitor.QueryRow(ctx, "SELECT count(*) FILTER (WHERE backend_type='client backend' AND pid<>pg_backend_pid() AND state<>'idle'),current_setting('max_connections')::int-current_setting('superuser_reserved_connections')::int FROM pg_stat_activity").Scan(&others, &slots) != nil || others != 0 || slots < 50 {
		t.Fatal("foreign work or budget")
	}
	clients := make([]*pgx.Conn, 12)
	for i := range clients {
		clients[i], e = pgx.ConnectConfig(ctx, cfg)
		if e != nil {
			t.Fatal("client unavailable")
		}
		defer clients[i].Close(context.Background())
	}
	origin := time.Now()
	var wg sync.WaitGroup
	done := make(chan struct{})
	for _, c := range clients {
		wg.Add(1)
		go func(c *pgx.Conn) {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				qctx, cancel := context.WithTimeout(ctx, 3*time.Second)
				_, e := c.Exec(qctx, "/*pooltune-managed-safety*/ SELECT pg_sleep(0.2)")
				cancel()
				mu.Lock()
				if e == nil {
					out.Successes++
				} else {
					out.Failures++
					out.Errors[liveError(e)]++
				}
				mu.Unlock()
				if e != nil {
					return
				}
			}
		}(c)
	}
	for i := 0; i < 60; i++ {
		if i == 8 {
			changed = true
			save()
			_, e = api("PATCH", 11)
			mu.Lock()
			out.AckSeconds = time.Since(origin).Seconds()
			mu.Unlock()
			if e != nil {
				break
			}
		}
		var count, active int
		e = monitor.QueryRow(ctx, "SELECT count(*),count(*) FILTER (WHERE state='active') FROM pg_stat_activity WHERE backend_type='client backend' AND usename='postgres' AND application_name='Supavisor' AND pid<>pg_backend_pid()").Scan(&count, &active)
		if e != nil {
			mu.Lock()
			out.Errors["monitor_connection_lost"]++
			mu.Unlock()
			break
		}
		mu.Lock()
		out.Samples = append(out.Samples, point{time.Since(origin).Seconds(), count, active})
		mu.Unlock()
		time.Sleep(time.Second)
	}
	close(done)
	wg.Wait()
	out.Complete = true
	t.Logf("persistent clients: successes=%d failures=%d, samples=%d", out.Successes, out.Failures, len(out.Samples))
}

// Extracted from the live save closure so its concurrent behavior is testable offline.
func encodeLiveEvidence(mu *sync.Mutex, v any) []byte {
	mu.Lock()
	defer mu.Unlock()
	raw, _ := json.MarshalIndent(v, "", "  ")
	return raw
}
func TestLiveEvidenceSnapshotSynchronizesWithWorkers(t *testing.T) {
	var mu sync.Mutex
	out := struct{ Successes int }{0}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			mu.Lock()
			out.Successes++
			mu.Unlock()
		}
	}()
	for i := 0; i < 1000; i++ {
		if len(encodeLiveEvidence(&mu, &out)) == 0 {
			t.Fatal("missing evidence")
		}
	}
	wg.Wait()
}
