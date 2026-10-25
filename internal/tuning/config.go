package tuning

import (
	"bytes"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"io"
	"net"
	"path/filepath"
	"pooltune/internal/secrets"
	"strings"
)

type Config struct {
	Schema             string `json:"schema"`
	Target             string `json:"target"`
	Directory          string `json:"directory"`
	SecretRoot         string `json:"secret_root"`
	AdminDSNFile       string `json:"admin_dsn_file"`
	ApplicationDSNFile string `json:"application_dsn_file"`
	MonitorDSNFile     string `json:"monitor_dsn_file"`
	Listen             string `json:"listen"`
	Assets             string `json:"assets"`
	Bounds             Bounds `json:"bounds"`
	ExclusiveLocalPool bool   `json:"exclusive_local_pool"`
}

func LoadConfig(r io.Reader) (Config, error) {
	var c Config
	raw, e := io.ReadAll(io.LimitReader(r, 65537))
	if e != nil || len(raw) > 65536 {
		return c, ErrUnsafe
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(&struct{}{}) != io.EOF {
		return Config{}, ErrUnsafe
	}
	host, port, e := net.SplitHostPort(c.Listen)
	ip := net.ParseIP(host)
	if e != nil || ip == nil || !ip.IsLoopback() || port == "0" || port == "" || c.Schema != "local-tuner-1" || !identifier.MatchString(c.Target) || !c.ExclusiveLocalPool || c.Bounds.Valid() != nil {
		return Config{}, ErrUnsafe
	}
	for _, p := range []string{c.Directory, c.SecretRoot, c.AdminDSNFile, c.ApplicationDSNFile, c.MonitorDSNFile, c.Assets} {
		if !filepath.IsAbs(p) {
			return Config{}, ErrUnsafe
		}
	}
	if overlaps(c.Assets, c.Directory) || overlaps(c.Assets, c.SecretRoot) {
		return Config{}, ErrUnsafe
	}
	return c, nil
}
func overlaps(a, b string) bool {
	inside := func(parent, child string) bool {
		r, e := filepath.Rel(parent, child)
		return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
	}
	return inside(a, b) || inside(b, a)
}
func (c Config) Connections() (admin, app, monitor *pgx.ConnConfig, err error) {
	assets, e := filepath.EvalSymlinks(c.Assets)
	if e != nil {
		return nil, nil, nil, ErrUnsafe
	}
	for _, path := range []string{c.Directory, c.SecretRoot} {
		resolved, e := filepath.EvalSymlinks(path)
		if e != nil || overlaps(assets, resolved) {
			return nil, nil, nil, ErrUnsafe
		}
	}
	reader, e := secrets.Open(c.SecretRoot)
	if e != nil {
		return nil, nil, nil, ErrUnsafe
	}
	defer reader.Close()
	configs := make([]*pgx.ConnConfig, 3)
	for i, path := range []string{c.AdminDSNFile, c.ApplicationDSNFile, c.MonitorDSNFile} {
		raw, e := reader.Read(path)
		if e != nil {
			return nil, nil, nil, ErrUnsafe
		}
		configs[i], e = pgx.ParseConfig(strings.TrimSpace(string(raw)))
		if e != nil {
			return nil, nil, nil, ErrUnsafe
		}
		configs[i].ConnectTimeout = 2e9
	}
	return configs[0], configs[1], configs[2], nil
}
