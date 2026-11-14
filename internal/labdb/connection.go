// Package labdb restricts the hosted development commands to fixture databases.
package labdb

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	RookeryHost = "aws-0-us-west-2.pooler.supabase.com"
	RookeryUser = "postgres.osdjqwpzgyzwfaxyufze"
)

func Config(dsn string, rookery bool) (*pgx.ConnConfig, error) {
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
		u.User == nil || u.Fragment != "" {
		return nil, errors.New("expected a PostgreSQL URL")
	}
	password, hasPassword := u.User.Password()
	db := strings.TrimPrefix(u.Path, "/")
	port, portErr := strconv.ParseUint(u.Port(), 10, 16)
	if !hasPassword || password == "" || u.User.Username() == "" || db == "" ||
		strings.Contains(db, "/") || portErr != nil || port == 0 {
		return nil, errors.New("URL requires a user, password, port and database")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query) != 1 || len(query["sslmode"]) != 1 {
		return nil, errors.New("URL requires exactly one sslmode parameter")
	}

	mode := query.Get("sslmode")
	var tlsConfig *tls.Config
	if rookery {
		if u.Hostname() != RookeryHost || port != 5432 || u.User.Username() != RookeryUser ||
			db != "postgres" || mode != "verify-full" {
			return nil, errors.New("expected the Rookery session pooler with verify-full")
		}
		ca, err := os.ReadFile(os.Getenv("POOLTUNE_LAB_ROOKERY_CA_FILE"))
		roots := x509.NewCertPool()
		if err != nil || !roots.AppendCertsFromPEM(ca) {
			return nil, errors.New("supply the Supabase CA with POOLTUNE_LAB_ROOKERY_CA_FILE")
		}
		tlsConfig = &tls.Config{RootCAs: roots, ServerName: RookeryHost, MinVersion: tls.VersionTLS12}
	} else if u.Hostname() != "127.0.0.1" || mode != "disable" {
		return nil, errors.New("expected a local disposable database at 127.0.0.1 with sslmode=disable")
	}
	if os.Getenv("PGSERVICE") != "" {
		return nil, errors.New("unset PGSERVICE when running the lab")
	}
	// Start from a fixed pgx configuration, then assign only the validated connection fields.
	config, err := pgx.ParseConfig("postgres://lab:placeholder@127.0.0.1:5432/lab?sslmode=disable&target_session_attrs=any&connect_timeout=5")
	if err != nil {
		return nil, errors.New("cannot initialize PostgreSQL driver")
	}
	config.Host, config.Port, config.Database = u.Hostname(), uint16(port), db
	config.User, config.Password = u.User.Username(), password
	config.RuntimeParams = map[string]string{"application_name": "pooltune-query-lab"}
	config.Fallbacks = nil
	config.TLSConfig = tlsConfig
	config.DialFunc = (&net.Dialer{Timeout: 5 * time.Second}).DialContext
	config.DefaultQueryExecMode = pgx.QueryExecModeExec
	return config, nil
}

func Close(connection *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	connection.Close(ctx)
}

// Keep raw driver errors out of CLI output. They may contain credentials or database details.
func Diagnostic(stage string, err error) error {
	var pgError *pgconn.PgError
	switch {
	case errors.As(err, &pgError):
		return fmt.Errorf("%s failed (SQLSTATE %s)", stage, pgError.Code)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%s timed out", stage)
	default:
		return fmt.Errorf("%s failed", stage)
	}
}
