package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Transactions is a database-scoped PgBouncer counter, not a connection count,
// HTTP request count, or success-only count. Keep uint64 precise in JavaScript.
type Transactions struct {
	At    time.Time `json:"at"`
	Total uint64    `json:"total,string"`
	Epoch string    `json:"epoch"`
	Demo  bool      `json:"demo,omitempty"`
}

func transactionTotal(rows []map[string]string, database string) (uint64, error) {
	var total uint64
	found := false
	for _, row := range rows {
		if row["database"] != database {
			continue
		}
		if found {
			return 0, ErrAPI
		}
		n, e := strconv.ParseUint(row["total_xact_count"], 10, 64)
		if e != nil {
			return 0, ErrAPI
		}
		found, total = true, n
	}
	if !found {
		return 0, ErrAPI
	}
	return total, nil
}

// ReadTransactions reads only the named pooler database. The epoch binds a
// process lifetime without exposing private resource identities to browsers.
// Missing telemetry must remain nil, never a fabricated zero.
func ReadTransactions(ctx context.Context, c *pgx.Conn, database, epoch string) *Transactions {
	// SHOW STATS uses PostgreSQL numeric for unsigned totals. Read its exact
	// text representation instead of formatting pgx's pgtype.Numeric struct.
	rows, err := c.Query(ctx, "SHOW STATS", pgx.QueryExecModeSimpleProtocol)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var counters []map[string]string
	for rows.Next() {
		if len(counters) >= 10001 {
			return nil
		}
		row := map[string]string{}
		for i, field := range rows.FieldDescriptions() {
			if field.Name == "database" || field.Name == "total_xact_count" {
				if field.Format != 0 {
					return nil
				}
				row[field.Name] = string(rows.RawValues()[i])
			}
		}
		counters = append(counters, row)
	}
	if rows.Err() != nil || epoch == "" {
		return nil
	}
	n, err := transactionTotal(counters, database)
	if err != nil {
		return nil
	}
	hash := sha256.Sum256([]byte(epoch))
	return &Transactions{At: time.Now(), Total: n, Epoch: hex.EncodeToString(hash[:])}
}

// PollTransactions keeps optional chart traffic off the safety/readback socket.
// A failed or timed-out statistics query cannot poison the control connection.
func PollTransactions(ctx context.Context, config *pgx.ConnConfig, database, epoch string) *Transactions {
	ctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	c, err := pgx.ConnectConfig(ctx, config.Copy())
	if err != nil {
		return nil
	}
	defer c.Close(ctx)
	return ReadTransactions(ctx, c, database, epoch)
}
