package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"pooltune/internal/supabaseproject"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// LookupMeasuredPlan describes one owner-operated read-only measurement. It
// intentionally contains no query text, identifiers, inputs or plan predicates.
type LookupMeasuredPlan struct {
	CapturedAt       time.Time `json:"captured_at"`
	Node             string    `json:"node"`
	EstimatedRows    int64     `json:"estimated_rows"`
	ActualRows       int64     `json:"actual_rows"`
	FilteredRows     int64     `json:"filtered_rows"`
	SharedHitBlocks  int64     `json:"shared_hit_blocks"`
	SharedReadBlocks int64     `json:"shared_read_blocks"`
	ExecutionMS      float64   `json:"execution_ms"`
}

func readProjectLookupPlan(ctx context.Context, target *pgx.Conn, project supabaseproject.Project, role uint32, query int64) (*LookupMeasuredPlan, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var at time.Time
	var raw []byte
	err := target.QueryRow(ctx, "SELECT * FROM "+qualified(project.Role(), "read_lookup_plan")+"($1::oid,$2::bigint)", role, query).Scan(&at, &raw)
	var pgErr *pgconn.PgError
	if errors.Is(err, pgx.ErrNoRows) || (errors.As(err, &pgErr) && (pgErr.Code == "42501" || pgErr.Code == "42883")) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	age := time.Since(at)
	if at.IsZero() || age < 0 || age > 2*time.Minute || len(raw) > 2048 {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != 7 {
		return nil, nil
	}
	for _, key := range []string{"node", "estimated_rows", "actual_rows", "filtered_rows", "shared_hit_blocks", "shared_read_blocks", "execution_ms"} {
		if value, exists := fields[key]; !exists || string(value) == "null" {
			return nil, nil
		}
	}
	var sample LookupMeasuredPlan
	if err := json.Unmarshal(raw, &sample); err != nil {
		return nil, nil
	}
	switch sample.Node {
	case "Seq Scan", "Index Scan", "Index Only Scan":
	default:
		return nil, nil
	}
	if sample.EstimatedRows < 0 || sample.ActualRows < 0 || sample.FilteredRows < 0 ||
		sample.SharedHitBlocks < 0 || sample.SharedReadBlocks < 0 || !nonnegativeFinite(sample.ExecutionMS) || sample.ExecutionMS > 750 {
		return nil, nil
	}
	sample.CapturedAt = at.UTC()
	return &sample, nil
}
