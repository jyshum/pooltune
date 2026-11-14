package hostedquery

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const trialLock int64 = 761994345002
const trialIndex = "pooltune_lookup_idx"

const journalTable = "pooltune_lab_state.index_runs"

func prepareJournal(ctx context.Context, state *pgx.Conn) error {
	if err := PrepareState(ctx, state); err != nil {
		return err
	}
	_, err := state.Exec(ctx, `CREATE TABLE IF NOT EXISTS `+journalTable+` (
		id text PRIMARY KEY, target text NOT NULL, status text NOT NULL,
		recovered_index text, report jsonb, created_at timestamptz NOT NULL DEFAULT now()
	)`)
	return err
}

func trialSchema(id string) string { return "pooltune_trial_" + id }

func qualified(schema, name string) string { return pgx.Identifier{schema, name}.Sanitize() }

func lockTrial(ctx context.Context, target *pgx.Conn) error {
	var acquired bool
	if err := target.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", trialLock).Scan(&acquired); err != nil {
		return err
	}
	if !acquired {
		return errors.New("another trial owns the target lock")
	}
	return nil
}

func unlockTrial(ctx context.Context, target *pgx.Conn) error {
	var released bool
	if err := target.QueryRow(ctx, "SELECT pg_advisory_unlock($1)", trialLock).Scan(&released); err != nil {
		return err
	}
	if !released {
		return errors.New("trial session lost its advisory lock")
	}
	return nil
}

func writeTrialStatus(ctx context.Context, state *pgx.Conn, id, status string, recovered *string, report *IndexTrialReport) error {
	var payload *string
	if report != nil {
		bytes, err := json.Marshal(report)
		if err != nil {
			return err
		}
		raw := string(bytes)
		payload = &raw
	}
	tag, err := state.Exec(ctx, "UPDATE "+journalTable+" SET status=$2,recovered_index=$3,report=COALESCE($4::jsonb,report) WHERE id=$1", id, status, recovered, payload)
	if err == nil && tag.RowsAffected() != 1 {
		return errors.New("index action journal row missing")
	}
	return err
}

// RecoverIndexTrials serializes reconciliation with builds on the same target.
func RecoverIndexTrials(ctx context.Context, target, state *pgx.Conn, identity string) (err error) {
	if err := prepareJournal(ctx, state); err != nil {
		return err
	}
	if err := lockTrial(ctx, target); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, releaseTrialLock(target)) }()
	return reconcileTrials(ctx, target, state, journalTarget(target, identity))
}

func journalTarget(target *pgx.Conn, identity string) string {
	config := target.Config()
	return fmt.Sprintf("%s/%s:%d/%s/%s", identity, config.Host, config.Port, config.Database, config.User)
}

func reconcileTrials(ctx context.Context, target, state *pgx.Conn, identity string) error {
	rows, err := state.Query(ctx, "SELECT id FROM "+journalTable+" WHERE target=$1 AND status IN ('setup','building','verified') ORDER BY created_at", identity)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range ids {
		status, err := cleanTrial(ctx, target, id)
		if err != nil {
			return fmt.Errorf("reconcile fixture %s: %w", id, err)
		}
		if err := writeTrialStatus(ctx, state, id, "recovered", &status, nil); err != nil {
			return err
		}
	}
	return nil
}

// cleanTrial inspects the index before removing only the journal-owned fixture.
func cleanTrial(ctx context.Context, target *pgx.Conn, id string) (string, error) {
	bytes, err := hex.DecodeString(id)
	if err != nil || len(bytes) != 12 {
		return "", errors.New("invalid journal fixture identity")
	}
	schema := trialSchema(id)
	var comment *string
	err = target.QueryRow(ctx, "SELECT obj_description(oid,'pg_namespace') FROM pg_namespace WHERE nspname=$1", schema).Scan(&comment)
	if errors.Is(err, pgx.ErrNoRows) {
		return "absent", nil
	}
	if err != nil {
		return "", err
	}
	if comment == nil || *comment != "pooltune trial "+id {
		return "", errors.New("fixture ownership could not be established")
	}
	var unexpected bool
	if err := target.QueryRow(ctx, `
		SELECT EXISTS (SELECT FROM pg_class c JOIN pg_namespace n ON c.relnamespace=n.oid
			WHERE n.nspname=$1 AND c.relname NOT IN ('candidate','healthy','candidate_pkey','healthy_pkey',$2))
	`, schema, trialIndex).Scan(&unexpected); err != nil {
		return "", err
	}
	if unexpected {
		return "", errors.New("fixture contains unexpected relations")
	}
	index, err := inspectTrialIndex(ctx, target, schema)
	if err != nil {
		return "", err
	}
	observed := "absent"
	if index.Exists {
		observed = "invalid"
		if index.Valid && index.Ready && index.Matches {
			observed = "valid"
		}
		if !index.Matches {
			return "", errors.New("named index does not match the trial")
		}
	}
	err = pgx.BeginFunc(ctx, target, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout='250ms'"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DROP TABLE "+qualified(schema, "candidate")+", "+qualified(schema, "healthy")); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" RESTRICT")
		return err
	})
	return observed, err
}
