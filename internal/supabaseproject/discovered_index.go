package supabaseproject

import (
	"context"
	_ "embed"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

//go:embed discovered_index.sql
var discoveredIndexTemplate string

// ErrDiscoveredIndexEvidenceUnavailable indicates that the monitor could not
// prove the observed query's relation and keys using passive evidence.
var ErrDiscoveredIndexEvidenceUnavailable = errors.New("discovered index evidence is unavailable")

// DiscoveredIndexEvidence binds a live query counter to the exact ordinary
// relation and ordered int4 filter keys. PlanNode is a generic estimate.
type DiscoveredIndexEvidence struct {
	DatabaseOID          uint32
	RoleOID              uint32
	QueryID              int64
	TableOID             uint32
	Schema               string
	Relation             string
	Only                 bool
	Columns              []int16
	ObjectiveMS          float64
	Boot                 time.Time
	ResetAt              time.Time
	EntrySince           time.Time
	PlanNode             string
	TablePages           int32
	EstimatedTableRows   *float64
	ModifiedSinceAnalyze int64
	EstimatedDeadRows    int64
}

// DiscoveredIndexEvidenceSQL installs the optional passive reader alongside the
// automatic query snapshot. The reader grants no database repair authority.
func (p Project) DiscoveredIndexEvidenceSQL() string {
	return strings.ReplaceAll(discoveredIndexTemplate, "pooltune_monitor_role", p.Role())
}

// ReadDiscoveredIndexEvidence reads only the exact role and query identity seen
// in a fresh detector claim. Neither raw query text nor values leave PostgreSQL.
func (p Project) ReadDiscoveredIndexEvidence(ctx context.Context, monitor *pgx.Conn, roleOID uint32, queryID int64) (DiscoveredIndexEvidence, error) {
	if monitor == nil || roleOID == 0 || queryID == 0 {
		return DiscoveredIndexEvidence{}, ErrDiscoveredIndexEvidenceUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var evidence DiscoveredIndexEvidence
	err := monitor.QueryRow(ctx, "SELECT * FROM "+pgx.Identifier{p.Role(), "read_discovered_index"}.Sanitize()+"($1::oid,$2::bigint)", roleOID, queryID).
		Scan(&evidence.DatabaseOID, &evidence.RoleOID, &evidence.QueryID, &evidence.TableOID, &evidence.Schema, &evidence.Relation,
			&evidence.Only, &evidence.Columns, &evidence.ObjectiveMS, &evidence.Boot, &evidence.ResetAt, &evidence.EntrySince,
			&evidence.PlanNode, &evidence.TablePages, &evidence.EstimatedTableRows, &evidence.ModifiedSinceAnalyze, &evidence.EstimatedDeadRows)
	var pgErr *pgconn.PgError
	if errors.Is(err, pgx.ErrNoRows) || errors.As(err, &pgErr) && (pgErr.Code == "42501" || pgErr.Code == "42883") {
		return DiscoveredIndexEvidence{}, ErrDiscoveredIndexEvidenceUnavailable
	}
	return evidence, err
}
