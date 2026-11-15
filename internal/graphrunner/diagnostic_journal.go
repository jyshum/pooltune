package graphrunner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"
)

// Go persists evidence receipts before returning them to the graph. A graph
// restart can restore these exact measurements without repeating customer work.
type diagnosticJournal struct {
	conn  *pgx.Conn
	input DiagnosticInput
}

func openDiagnosticJournal(ctx context.Context, config *pgx.ConnConfig, input DiagnosticInput) (*diagnosticJournal, error) {
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return nil, errors.New("diagnostic state unavailable")
	}
	journal := &diagnosticJournal{conn, input}
	ok := false
	defer func() {
		if !ok {
			journal.close()
		}
	}()
	var locked bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended('pooltune-diagnostic/' || $1,0))`, input.RunID).Scan(&locked); err != nil || !locked {
		return nil, errors.New("diagnostic run already active")
	}
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(738210,27)`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS pooltune_diagnostic;
   CREATE TABLE IF NOT EXISTS pooltune_diagnostic.go_runs(run_id text PRIMARY KEY,input_hash text NOT NULL,model_attempts integer NOT NULL DEFAULT 0);
   ALTER TABLE pooltune_diagnostic.go_runs ADD COLUMN IF NOT EXISTS model_attempts integer NOT NULL DEFAULT 0;
   CREATE TABLE IF NOT EXISTS pooltune_diagnostic.go_evidence(run_id text NOT NULL REFERENCES pooltune_diagnostic.go_runs(run_id),tool text NOT NULL,observation jsonb NOT NULL,PRIMARY KEY(run_id,tool));`)
		return err
	})
	if err != nil {
		return nil, errors.New("diagnostic journal setup failed")
	}
	payload, err := json.Marshal(input)
	if err != nil || len(payload) > 60000 {
		return nil, errors.New("invalid diagnostic input")
	}
	hash := sha256.Sum256(payload)
	digest := hex.EncodeToString(hash[:])
	if _, err = conn.Exec(ctx, `INSERT INTO pooltune_diagnostic.go_runs(run_id,input_hash) VALUES($1,$2) ON CONFLICT DO NOTHING`, input.RunID, digest); err != nil {
		return nil, errors.New("diagnostic input journal failed")
	}
	var previous string
	if err = conn.QueryRow(ctx, `SELECT input_hash FROM pooltune_diagnostic.go_runs WHERE run_id=$1`, input.RunID).Scan(&previous); err != nil || previous != digest {
		return nil, errors.New("diagnostic run input changed")
	}
	ok = true
	return journal, nil
}
func (j *diagnosticJournal) close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	j.conn.Close(ctx)
}
func (j *diagnosticJournal) reader(source DiagnosticReader) DiagnosticReader {
	return func(ctx context.Context, tool string) (DiagnosticObservation, error) {
		if !diagnosticTools[tool] {
			return DiagnosticObservation{}, errors.New("unknown evidence tool")
		}
		var raw []byte
		err := j.conn.QueryRow(ctx, `SELECT observation FROM pooltune_diagnostic.go_evidence WHERE run_id=$1 AND tool=$2`, j.input.RunID, tool).Scan(&raw)
		if err == nil {
			var o DiagnosticObservation
			if strictJSON(raw, &o) != nil || !validObservation(o, j.input, tool) {
				return o, errors.New("invalid saved evidence receipt")
			}
			return o, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return DiagnosticObservation{}, errors.New("evidence receipt lookup failed")
		}
		o := DiagnosticObservation{DiagnosticIdentity: j.input.DiagnosticIdentity, Tool: tool, At: time.Now().UTC(), Status: "unavailable"}
		if source != nil {
			o, err = source(ctx, tool)
			if err != nil {
				return o, err
			}
		}
		if !validObservation(o, j.input, tool) {
			return o, errors.New("invalid collected evidence")
		}
		raw, err = json.Marshal(o)
		if err != nil || len(raw) > 60000 {
			return o, errors.New("oversized evidence receipt")
		}
		if _, err = j.conn.Exec(ctx, `INSERT INTO pooltune_diagnostic.go_evidence(run_id,tool,observation) VALUES($1,$2,$3)`, j.input.RunID, tool, string(raw)); err != nil {
			return o, errors.New("evidence receipt save failed")
		}
		return o, nil
	}
}

func (j *diagnosticJournal) reserveModel(ctx context.Context) (int, error) {
	var attempts int
	err := j.conn.QueryRow(ctx, `UPDATE pooltune_diagnostic.go_runs SET model_attempts=model_attempts+1 WHERE run_id=$1 AND model_attempts<3 RETURNING model_attempts`, j.input.RunID).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return 3, errors.New("model attempt budget exhausted")
	}
	return attempts, err
}
