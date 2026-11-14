package burst

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"pooltune/internal/admin"
	"pooltune/internal/durable"
	"pooltune/internal/provider"
	"strings"
	"testing"
	"time"
)

func TestDashboardReportsActualCapacityWithoutProviderIdentifiers(t *testing.T) {
	now := time.Now()
	c, e := NewController(Config{Target: "orders", Base: 2, Ceiling: 4, Queue: 8, QueueWait: time.Second, PressureSource: "azure:private-subscription", PressureMaxAge: 135 * time.Second}, nil)
	if e != nil {
		t.Fatal(e)
	}
	c.Update(Health{At: now, PressureAt: now.Add(-100 * time.Second), Source: "azure:private-subscription", CPU: .2, Memory: .3, Warm: 4, BudgetVerified: true})
	if e = c.Resume(now); e != nil {
		t.Fatal(e)
	}
	b := DashboardBackend{Controller: c}
	v, e := b.Read(context.Background(), "state")
	if e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(raw), "private-subscription") {
		t.Fatal("provider resource identity exposed")
	}
	var view DashboardState
	if e = json.Unmarshal(raw, &view); e != nil {
		t.Fatal(e)
	}
	if view.Mode != "managed-burst" || view.Provider != "azure" || view.Effective != 2 || view.Warm != 4 || !view.Ready || view.PressureAgeSeconds < 100 {
		t.Fatalf("wrong live snapshot: %+v", view)
	}
	c.Update(Health{})
	v, e = b.Read(context.Background(), "state")
	if e != nil {
		t.Fatal(e)
	}
	view = v.(DashboardState)
	if view.Ready || view.HealthVerified {
		t.Fatal("missing health shown as ready")
	}
	if _, e = b.Execute(context.Background(), "resume", admin.Command{}); e == nil {
		t.Fatal("read-only view accepted a mutation")
	}
}

func TestPrivateDashboardRequiresClaimAndRejectsSecretAssetRoots(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	assets := filepath.Join(dir, "ui")
	secrets := filepath.Join(dir, "secrets")
	for _, p := range []string{state, assets, secrets} {
		if e := os.Mkdir(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.WriteFile(filepath.Join(assets, "index.html"), []byte("private managed dashboard"), 0600); e != nil {
		t.Fatal(e)
	}
	files, e := durable.Open(state)
	if e != nil {
		t.Fatal(e)
	}
	defer files.Close()
	c, e := NewController(Config{Target: "orders", Base: 1, Ceiling: 2, Queue: 4, QueueWait: time.Second, PressureSource: "supabase:project", PressureMaxAge: 75 * time.Second}, nil)
	if e != nil {
		t.Fatal(e)
	}
	r := Runtime{cfg: Settings{Directory: state}, files: files, Controller: c}
	if _, e = r.StartDashboard("0.0.0.0:0", assets, secrets); e == nil {
		t.Fatal("public dashboard allowed")
	}
	if _, e = r.StartDashboard("127.0.0.1:0", dir, secrets); e == nil {
		t.Fatal("asset root containing secrets allowed")
	}
	d, e := r.StartDashboard("127.0.0.1:0", assets, secrets)
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	response, e := http.Get(d.URL + "/api/state")
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("unclaimed state leaked")
	}
	claim, e := files.Load("claim.token")
	if e != nil {
		t.Fatal(e)
	}
	body, _ := json.Marshal(map[string]string{"claim_token": string(claim), "password": "private-dashboard-test-password"})
	req, _ := http.NewRequest("POST", d.URL+"/api/setup/claim", bytes.NewReader(body))
	req.Header.Set("Origin", d.URL)
	req.Header.Set("Content-Type", "application/json")
	response, e = http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	if response.StatusCode != 200 {
		t.Fatalf("claim returned %d", response.StatusCode)
	}
	cookies := response.Cookies()
	response.Body.Close()
	if len(cookies) != 1 || !cookies[0].HttpOnly {
		t.Fatal("private session cookie missing")
	}
	req, _ = http.NewRequest("GET", d.URL+"/api/state", nil)
	req.AddCookie(cookies[0])
	response, e = http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	var view DashboardState
	if e = json.NewDecoder(response.Body).Decode(&view); e != nil {
		t.Fatal(e)
	}
	if response.StatusCode != 200 || view.Mode != "managed-burst" || !view.Frozen {
		t.Fatalf("unexpected authenticated state: %+v", view)
	}
}

func TestDashboardTransactionTelemetryForAllManagedProviders(t *testing.T) {
	for _, kind := range []string{"azure", "google", "supabase"} {
		t.Run(kind, func(t *testing.T) {
			now := time.Now()
			c, e := NewController(Config{Target: "orders", Base: 1, Ceiling: 2, Queue: 4, QueueWait: time.Second, PressureSource: kind + ":private", PressureMaxAge: 75 * time.Second}, nil)
			if e != nil {
				t.Fatal(e)
			}
			counter := &provider.Transactions{At: now, Total: 9007199254740993, Epoch: "opaque-process-epoch"}
			c.Update(Health{Transactions: counter, At: now, PressureAt: now, Source: kind + ":private", CPU: .2, Memory: .2, Warm: 2, BudgetVerified: true})
			b := DashboardBackend{Controller: c}
			v, e := b.Read(context.Background(), "state")
			if e != nil {
				t.Fatal(e)
			}
			view := v.(DashboardState)
			if view.Provider != kind || view.Transactions == nil || view.Transactions.Total != counter.Total {
				t.Fatalf("missing provider counter: %+v", view)
			}
			raw, _ := json.Marshal(v)
			if !strings.Contains(string(raw), `"total":"9007199254740993"`) {
				t.Fatal("counter lost precision")
			}
			c.Update(Health{})
			v, _ = b.Read(context.Background(), "state")
			if v.(DashboardState).Transactions != nil {
				t.Fatal("missing source invented zero")
			}
		})
	}
}
