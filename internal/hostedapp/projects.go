package hostedapp

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"pooltune/internal/hostedquery"
	"pooltune/internal/supabaseproject"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxActiveProjects = 5

var (
	errProjectCapacity = errors.New("preview project limit reached")
	errProjectActive   = errors.New("disconnect before replacing the monitor credentials")
	errProjectName     = errors.New("enter a project name of up to 100 bytes")
	errProjectRef      = errors.New("enter the 20-character Supabase project reference")
)

type projectRecord struct {
	ID, Ref, Name string
	Owner, Epoch  int64
	Configured    bool
	Secret        []byte
}

func (p projectRecord) identity() (supabaseproject.Project, error) {
	return supabaseproject.New(p.Ref, p.ID)
}

type projectSecrets struct {
	Password, CA, LogsToken string
	Automatic               bool     `json:"automatic,omitempty"`
	Capabilities            []string `json:"capabilities,omitempty"`
	RepairsPaused           bool     `json:"repairs_paused,omitempty"`
}

type vault struct{ cipher.AEAD }

func newVault(key []byte) (vault, error) {
	if len(key) != 32 {
		return vault{}, errors.New("POOLTUNE_APP_KEY must encode 32 random bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return vault{}, err
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	return vault{aead}, err
}

func projectBinding(project projectRecord) []byte {
	return []byte(fmt.Sprintf("pooltune/project/v1/%d/%s/%s", project.Owner, project.Ref, project.ID))
}

func (v vault) seal(project projectRecord, data projectSecrets) ([]byte, error) {
	plain, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return v.Seal(nil, nil, plain, projectBinding(project)), nil
}

func (v vault) open(project projectRecord, sealed []byte) (projectSecrets, error) {
	var data projectSecrets
	plain, err := v.Open(nil, nil, sealed, projectBinding(project))
	if err != nil {
		return data, errors.New("project credential could not be opened")
	}
	return data, json.Unmarshal(plain, &data)
}

type projectStore struct{ db *pgxpool.Pool }

func (s projectStore) prepare(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(761994345005);
			CREATE TABLE IF NOT EXISTS pooltune_app.projects (
				id text PRIMARY KEY,
				ref text NOT NULL,
				name text NOT NULL,
				owner_id bigint NOT NULL REFERENCES pooltune_app.users(id),
				epoch bigint NOT NULL DEFAULT 0,
				secret bytea,
				created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
				UNIQUE(owner_id, ref),
				CHECK ((secret IS NOT NULL) = (epoch > 0))
			);
			ALTER TABLE pooltune_app.projects ADD COLUMN IF NOT EXISTS pending_setup bytea;
			CREATE TABLE IF NOT EXISTS pooltune_app.model_budgets (
				project_id text NOT NULL REFERENCES pooltune_app.projects(id) ON DELETE CASCADE,
				utc_day date NOT NULL,
				calls integer NOT NULL CHECK (calls>0),
				PRIMARY KEY(project_id,utc_day)
			)`)
		return err
	})
}

func (s projectStore) create(ctx context.Context, owner int64, name, ref string) (id string, err error) {
	name, ref = strings.TrimSpace(name), strings.TrimSpace(ref)
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	id = hex.EncodeToString(token[:])
	if name == "" || len(name) > 100 {
		return "", errProjectName
	}
	if _, err := supabaseproject.New(ref, id); err != nil {
		return "", errProjectRef
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT id FROM pooltune_app.users WHERE id=$1 FOR UPDATE`, owner); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM pooltune_app.projects WHERE owner_id=$1`, owner).Scan(&count); err != nil {
			return err
		}
		if count >= maxActiveProjects {
			return errProjectCapacity
		}
		_, err := tx.Exec(ctx, `INSERT INTO pooltune_app.projects(id,ref,name,owner_id) VALUES ($1,$2,$3,$4)`, id, ref, name, owner)
		return err
	})
	return id, err
}

const publicProjectColumns = `id, ref, name, owner_id, epoch, secret IS NOT NULL`

func (s projectStore) find(ctx context.Context, owner int64, id string) (projectRecord, error) {
	var project projectRecord
	err := s.db.QueryRow(ctx, `SELECT `+publicProjectColumns+`, secret FROM pooltune_app.projects WHERE owner_id=$1 AND id=$2`, owner, id).
		Scan(&project.ID, &project.Ref, &project.Name, &project.Owner, &project.Epoch, &project.Configured, &project.Secret)
	return project, err
}

func (s projectStore) list(ctx context.Context, owner int64) ([]projectRecord, error) {
	rows, err := s.db.Query(ctx, `SELECT `+publicProjectColumns+` FROM pooltune_app.projects WHERE owner_id=$1 ORDER BY created_at,id`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []projectRecord
	for rows.Next() {
		var p projectRecord
		if err := rows.Scan(&p.ID, &p.Ref, &p.Name, &p.Owner, &p.Epoch, &p.Configured); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func (s projectStore) active(ctx context.Context) ([]projectRecord, error) {
	rows, err := s.db.Query(ctx, `SELECT id, ref, name, owner_id, epoch, secret FROM pooltune_app.projects WHERE epoch>0 ORDER BY created_at,id LIMIT $1`, maxActiveProjects)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []projectRecord
	for rows.Next() {
		var p projectRecord
		if err := rows.Scan(&p.ID, &p.Ref, &p.Name, &p.Owner, &p.Epoch, &p.Secret); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func (s projectStore) activate(ctx context.Context, project projectRecord, sealed []byte) error {
	identity, err := project.identity()
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(761994345006)`); err != nil {
			return err
		}
		var epoch int64
		if err := tx.QueryRow(ctx, `SELECT epoch FROM pooltune_app.projects WHERE owner_id=$1 AND id=$2 FOR UPDATE`, project.Owner, project.ID).Scan(&epoch); err != nil {
			return err
		}
		if epoch > 0 {
			return errProjectActive
		}
		if epoch != project.Epoch {
			return errors.New("project changed during connection check")
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM pooltune_app.projects WHERE epoch>0`).Scan(&count); err != nil {
			return err
		}
		if count >= maxActiveProjects {
			return errProjectCapacity
		}
		newEpoch, err := hostedquery.ActivateProjectMonitor(ctx, tx, identity, true)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE pooltune_app.projects SET epoch=$3, secret=$4, pending_setup=NULL WHERE owner_id=$1 AND id=$2`, project.Owner, project.ID, newEpoch, sealed)
		return err
	})
}

func (s projectStore) disconnect(ctx context.Context, project projectRecord) error {
	identity, err := project.identity()
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var epoch int64
		if err := tx.QueryRow(ctx, `SELECT epoch FROM pooltune_app.projects WHERE owner_id=$1 AND id=$2 FOR UPDATE`, project.Owner, project.ID).Scan(&epoch); err != nil {
			return err
		}
		if epoch <= 0 {
			return nil
		}
		if err := hostedquery.DisconnectProjectMonitorTx(ctx, tx, identity); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE pooltune_app.projects SET epoch=-$3::bigint,secret=NULL WHERE owner_id=$1 AND id=$2`, project.Owner, project.ID, epoch)
		return err
	})
}

func (s projectStore) replaceActivePermissions(ctx context.Context, project projectRecord, secret []byte) error {
	identity, err := project.identity()
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE pooltune_app.projects SET epoch=epoch+1, secret=$4
			WHERE id=$1 AND owner_id=$2 AND epoch=$3 AND epoch>0`, project.ID, project.Owner, project.Epoch, secret)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return hostedquery.ErrProjectDisconnected
		}
		tag, err = tx.Exec(ctx, `UPDATE pooltune_worker.projects SET monitor_epoch=monitor_epoch+1
			WHERE project_id=$1 AND monitor_epoch=$2`, identity.ID(), project.Epoch)
		if err == nil && tag.RowsAffected() != 1 {
			return hostedquery.ErrProjectDisconnected
		}
		return err
	})
}

func checkProject(ctx context.Context, project projectRecord, credentials projectSecrets, policy hostedquery.SamplingPolicy) error {
	if credentials.Password == "" || len(credentials.Password) > 1024 || len(credentials.CA) > 32768 || len(credentials.LogsToken) > 512 {
		return errors.New("enter the dedicated monitor password and Supabase root certificate")
	}
	identity, err := project.identity()
	if err != nil {
		return err
	}
	config, err := identity.Config(credentials.Password, []byte(credentials.CA))
	if err != nil {
		return err
	}
	if _, err := hostedquery.NewTrafficPlanReader(credentials.LogsToken); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	target, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return errors.New("could not reach Supabase with these monitor credentials over verified TLS")
	}
	defer target.Close(context.Background())
	monitor, err := hostedquery.NewProjectMonitor(target, nil, identity, policy)
	if err != nil {
		return err
	}
	enrollment, err := monitor.VerifyEnrollment(ctx)
	if err != nil {
		return errors.New("the monitor login could not read its enrollment. Check the setup SQL and role permissions")
	}
	if len(enrollment.Queries) == 0 && !credentials.Automatic {
		return errors.New("enroll at least one application query in Supabase before connecting")
	}
	return nil
}

// Admission happens immediately before the provider request and survives restarts.
func (s projectStore) admitModel(ctx context.Context, id string, epoch int64) error {
	const maxCalls = 8
	var calls int
	err := s.db.QueryRow(ctx, `WITH current_day AS (SELECT (statement_timestamp() AT TIME ZONE 'UTC')::date AS day),
		active AS (SELECT id FROM pooltune_app.projects WHERE id=$1 AND epoch=$3 AND epoch>0 FOR UPDATE),
		cleanup AS (DELETE FROM pooltune_app.model_budgets WHERE project_id=$1 AND utc_day < (SELECT day-8 FROM current_day))
		INSERT INTO pooltune_app.model_budgets(project_id,utc_day,calls) SELECT id, day, 1 FROM current_day CROSS JOIN active
		ON CONFLICT (project_id, utc_day) DO UPDATE SET calls=model_budgets.calls+1 WHERE model_budgets.calls < $2
		RETURNING calls`, id, maxCalls, epoch).Scan(&calls)
	if errors.Is(err, pgx.ErrNoRows) {
		return hostedquery.ErrProjectModelBudget
	}
	return err
}
