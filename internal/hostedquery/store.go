package hostedquery

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

const insertReport = "INSERT INTO pooltune_lab_state.runs (report) VALUES ($1::jsonb) RETURNING id"

// PrepareState initializes the lab schema in Pooltune's separate local database.
func PrepareState(ctx context.Context, state *pgx.Conn) error {
	return pgx.BeginFunc(ctx, state, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS pooltune_lab_state"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			CREATE TABLE IF NOT EXISTS pooltune_lab_state.runs (
				id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
				recorded_at timestamptz NOT NULL DEFAULT now(),
				report jsonb NOT NULL
			)
		`)
		if err != nil {
			return err
		}
		// Exercise the real insert in a savepoint, then discard the probe row.
		probe, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		var id int64
		writeErr := probe.QueryRow(ctx, insertReport, "{}").Scan(&id)
		return errors.Join(writeErr, probe.Rollback(ctx))
	})
}

func RecordReport(ctx context.Context, state *pgx.Conn, report Report) (int64, error) {
	payload, err := json.Marshal(report)
	if err != nil {
		return 0, err
	}
	var id int64
	err = state.QueryRow(ctx, insertReport, string(payload)).Scan(&id)
	return id, err
}
