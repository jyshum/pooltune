package burst

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"io"
	"path/filepath"
	"pooltune/internal/provider"
	"pooltune/internal/secrets"
	"strings"
	"time"
)

// Profile contains paths to private mounted secrets, never inline credentials.
type Profile struct {
	BackendIP       string        `json:"backend_ip"`
	Schema          string        `json:"schema"`
	Target          string        `json:"target"`
	Directory       string        `json:"directory"`
	SecretRoot      string        `json:"secret_root"`
	BackendDSN      string        `json:"backend_dsn_file"`
	MonitorDSN      string        `json:"monitor_dsn_file"`
	TokenFile       string        `json:"provider_token_file"`
	Listen          string        `json:"listen"`
	CertificateFile string        `json:"certificate_file"`
	KeyFile         string        `json:"key_file"`
	UpstreamCA      string        `json:"upstream_ca"`
	SQLRole         string        `json:"sql_role"`
	Base            int           `json:"base"`
	Ceiling         int           `json:"ceiling"`
	Queue           int           `json:"queue"`
	QueueWaitMS     int           `json:"queue_wait_ms"`
	OtherReserve    int           `json:"other_reserve"`
	ProviderLimit   int           `json:"provider_limit"`
	Exclusive       bool          `json:"exclusive_budget"`
	Provider        provider.Kind `json:"provider"`
	Project         string        `json:"project"`
	Instance        string        `json:"instance"`
	Subscription    string        `json:"subscription"`
	ResourceGroup   string        `json:"resource_group"`
}

func LoadProfile(r io.Reader) (Profile, error) {
	var p Profile
	raw, e := io.ReadAll(io.LimitReader(r, 65537))
	if e != nil || len(raw) > 65536 {
		return p, ErrUnsafe
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(&struct{}{}) != io.EOF {
		return Profile{}, ErrUnsafe
	}
	if p.Schema != "burst-gateway-1" || !atom.MatchString(p.Target) || !p.Exclusive || p.Base < 1 || p.Ceiling < p.Base || p.Ceiling > 1000 || p.Queue < 1 || p.Queue > 10000 || p.QueueWaitMS < 1 || p.QueueWaitMS > 30000 || p.ProviderLimit < p.Ceiling || p.OtherReserve < 1 {
		return Profile{}, ErrUnsafe
	}
	for _, v := range []string{p.Directory, p.SecretRoot, p.BackendDSN, p.MonitorDSN, p.TokenFile, p.CertificateFile, p.KeyFile, p.UpstreamCA} {
		if !filepath.IsAbs(v) {
			return Profile{}, ErrUnsafe
		}
	}
	if p.Provider != provider.Azure && p.Provider != provider.Google && p.Provider != provider.Supabase {
		return Profile{}, ErrUnsafe
	}
	return p, nil
}
func (p Profile) Settings() (Settings, error) {
	reader, e := secrets.Open(p.SecretRoot)
	if e != nil {
		return Settings{}, ErrUnsafe
	}
	defer reader.Close()
	configs := make([]*pgx.ConnConfig, 2)
	for i, path := range []string{p.BackendDSN, p.MonitorDSN} {
		raw, e := reader.Read(path)
		if e != nil {
			return Settings{}, ErrUnsafe
		}
		configs[i], e = pgx.ParseConfig(strings.TrimSpace(string(raw)))
		if e != nil || !verified(configs[i]) {
			return Settings{}, ErrUnsafe
		}
		configs[i].ConnectTimeout = 3 * time.Second
	}
	// Validate private key and token with the rooted secret reader as well.
	if _, e = reader.Read(p.KeyFile); e != nil {
		return Settings{}, ErrUnsafe
	}
	if _, e = reader.Read(p.TokenFile); e != nil {
		return Settings{}, ErrUnsafe
	}
	token := func(context.Context) (string, error) {
		r, e := secrets.Open(p.SecretRoot)
		if e != nil {
			return "", e
		}
		defer r.Close()
		v, e := r.Read(p.TokenFile)
		return strings.TrimSpace(string(v)), e
	}
	pc := provider.Config{Kind: p.Provider, Target: provider.Target{ID: p.Target}, Project: p.Project, Instance: p.Instance, Subscription: p.Subscription, ResourceGroup: p.ResourceGroup, Timeout: 5 * time.Second, Freshness: time.Minute, Token: token}
	a, e := provider.NewDiscovery(pc)
	if e != nil {
		return Settings{}, e
	}
	source := ""
	age := 120 * time.Second
	switch p.Provider {
	case provider.Supabase:
		source = "supabase:" + p.Project
		age = 75 * time.Second
	case provider.Google:
		age = provider.GooglePressureMaxAge
		source = "google:" + p.Project + ":" + p.Instance
	case provider.Azure:
		age = provider.AzurePressureMaxAge
		source = "azure:https://management.azure.com/subscriptions/" + p.Subscription + "/resourceGroups/" + p.ResourceGroup + "/providers/Microsoft.DBforPostgreSQL/flexibleServers/" + p.Instance
	}
	c := Settings{Control: Config{Target: p.Target, Base: p.Base, Ceiling: p.Ceiling, Queue: p.Queue, QueueWait: time.Duration(p.QueueWaitMS) * time.Millisecond, PressureSource: source, PressureMaxAge: age}, Backend: configs[0], Monitor: configs[1], SQLRole: p.SQLRole, OtherReserve: p.OtherReserve, ProviderLimit: p.ProviderLimit, Exclusive: p.Exclusive, Directory: p.Directory, Listen: p.Listen, CertificateFile: p.CertificateFile, KeyFile: p.KeyFile, UpstreamCA: p.UpstreamCA, Pressure: a.Pressure}
	c.BackendIP = p.BackendIP
	c.VerifyTarget = func(ctx context.Context, b, m *pgx.ConnConfig) error {
		for _, v := range []*pgx.ConnConfig{b, m} {
			if e := a.VerifySQLTarget(ctx, v.Host, v.TLSConfig.ServerName, v.User); e != nil {
				return e
			}
		}
		return nil
	}
	return c, c.validate()
}
