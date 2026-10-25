package main

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"pooltune/internal/admin"
	"pooltune/internal/domain"
	"pooltune/internal/durable"
	"pooltune/internal/provider"
	"pooltune/internal/secrets"
	"pooltune/internal/store"
	"pooltune/internal/tuning"
	"regexp"
	"strings"
	"time"
)

type managedProfile struct {
	Provider      provider.Kind `json:"provider"`
	Target        string        `json:"target"`
	Subscription  string        `json:"subscription"`
	ResourceGroup string        `json:"resource_group"`
	Instance      string        `json:"instance"`
	Project       string        `json:"project"`
	Database      string        `json:"database"`
	Role          string        `json:"role"`
	Directory     string        `json:"directory"`
	SecretRoot    string        `json:"secret_root"`
	PasswordFile  string        `json:"password_file"`
	TokenFile     string        `json:"token_file"`
	CAFile        string        `json:"ca_file"`
	Exclusive     bool          `json:"exclusive_management"`
	OtherReserve  int           `json:"other_reserve"`
	Bounds        tuning.Bounds `json:"bounds"`
}

func loadManaged(r io.Reader) (managedProfile, error) {
	var c managedProfile
	d := json.NewDecoder(io.LimitReader(r, 65537))
	d.DisallowUnknownFields()
	id := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	if d.Decode(&c) != nil || d.Decode(&struct{}{}) != io.EOF || !id.MatchString(c.Target) || !c.Exclusive || c.Bounds.Valid() != nil || c.OtherReserve < 1 || c.OtherReserve > 10000 || c.Database != "postgres" || !id.MatchString(c.Role) {
		return c, tuning.ErrUnsafe
	}
	for _, p := range []string{c.Directory, c.SecretRoot, c.PasswordFile, c.CAFile} {
		if !filepath.IsAbs(p) {
			return c, tuning.ErrUnsafe
		}
	}
	switch c.Provider {
	case provider.Azure:
		if !id.MatchString(c.Subscription) || !id.MatchString(c.ResourceGroup) || !id.MatchString(c.Instance) {
			return c, tuning.ErrUnsafe
		}
	case provider.Supabase:
		if !id.MatchString(c.Project) || !filepath.IsAbs(c.TokenFile) {
			return c, tuning.ErrUnsafe
		}
	default:
		return c, provider.ErrUnsupported
	}
	return c, nil
}
func runManaged(ctx context.Context, path string, resume bool) error {
	if !filepath.IsAbs(path) {
		return tuning.ErrUnsafe
	}
	f, e := os.Open(path)
	if e != nil {
		return tuning.ErrUnsafe
	}
	cfg, e := loadManaged(f)
	f.Close()
	if e != nil {
		return e
	}
	reader, e := secrets.Open(cfg.SecretRoot)
	if e != nil {
		return tuning.ErrUnsafe
	}
	defer reader.Close()
	token := func(ctx context.Context) (string, error) {
		if cfg.TokenFile != "" {
			raw, e := reader.Read(cfg.TokenFile)
			return strings.TrimSpace(string(raw)), e
		}
		raw, e := exec.CommandContext(ctx, "az", "account", "get-access-token", "--subscription", cfg.Subscription, "--resource", "https://management.azure.com/", "--query", "accessToken", "-o", "tsv").Output()
		if e != nil || len(raw) > 65536 {
			return "", provider.ErrAPI
		}
		return strings.TrimSpace(string(raw)), nil
	}
	api := provider.Config{Kind: cfg.Provider, Target: provider.Target{ID: cfg.Target}, Subscription: cfg.Subscription, ResourceGroup: cfg.ResourceGroup, Instance: cfg.Instance, Project: cfg.Project, Timeout: 10 * time.Second, Freshness: time.Second, Token: token}
	encoder := json.NewEncoder(os.Stdout)
	if cfg.Provider == provider.Supabase {
		// The persistent-client test observed 57P01 on every client after resize.
		// Reading this provider is supported; automatic hot changes remain refused.
		a, e := provider.NewDiscovery(api)
		if e != nil {
			return e
		}
		if resume {
			return provider.ErrUnsupported
		}
		for {
			tick, cancel := context.WithTimeout(ctx, 12*time.Second)
			o, oe := a.Observe(tick, api.Target)
			p, pe := a.Pressure(tick)
			cancel()
			report := map[string]any{"provider": "supabase", "target": cfg.Target, "automation_enabled": false, "status": "mutation_blocked", "gaps": []string{"Live resize disconnected all persistent test clients with SQLSTATE57P01; no automatic Supavisor writes", "Database PgBouncer metrics do not measure shared Supavisor waiting clients"}, "configuration_verified": oe == nil, "pressure_verified": pe == nil}
			if oe == nil {
				report["parameters"] = o.Parameters
			}
			if pe == nil {
				report["database_pressure"] = p
			}
			encoder.Encode(report)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(15 * time.Second):
			}
		}
	}
	password, e := reader.Read(cfg.PasswordFile)
	if e != nil {
		return tuning.ErrUnsafe
	}
	host := cfg.Instance + ".postgres.database.azure.com"
	u := url.URL{Scheme: "postgres", User: url.User(cfg.Role), Host: net.JoinHostPort(host, "5432"), Path: "/" + cfg.Database, RawQuery: url.Values{"sslmode": {"verify-full"}, "sslrootcert": {cfg.CAFile}, "connect_timeout": {"3"}, "application_name": {"pooltune_managed_monitor"}}.Encode()}
	monitor, e := pgx.ParseConfig(u.String())
	if e != nil {
		return tuning.ErrUnsafe
	}
	monitor.Password = strings.TrimSpace(string(password))
	adminConn := monitor.Copy()
	adminConn.Port = 6432
	adminConn.Database = "pgbouncer"
	files, e := durable.Open(cfg.Directory)
	if e != nil {
		return e
	}
	defer files.Close()
	j, e := store.OpenStore(filepath.Join(cfg.Directory, "control.db"))
	if e != nil {
		return e
	}
	defer j.Close()
	p, e := provider.NewAzurePool(provider.AzurePoolConfig{API: api, Admin: adminConn, Monitor: monitor, Database: cfg.Database, Role: cfg.Role, Maximum: cfg.Bounds.Maximum, OtherReserve: cfg.OtherReserve, ExclusiveManagement: cfg.Exclusive, Journal: j})
	if e != nil {
		return e
	}
	defer p.Close()
	s, e := tuning.NewManaged(cfg.Target, cfg.Bounds, p, j, files, p.Source())
	if e != nil {
		return e
	}
	var pressure provider.Pressure
	lastPressure := time.Time{}
	resumed := false
	for {
		tick, cancel := context.WithTimeout(ctx, 12*time.Second)
		if time.Since(lastPressure) > 5*time.Second {
			pressure, e = p.Pressure(tick)
			if e != nil {
				pressure = provider.Pressure{}
			}
			lastPressure = time.Now()
		}
		s.Tick(tick, time.Now(), domain.Resources{At: pressure.At, Source: pressure.Source, CPU: pressure.CPU, Memory: pressure.Memory})
		v := s.View(time.Now())
		if resume && !resumed && v.Ready {
			_, e = s.Execute(tick, "resume", admin.Command{ExpectedRevision: v.Revision, IdempotencyKey: "cli-resume-" + v.Revision})
			if e == nil {
				resumed = true
			}
		}
		cancel()
		encoder.Encode(s.View(time.Now()))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
}
