package hostedapp

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	_ "embed"
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

//go:embed free_plan.sql
var freePlanSchema string

var (
	errProjectCapacity = errors.New("preview project limit reached")
	errProjectActive   = errors.New("disconnect before replacing the monitor credentials")
	errProjectName     = errors.New("enter a project name of up to 100 bytes")
	errProjectRef      = errors.New("enter the 20-character Supabase project reference")
)

type projectRecord struct {
	ID, Ref, Name                   string
	SupabaseConnection, SupabaseOrg string
	Owner, Epoch                    int64
	Configured                      bool
	SupabaseBlocked                 bool
	AgentPaused                     bool
	Secret                          []byte
}

func (p projectRecord) identity() (supabaseproject.Project, error) {
	return supabaseproject.New(p.Ref, p.ID)
}

type projectSecrets struct {
	Password, CA, LogsToken string
	RepairPassword          string             `json:"repair_password,omitempty"`
	ObjectiveMS             int                `json:"objective_ms,omitempty"`
	Automatic               bool               `json:"automatic,omitempty"`
	Capabilities            []string           `json:"capabilities,omitempty"`
	RepairsPaused           bool               `json:"repairs_paused,omitempty"`
	DisconnectPending       bool               `json:"disconnect_pending,omitempty"`
	Enrollments             []repairEnrollment `json:"repair_enrollments,omitempty"`
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
            ALTER TABLE pooltune_app.projects ADD COLUMN IF NOT EXISTS agent_paused boolean NOT NULL DEFAULT false;
            ALTER TABLE pooltune_app.projects ADD COLUMN IF NOT EXISTS pause_cleanup boolean NOT NULL DEFAULT false;
			CREATE TABLE IF NOT EXISTS pooltune_app.repair_worker_links (
				project_id text PRIMARY KEY REFERENCES pooltune_app.projects(id) ON DELETE CASCADE,
				token_digest bytea NOT NULL CHECK (octet_length(token_digest)=32),
				report_epoch bigint,
				report jsonb,
				reported_at timestamptz
			);
			CREATE TABLE IF NOT EXISTS pooltune_app.model_budgets (
				project_id text NOT NULL REFERENCES pooltune_app.projects(id) ON DELETE CASCADE,
				utc_day date NOT NULL,
				calls integer NOT NULL CHECK (calls>0),
				PRIMARY KEY(project_id,utc_day)
			)`)
		if err == nil {
			_, err = tx.Exec(ctx, freePlanSchema)
		}
		return err
	})
}

func (s projectStore) create(ctx context.Context, owner int64, name, ref string, choices ...string) (id string, err error) {
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
		_, err := tx.Exec(ctx, `INSERT INTO pooltune_app.projects(id,ref,name,owner_id,setup_capabilities) VALUES($1,$2,$3,$4,$5)`, id, ref, name, owner, append([]string{}, choices...))
		return err
	})
	return id, err
}

const publicProjectColumns = `id, ref, name, owner_id, epoch, secret IS NOT NULL, coalesce(supabase_connection,''), supabase_org, supabase_blocked, agent_paused`

func (s projectStore) find(ctx context.Context, owner int64, id string) (projectRecord, error) {
	var project projectRecord
	err := s.db.QueryRow(ctx, `SELECT `+publicProjectColumns+`, secret FROM pooltune_app.projects WHERE owner_id=$1 AND id=$2`, owner, id).
		Scan(&project.ID, &project.Ref, &project.Name, &project.Owner, &project.Epoch, &project.Configured, &project.SupabaseConnection, &project.SupabaseOrg, &project.SupabaseBlocked, &project.AgentPaused, &project.Secret)
	return project, err
}

func (s projectStore) list(ctx context.Context, owner int64) ([]projectRecord, error) {
	rows, err := s.db.Query(ctx, `SELECT `+publicProjectColumns+` FROM pooltune_app.projects p WHERE owner_id=$1 ORDER BY lower(name),id`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []projectRecord
	for rows.Next() {
		var p projectRecord
		if err := rows.Scan(&p.ID, &p.Ref, &p.Name, &p.Owner, &p.Epoch, &p.Configured, &p.SupabaseConnection, &p.SupabaseOrg, &p.SupabaseBlocked, &p.AgentPaused); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func (s projectStore) active(ctx context.Context) ([]projectRecord, error) {
	rows, err := s.db.Query(ctx, `SELECT id, ref, name, owner_id, epoch, secret, coalesce(supabase_connection,''), supabase_org, supabase_blocked, agent_paused FROM pooltune_app.projects WHERE epoch>0 AND NOT supabase_blocked AND (NOT agent_paused OR pause_cleanup) ORDER BY created_at,id LIMIT $1`, maxActiveProjects)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []projectRecord
	for rows.Next() {
		var p projectRecord
		if err := rows.Scan(&p.ID, &p.Ref, &p.Name, &p.Owner, &p.Epoch, &p.Secret, &p.SupabaseConnection, &p.SupabaseOrg, &p.SupabaseBlocked, &p.AgentPaused); err != nil {
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
		if project.SupabaseConnection != "" {
			var matches bool
			if err := tx.QueryRow(ctx, `SELECT supabase_connection=$3 AND supabase_org=$4 AND NOT supabase_blocked FROM pooltune_app.projects WHERE id=$1 AND owner_id=$2`, project.ID, project.Owner, project.SupabaseConnection, project.SupabaseOrg).Scan(&matches); err != nil {
				return err
			}
			if !matches {
				return errors.New("Supabase connection changed during setup. Retry")
			}
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
		if epoch != project.Epoch {
			return hostedquery.ErrProjectDisconnected
		}
		if epoch <= 0 {
			_, err := tx.Exec(ctx, `UPDATE pooltune_app.projects SET pending_setup=NULL WHERE owner_id=$1 AND id=$2`, project.Owner, project.ID)
			return err
		}
		if err := hostedquery.DisconnectProjectMonitorTx(ctx, tx, identity); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM pooltune_app.repair_worker_links WHERE project_id=$1`, project.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE pooltune_app.projects SET epoch=-$3::bigint,secret=NULL,pending_setup=NULL,agent_paused=false,pause_cleanup=false WHERE owner_id=$1 AND id=$2`, project.Owner, project.ID, epoch)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM pooltune_app.supabase_cli_credentials WHERE ref=$1 AND NOT EXISTS(SELECT FROM pooltune_app.projects WHERE ref=$1 AND (epoch>0 OR pending_setup IS NOT NULL))`, project.Ref)
		return err
	})
}

// Negative worker epochs also fence supervisors from older application builds.
const advanceWorkerEpochSQL = `UPDATE pooltune_worker.projects
    SET monitor_epoch=CASE WHEN agent_paused THEN -($2::bigint+1) ELSE $2::bigint+1 END
    WHERE project_id=$1 AND monitor_epoch=CASE WHEN agent_paused THEN -$2::bigint ELSE $2::bigint END`

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
		tag, err = tx.Exec(ctx, advanceWorkerEpochSQL, identity.ID(), project.Epoch)
		if err == nil && tag.RowsAffected() != 1 {
			return hostedquery.ErrProjectDisconnected
		}
		return err
	})
}

func checkProject(ctx context.Context, project projectRecord, credentials projectSecrets, policy hostedquery.SamplingPolicy, requireDiscoveredIndex bool) error {
	if credentials.Password == "" || len(credentials.Password) > 1024 || len(credentials.CA) > 32768 || len(credentials.LogsToken) > 16384 {
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
	if _, err := hostedquery.NewTrafficPlanReader(credentials.LogsToken); err != nil && project.SupabaseConnection == "" {
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
	if requireDiscoveredIndex {
		var available bool
		err := target.QueryRow(ctx, "SELECT coalesce(has_function_privilege(session_user,to_regprocedure($1),'EXECUTE'),false)",
			identity.Role()+".read_discovered_index(oid,bigint)").Scan(&available)
		if err != nil || !available {
			return errors.New("the monitor cannot read the evidence needed for the selected index repair")
		}
	}
	return nil
}

// Preflight skips investigations that cannot run. The provider transport makes the
// binding reservation once it knows the outbound request size.
func (s projectStore) admitModel(ctx context.Context, id string, epoch int64) error {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pooltune_app.projects p
 CROSS JOIN LATERAL pooltune_app.free_plan_usage(p.owner_id) u
 WHERE p.id=$1 AND p.epoch=$2 AND p.epoch>0 AND NOT p.agent_paused AND NOT p.supabase_blocked
 AND u.credits<50 AND u.account_micros<250000 AND u.pool_micros<45000000)`, id, epoch).Scan(&ok)
	if err == nil && !ok {
		return hostedquery.ErrProjectModelBudget
	}
	return err
}
