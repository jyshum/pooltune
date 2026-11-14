package telemetry

import (
	"context"
	"crypto/x509"
	"errors"
	"github.com/jackc/pgx/v5"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ErrHistory = errors.New("history storage unavailable")

type Database struct{ config *pgx.ConnConfig }

func closeHistoryConnection(c interface{ Close(context.Context) error }) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = c.Close(ctx)
}

func FromFile(path string) (*Database, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrHistory
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, ErrHistory
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
		return nil, ErrHistory
	}
	raw, e := io.ReadAll(io.LimitReader(f, 16385))
	if e != nil {
		return nil, ErrHistory
	}
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	if len(lines) != 1 || (!strings.HasPrefix(lines[0], "postgresql://") && !strings.HasPrefix(lines[0], "postgres://")) {
		return nil, ErrHistory
	}
	cfg, e := pgx.ParseConfig(lines[0])
	if e != nil || cfg.TLSConfig == nil {
		return nil, ErrHistory
	}
	// Always verify the cloud host certificate; never permit a plaintext fallback.
	if caFile := os.Getenv("POOLTUNE_HISTORY_CA_FILE"); caFile != "" {
		pem, err := os.ReadFile(caFile)
		roots := x509.NewCertPool()
		if err != nil || !roots.AppendCertsFromPEM(pem) {
			return nil, ErrHistory
		}
		cfg.TLSConfig.RootCAs = roots
	}
	cfg.TLSConfig.InsecureSkipVerify = false
	cfg.TLSConfig.ServerName = cfg.Host
	cfg.Fallbacks = nil
	cfg.ConnectTimeout = 2 * time.Second
	cfg.RuntimeParams["application_name"] = "pooltune-history"
	return &Database{cfg}, nil
}
func (d *Database) connect(ctx context.Context) (*pgx.Conn, error) {
	c, e := pgx.ConnectConfig(ctx, d.config.Copy())
	if e != nil {
		return nil, ErrHistory
	}
	return c, nil
}

const schema = `CREATE SCHEMA IF NOT EXISTS pooltune_history;
CREATE TABLE IF NOT EXISTS pooltune_history.samples (
 at timestamptz NOT NULL, stream text NOT NULL, provider text NOT NULL, epoch text NOT NULL,
 demo boolean NOT NULL, total numeric(20,0) NOT NULL, rate double precision,
 delta numeric(20,0) NOT NULL, seconds double precision NOT NULL,
 active integer NOT NULL, queued integer NOT NULL, admission_limit integer NOT NULL, warm integer NOT NULL,
 PRIMARY KEY (stream,provider,demo,epoch,at)
);
SELECT create_hypertable('pooltune_history.samples','at',if_not_exists=>TRUE);`

func (d *Database) initialize(ctx context.Context, c *pgx.Conn) error {
	_, e := c.Exec(ctx, schema)
	if e != nil {
		return ErrHistory
	}
	return nil
}
func (d *Database) write(ctx context.Context, c *pgx.Conn, stream string, s Sample) error {
	_, e := c.Exec(ctx, `INSERT INTO pooltune_history.samples (at,stream,provider,epoch,demo,total,rate,delta,seconds,active,queued,admission_limit,warm) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT DO NOTHING`, s.At, stream, s.Provider, s.Epoch, s.Demo, s.Total, s.Rate, s.Delta, s.Seconds, s.Active, s.Queued, s.Limit, s.Warm)
	if e != nil {
		return ErrHistory
	}
	return nil
}

type Bucket struct {
	At       time.Time `json:"at"`
	Provider string    `json:"provider"`
	Demo     bool      `json:"demo"`
	Rate     *float64  `json:"rate"`
	Peak     *float64  `json:"peak"`
	Active   int       `json:"active"`
	Queued   int       `json:"queued"`
	Limit    int       `json:"limit"`
	Warm     int       `json:"warm"`
	Readings int       `json:"readings"`
}

func (d *Database) query(ctx context.Context, stream string) ([]Bucket, error) {
	c, e := d.connect(ctx)
	if e != nil {
		return nil, ErrHistory
	}
	defer closeHistoryConnection(c)
	rows, e := c.Query(ctx, `SELECT time_bucket('1 minute',at) AS bucket,provider,demo,
 (sum(delta)/nullif(sum(seconds),0))::float8,max(rate),max(active),max(queued),max(admission_limit),max(warm),count(*)
 FROM pooltune_history.samples WHERE stream=$1 AND at >= now()-interval '1 hour' AND at <= now()
 GROUP BY bucket,provider,demo ORDER BY bucket LIMIT 300`, stream)
	if e != nil {
		return nil, ErrHistory
	}
	defer rows.Close()
	out := []Bucket{}
	for rows.Next() {
		var b Bucket
		if rows.Scan(&b.At, &b.Provider, &b.Demo, &b.Rate, &b.Peak, &b.Active, &b.Queued, &b.Limit, &b.Warm, &b.Readings) != nil {
			return nil, ErrHistory
		}
		out = append(out, b)
	}
	if rows.Err() != nil {
		return nil, ErrHistory
	}
	return out, nil
}
