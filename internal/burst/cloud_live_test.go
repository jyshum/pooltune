//go:build managedlive

package burst

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"pooltune/internal/provider"
	"strings"
	"sync"
	"testing"
	"time"
)

type cloudProfile struct {
	BackendIP                                                                                      string
	Kind, Host, User, Database, SQLRole, CA, PasswordFile, TokenFile, Project, Instance, Directory string
	Port, Base, Ceiling, Clients, SteadyRPS, BurstRPS, OtherReserve, ProviderLimit                 int
}
type cloudOutcome struct {
	Phase, Error string
	LatencyMS    float64
}
type cloudPoint struct {
	Health                    Health
	Pressure                  provider.Pressure
	Seconds                   float64
	State                     State
	Active, Queued, Uncertain int
}
type cloudTrial struct {
	Name            string
	Complete        bool
	SetupError      string
	Outcomes        []cloudOutcome
	Points          []cloudPoint
	WarmConnections int
	Seconds         float64
}

func TestCloudSpike(t *testing.T) {
	file := os.Getenv("POOLTUNE_BURST_PROFILE")
	if file == "" || os.Getenv("POOLTUNE_BURST_APPROVED") != "yes" {
		t.Skip("explicit live profile required")
	}
	raw, e := os.ReadFile(file)
	if e != nil {
		t.Fatal(e)
	}
	var p cloudProfile
	if json.Unmarshal(raw, &p) != nil {
		t.Fatal("profile")
	}
	if p.Clients <= p.Ceiling || p.BurstRPS <= p.Base*5 || p.BurstRPS > p.Ceiling*5 || p.SteadyRPS > p.Base*4 {
		t.Fatal("workload must cross base capacity without exceeding ceiling")
	}
	password, e := os.ReadFile(p.PasswordFile)
	if e != nil {
		t.Fatal("password file")
	}
	backend, e := pgx.ParseConfig(fmt.Sprintf("host=%s port=%d user=%s dbname=%s sslmode=verify-full sslrootcert=%s connect_timeout=5", p.Host, p.Port, p.User, p.Database, p.CA))
	if e != nil {
		t.Fatal("database profile")
	}
	backend.Password = strings.TrimSpace(string(password))
	backend.Fallbacks = nil
	backend.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pc := provider.Config{Kind: provider.Kind(p.Kind), Target: provider.Target{ID: "burst-" + p.Kind}, Project: p.Project, Instance: p.Instance, Timeout: 5 * time.Second, Freshness: time.Minute}
	source := ""
	age := 120 * time.Second
	switch pc.Kind {
	case provider.Supabase:
		source = "supabase:" + p.Project
		age = 75 * time.Second
		pc.Token = func(context.Context) (string, error) {
			v, e := os.ReadFile(p.TokenFile)
			return strings.TrimSpace(string(v)), e
		}
	case provider.Azure:
		age = provider.AzurePressureMaxAge
		pc.Subscription = os.Getenv("POOLTUNE_TEST_AZURE_SUBSCRIPTION")
		pc.ResourceGroup = os.Getenv("POOLTUNE_TEST_AZURE_RESOURCE_GROUP")
		if pc.Subscription == "" || pc.ResourceGroup == "" {
			t.Fatal("POOLTUNE_TEST_AZURE_SUBSCRIPTION and POOLTUNE_TEST_AZURE_RESOURCE_GROUP required")
		}
		source = "azure:https://management.azure.com/subscriptions/" + pc.Subscription + "/resourceGroups/" + pc.ResourceGroup + "/providers/Microsoft.DBforPostgreSQL/flexibleServers/" + p.Instance
		pc.Token = func(ctx context.Context) (string, error) {
			v, e := exec.CommandContext(ctx, "/opt/homebrew/bin/az", "account", "get-access-token", "--query", "accessToken", "-o", "tsv").Output()
			return strings.TrimSpace(string(v)), e
		}
	case provider.Google:
		age = provider.GooglePressureMaxAge
		source = "google:" + p.Project + ":" + p.Instance
		pc.Token = func(ctx context.Context) (string, error) {
			v, e := exec.CommandContext(ctx, "/opt/homebrew/bin/gcloud", "auth", "print-access-token").Output()
			return strings.TrimSpace(string(v)), e
		}
	default:
		t.Fatal("unsupported live provider")
	}
	adapter, e := provider.NewDiscovery(pc)
	if e != nil {
		t.Fatal(e)
	}
	// Cache auth tokens for this short run, never write or print them.
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	token, e := pc.Token(ctx)
	if e != nil {
		t.Fatal("cloud authentication")
	}
	pc.Token = func(context.Context) (string, error) { return token, nil }
	adapter, e = provider.NewDiscovery(pc)
	if e != nil {
		t.Fatal(e)
	}
	cert, key := cloudCertificate(t, p.Directory)
	trials := []cloudTrial{}
	save := func() {
		b, _ := json.MarshalIndent(trials, "", "  ")
		if e := os.WriteFile(filepath.Join(p.Directory, "results.json"), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	modes, e := cloudModes(os.Getenv("POOLTUNE_BURST_MODE"))
	if e != nil {
		t.Fatal(e)
	}
	for _, mode := range modes {
		tr := cloudTrial{Name: mode}
		trials = append(trials, tr)
		idx := len(trials) - 1
		save()
		base, ceiling := p.Base, p.Ceiling
		if mode == "fixed-base" {
			ceiling = base
		}
		if mode == "fixed-ceiling" {
			base = ceiling
		}
		dir := filepath.Join(p.Directory, mode)
		if e = os.Mkdir(dir, 0700); e != nil {
			t.Fatal(e)
		}

		// Runtime lifetime is ctx, startup is bounded through each network operation.
		settings := Settings{Control: Config{Target: "burst-" + p.Kind, Base: base, Ceiling: ceiling, Queue: 64, QueueWait: 2 * time.Second, PressureSource: source, PressureMaxAge: age}, Backend: backend, Monitor: backend, SQLRole: p.SQLRole, OtherReserve: p.OtherReserve, ProviderLimit: p.ProviderLimit, Exclusive: true, Directory: dir, Listen: "127.0.0.1:0", CertificateFile: cert, KeyFile: key, UpstreamCA: p.CA, Pressure: adapter.Pressure}

		settings.BackendIP = p.BackendIP
		settings.VerifyTarget = func(ctx context.Context, b, m *pgx.ConnConfig) error {
			for _, v := range []*pgx.ConnConfig{b, m} {
				if e := adapter.VerifySQLTarget(ctx, v.Host, v.TLSConfig.ServerName, v.User); e != nil {
					return e
				}
			}
			return nil
		}
		r, e := Start(ctx, settings)
		if e != nil {
			trials[idx].SetupError = e.Error()
			save()
			t.Fatal(e)
		}
		if e = r.Controller.Resume(time.Now()); e != nil {
			r.Close()
			trials[idx].SetupError = e.Error()
			save()
			t.Fatal(e)
		}
		out := runCloudTraffic(t, ctx, r, p, mode == "burst")
		out.Name = mode
		out.WarmConnections = ceiling
		trials[idx] = out
		save()
		if e = r.Close(); e != nil {
			t.Fatal(e)
		}
		t.Logf("%s complete: %d outcomes", mode, len(out.Outcomes))
	}
}
func cloudCertificate(t *testing.T, dir string) (string, string) {
	t.Helper()
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "Pooltune private test gateway"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	crt, k := filepath.Join(dir, "gateway.crt"), filepath.Join(dir, "gateway.key")
	if e = os.WriteFile(crt, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(k, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); e != nil {
		t.Fatal(e)
	}
	return crt, k
}
func runCloudTraffic(t *testing.T, ctx context.Context, r *Runtime, p cloudProfile, gateway bool) cloudTrial {
	clientConfig := r.ClientConfig()
	if !gateway {
		clientConfig = r.keeper.Config().Copy()
	}
	t.Helper()
	result := cloudTrial{}
	var mu sync.Mutex
	clients := make([]*pgx.Conn, p.Clients)
	for i := range clients {
		c, e := pgx.ConnectConfig(ctx, clientConfig)
		if e != nil {
			for _, v := range clients {
				if v != nil {
					v.Close(context.Background())
				}
			}
			r.Close()
			t.Fatal("gateway client authentication failed")
		}
		clients[i] = c
	}
	type job struct {
		At    time.Time
		Phase string
	}
	jobs := make(chan job, 64)
	var wg sync.WaitGroup
	add := func(v cloudOutcome) { mu.Lock(); result.Outcomes = append(result.Outcomes, v); mu.Unlock() }
	for _, c := range clients {
		wg.Add(1)
		go func(c *pgx.Conn) {
			defer wg.Done()
			defer func() { c.Close(context.Background()) }()
			for j := range jobs {
				if c.IsClosed() && ctx.Err() == nil {
					// A new connection serves only future requests. Never replay failed SQL.
					next, err := pgx.ConnectConfig(ctx, clientConfig)
					if err == nil {
						c = next
					}
				}
				q, done := context.WithDeadline(ctx, j.At.Add(3*time.Second))
				_, e := c.Exec(q, "SELECT pg_sleep(0.2)")
				done()
				kind := ""
				if e != nil {
					kind = "transport_or_deadline"
					if pe, ok := e.(*pgconn.PgError); ok {
						kind = "sqlstate:" + pe.Code
					}
				}
				add(cloudOutcome{Phase: j.Phase, Error: kind, LatencyMS: float64(time.Since(j.At).Microseconds()) / 1000})
			}
		}(c)
	}
	start := time.Now()
	sampleDone := make(chan struct{})
	sampleStop := make(chan struct{})
	go func() {
		defer close(sampleDone)
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-sampleStop:
				return
			case now := <-tick.C:
				s := r.Controller.Scheduler.Stats()
				v := cloudPoint{Seconds: now.Sub(start).Seconds(), State: r.Controller.State(), Active: s.Active, Queued: s.Queued, Uncertain: s.Uncertain}
				r.Controller.mu.Lock()
				v.Health = r.Controller.health
				r.Controller.mu.Unlock()
				r.mu.Lock()
				v.Pressure = r.pressure
				r.mu.Unlock()
				mu.Lock()
				result.Points = append(result.Points, v)
				mu.Unlock()
			}
		}
	}()
	for sec := 0; sec < 60; sec++ {
		rate, phase := p.SteadyRPS, "steady"
		if sec >= 10 && sec < 20 {
			rate, phase = p.BurstRPS, "first-spike"
		}
		if sec >= 25 {
			rate, phase = p.BurstRPS, "sustained"
		}
		if sec >= 20 && sec < 25 {
			rate = 0
		}
		for i := 0; i < rate; i++ {
			at := start.Add(time.Duration(sec)*time.Second + time.Duration(i)*time.Second/time.Duration(rate))
			if d := time.Until(at); d > 0 {
				time.Sleep(d)
			}
			select {
			case jobs <- job{at, phase}:
			default:
				add(cloudOutcome{Phase: phase, Error: "generator_queue_full"})
			}
		}
	}
	close(jobs)
	wg.Wait()
	close(sampleStop)
	<-sampleDone
	result.Seconds = time.Since(start).Seconds()
	result.Complete = true
	return result
}

func cloudModes(mode string) ([]string, error) {
	switch mode {
	case "":
		return []string{"fixed-base", "burst", "fixed-ceiling"}, nil
	case "burst":
		return []string{"burst"}, nil
	default:
		return nil, fmt.Errorf("unsupported live mode")
	}
}
