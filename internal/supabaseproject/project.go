// Package supabaseproject defines the native connection and owner installation
// for Pooltune's read-only Supabase monitor.
package supabaseproject

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
)

var projectRef = regexp.MustCompile(`^[a-z0-9]{20}$`)
var installKey = regexp.MustCompile(`^[a-f0-9]{16}$`)

type Project struct {
	ref string
	key string
}

func New(ref, key string) (Project, error) {
	if !projectRef.MatchString(ref) || !installKey.MatchString(key) {
		return Project{}, errors.New("expected a Supabase project reference and a 16-character lowercase hex installation key")
	}
	return Project{ref: ref, key: key}, nil
}

func (p Project) Ref() string  { return p.ref }
func (p Project) ID() string   { return "supabase/" + p.ref + "/" + p.key }
func (p Project) Role() string { return "pt_monitor_" + p.key }

func (p Project) Config(password string, ca []byte) (*pgx.ConnConfig, error) {
	config, err := DirectConfig(p.ref, p.Role(), password, ca)
	if err != nil {
		return nil, err
	}
	config.RuntimeParams["default_transaction_read_only"] = "on"
	config.RuntimeParams["default_transaction_isolation"] = "read committed"
	config.RuntimeParams["statement_timeout"] = "2s"
	config.RuntimeParams["application_name"] = "pooltune-project-monitor"
	return config, nil
}

// DirectConfig only accepts the canonical direct database endpoint. Project.Config
// adds the dedicated login and the normal monitor's read-only session settings.
func DirectConfig(ref, role, password string, ca []byte) (*pgx.ConnConfig, error) {
	if !projectRef.MatchString(ref) || role == "" || password == "" || os.Getenv("PGSERVICE") != "" {
		return nil, errors.New("expected a Supabase reference, database login and password with PGSERVICE unset")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("expected a trusted Supabase CA certificate")
	}
	config, err := pgx.ParseConfig("postgres://placeholder:placeholder@127.0.0.1:5432/postgres?sslmode=disable&connect_timeout=5&target_session_attrs=any")
	if err != nil {
		return nil, errors.New("cannot initialize PostgreSQL driver")
	}
	host := "db." + ref + ".supabase.co"
	config.Host, config.Port, config.Database = host, 5432, "postgres"
	config.User, config.Password = role, password
	config.TLSConfig = &tls.Config{RootCAs: roots, ServerName: host, MinVersion: tls.VersionTLS12}
	config.Fallbacks = nil
	config.RuntimeParams = make(map[string]string)
	config.DialFunc = (&net.Dialer{Timeout: 5 * time.Second}).DialContext
	config.DefaultQueryExecMode = pgx.QueryExecModeExec
	return config, nil
}
