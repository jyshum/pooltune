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
const trialDefinition = "nonunique btree candidate (lookup), default ordering and operator class"

const journalTable = "pooltune_lab_state.index_runs"

func prepareJournal(ctx context.Context, state *pgx.Conn) error {
	if err := PrepareState(ctx, state); err != nil {
		return err
	}
	_, err := state.Exec(ctx, `CREATE TABLE IF NOT EXISTS `+journalTable+` (
		id text PRIMARY KEY, target text NOT NULL, status text NOT NULL,
		recovered_index text, report jsonb, created_at timestamptz NOT NULL DEFAULT now()
	)`)
	if err != nil {
		return err
	}
	_, err = state.Exec(ctx, `ALTER TABLE `+journalTable+`
		ADD COLUMN IF NOT EXISTS objects jsonb,
		ADD COLUMN IF NOT EXISTS index_name text,
		ADD COLUMN IF NOT EXISTS index_definition text`)
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
	return journalTargetConfig(target.Config(), identity)
}

func journalTargetConfig(config *pgx.ConnConfig, identity string) string {
	return fmt.Sprintf("%s/%s:%d/%s/%s", identity, config.Host, config.Port, config.Database, config.User)
}

func reconcileTrials(ctx context.Context, target, state *pgx.Conn, identity string) error {
	rows, err := state.Query(ctx, "SELECT id FROM "+journalTable+" WHERE target=$1 AND status IN ('setup','building','verified','declined') ORDER BY created_at", identity)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range ids {
		status, err := cleanTrial(ctx, target, state, id)
		if err != nil {
			return fmt.Errorf("reconcile fixture %s: %w", id, err)
		}
		if err := writeTrialStatus(ctx, state, id, "recovered", &status, nil); err != nil {
			return err
		}
	}
	return nil
}

type fixtureObjects struct {
	Schema      uint32 `json:"schema"`
	Owner       uint32 `json:"owner"`
	Candidate   uint32 `json:"candidate"`
	Healthy     uint32 `json:"healthy"`
	CandidatePK uint32 `json:"candidate_pk"`
	HealthyPK   uint32 `json:"healthy_pk"`
}

type trialReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadFixture(ctx context.Context, db trialReader, id string) (fixtureObjects, error) {
	var ids fixtureObjects
	err := db.QueryRow(ctx, `
		SELECT n.oid,n.nspowner,c.oid,h.oid,cp.oid,hp.oid
		FROM pg_namespace n JOIN pg_class c ON c.relnamespace=n.oid AND c.relname='candidate'
		JOIN pg_class h ON h.relnamespace=n.oid AND h.relname='healthy'
		JOIN pg_class cp ON cp.relnamespace=n.oid AND cp.relname='candidate_pkey'
		JOIN pg_class hp ON hp.relnamespace=n.oid AND hp.relname='healthy_pkey'
		JOIN pg_index ci ON ci.indexrelid=cp.oid AND ci.indrelid=c.oid AND ci.indisprimary
		JOIN pg_index hi ON hi.indexrelid=hp.oid AND hi.indrelid=h.oid AND hi.indisprimary
		WHERE n.nspname=$1 AND obj_description(n.oid,'pg_namespace')=$2
		AND n.nspowner=current_user::regrole::oid AND c.relowner=n.nspowner AND h.relowner=n.nspowner
		AND NOT EXISTS (SELECT FROM aclexplode(COALESCE(n.nspacl,acldefault('n',n.nspowner))) acl
			WHERE acl.grantee<>n.nspowner)
		AND c.relkind='r' AND h.relkind='r' AND NOT c.relispartition AND NOT h.relispartition
	`, trialSchema(id), "pooltune trial "+id).Scan(&ids.Schema, &ids.Owner, &ids.Candidate, &ids.Healthy, &ids.CandidatePK, &ids.HealthyPK)
	return ids, err
}

func expectedFixture(ctx context.Context, state *pgx.Conn, id string) (fixtureObjects, error) {
	var raw []byte
	var name, definition *string
	err := state.QueryRow(ctx, "SELECT objects,index_name,index_definition FROM "+journalTable+" WHERE id=$1", id).Scan(&raw, &name, &definition)
	if err != nil {
		return fixtureObjects{}, err
	}
	var ids fixtureObjects
	if name == nil || *name != trialIndex || definition == nil || *definition != trialDefinition ||
		json.Unmarshal(raw, &ids) != nil || ids.Candidate == 0 || ids.Healthy == 0 {
		return ids, errors.New("fixture has no matching durable ownership and index intent")
	}
	return ids, nil
}

// cleanTrial checks journal identities and attached relations under locks before dropping the fixture.
func cleanTrial(ctx context.Context, target, state *pgx.Conn, id string) (string, error) {
	bytes, err := hex.DecodeString(id)
	if err != nil || len(bytes) != 12 {
		return "", errors.New("invalid journal fixture identity")
	}
	schema := trialSchema(id)
	var exists bool
	if err := target.QueryRow(ctx, "SELECT to_regnamespace($1) IS NOT NULL", schema).Scan(&exists); err != nil {
		return "", err
	}
	if !exists {
		return "absent", nil
	}
	expected, err := expectedFixture(ctx, state, id)
	if err != nil {
		return "", err
	}
	var status string
	if err := state.QueryRow(ctx, "SELECT status FROM "+journalTable+" WHERE id=$1", id).Scan(&status); err != nil {
		return "", err
	}
	var observed string
	err = pgx.BeginFunc(ctx, target, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout='250ms'"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout='2s'"); err != nil {
			return err
		}
		candidate, healthy := qualified(schema, "candidate"), qualified(schema, "healthy")
		if _, err := tx.Exec(ctx, "LOCK TABLE ONLY "+candidate+", ONLY "+healthy+" IN ACCESS EXCLUSIVE MODE"); err != nil {
			return err
		}
		actual, err := loadFixture(ctx, tx, id)
		if err != nil || actual != expected {
			return errors.Join(errors.New("fixture catalog identity changed"), err)
		}
		var unexpected bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT FROM pg_class WHERE relnamespace=$1 AND oid<>ALL($2) AND relname<>$3)
			   OR EXISTS (SELECT FROM pg_index WHERE indrelid=ANY($4) AND indexrelid<>ALL($5)
			              AND indexrelid IS DISTINCT FROM to_regclass($6))
			   OR EXISTS (SELECT FROM pg_trigger WHERE tgrelid=ANY($4) AND NOT tgisinternal)
			   OR EXISTS (SELECT FROM pg_inherits WHERE inhparent=ANY($4) OR inhrelid=ANY($4))
		`, actual.Schema, []uint32{actual.Candidate, actual.Healthy, actual.CandidatePK, actual.HealthyPK}, trialIndex,
			[]uint32{actual.Candidate, actual.Healthy}, []uint32{actual.CandidatePK, actual.HealthyPK}, qualified(schema, trialIndex)).Scan(&unexpected); err != nil {
			return err
		}
		if unexpected {
			return errors.New("fixture contains unexpected relations or triggers")
		}
		index, err := inspectTrialIndex(ctx, tx, schema)
		if err != nil {
			return err
		}
		observed = "absent"
		if index.Exists {
			if status == "setup" || status == "declined" || status == "consumer_ready" || status == "queued_declined" {
				return errors.New("index exists before a build was authorized")
			}
			if !index.Matches {
				return errors.New("named index does not match the trial")
			}
			observed = "invalid"
			if index.Valid && index.Ready {
				observed = "valid"
			}
		}
		if _, err := tx.Exec(ctx, "DROP TABLE "+candidate+", "+healthy+" RESTRICT"); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" RESTRICT")
		return err
	})
	return observed, err
}
