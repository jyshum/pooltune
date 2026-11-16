package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"
)

func PrepareManagedCronState(ctx context.Context, state *pgx.Conn) error {
	_, err := state.Exec(ctx, `CREATE TABLE IF NOT EXISTS pooltune_worker.managed_cron_observations(
 project_id text NOT NULL REFERENCES pooltune_worker.projects(project_id),epoch bigint NOT NULL,job_id bigint NOT NULL,observed_at timestamptz NOT NULL,payload jsonb NOT NULL,
 PRIMARY KEY(project_id,epoch,job_id,observed_at));
 CREATE TABLE IF NOT EXISTS pooltune_worker.managed_cron_witnesses(
 project_id text NOT NULL REFERENCES pooltune_worker.projects(project_id),name text NOT NULL,epoch bigint NOT NULL,payload jsonb NOT NULL,removed_at timestamptz,PRIMARY KEY(project_id,name));
 REVOKE ALL ON pooltune_worker.managed_cron_observations,pooltune_worker.managed_cron_witnesses FROM PUBLIC`)
	return err
}

func SaveManagedCronObservation(ctx context.Context, state *pgx.Conn, project string, epoch int64, job int64, retention time.Duration, o ManagedCronObservation) error {
	if project == "" || epoch <= 0 || job <= 0 || o.At.IsZero() || len(o.Counters) > 64 || len(o.Blocks) > 16 || len(o.Runs) > 64 || retention < time.Minute || retention > 4*time.Hour {
		return errors.New("invalid bounded scheduling observation")
	}
	payload, err := json.Marshal(o)
	if err != nil || len(payload) > 256<<10 {
		return errors.New("scheduling observation exceeded its storage bound")
	}
	return pgx.BeginTxFunc(ctx, state, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var current string
		if err := tx.QueryRow(ctx, `SELECT project_id FROM pooltune_worker.projects WHERE project_id=$1 AND monitor_epoch=$2 FOR UPDATE`, project, epoch).Scan(&current); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO pooltune_worker.managed_cron_observations(project_id,epoch,job_id,observed_at,payload) VALUES($1,$2,$3,$4,$5::jsonb) ON CONFLICT DO NOTHING`, project, epoch, job, o.At, string(payload)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM pooltune_worker.managed_cron_observations WHERE project_id=$1 AND (epoch<>$2 OR observed_at<$3)`, project, epoch, o.At.Add(-retention))
		return err
	})
}

func ReadManagedCronObservations(ctx context.Context, state *pgx.Conn, project string, epoch int64, job int64, since time.Time) ([]ManagedCronObservation, error) {
	rows, err := state.Query(ctx, `SELECT o.payload FROM pooltune_worker.managed_cron_observations o JOIN pooltune_worker.projects p USING(project_id) WHERE o.project_id=$1 AND o.epoch=$2 AND p.monitor_epoch=$2 AND o.job_id=$3 AND o.observed_at>=$4 ORDER BY o.observed_at LIMIT 2048`, project, epoch, job, since)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (o ManagedCronObservation, err error) {
		var raw []byte
		err = row.Scan(&raw)
		if err == nil {
			err = json.Unmarshal(raw, &o)
		}
		return
	})
}

// SaveManagedCronWitness is the durable callback used before native creation
// commits. It survives epochs because exact cleanup remains required after pause.
func SaveManagedCronWitness(ctx context.Context, state *pgx.Conn, project string, epoch int64, w ManagedCronWitness) error {
	if project == "" || w.Project != project || w.JobID <= 0 || len(w.Fingerprint) != 64 || w.Generation == "" || w.Expires.IsZero() {
		return errors.New("invalid cleanup identity")
	}
	payload, err := json.Marshal(w)
	if err != nil {
		return err
	}
	tag, err := state.Exec(ctx, `INSERT INTO pooltune_worker.managed_cron_witnesses(project_id,name,epoch,payload) SELECT project_id,$3,$2,$4::jsonb FROM pooltune_worker.projects WHERE project_id=$1 AND monitor_epoch=$2 ON CONFLICT DO NOTHING`, project, epoch, w.Name, string(payload))
	if err == nil && tag.RowsAffected() != 1 {
		return errClaimLost
	}
	return err
}

func PendingManagedCronWitnesses(ctx context.Context, state *pgx.Conn, project string) ([]ManagedCronWitness, error) {
	rows, err := state.Query(ctx, `SELECT payload FROM pooltune_worker.managed_cron_witnesses WHERE project_id=$1 AND removed_at IS NULL ORDER BY name LIMIT 16`, project)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (w ManagedCronWitness, err error) {
		var raw []byte
		err = row.Scan(&raw)
		if err == nil {
			err = json.Unmarshal(raw, &w)
		}
		return
	})
}

func SettleManagedCronWitness(ctx context.Context, state *pgx.Conn, w ManagedCronWitness) error {
	payload, err := json.Marshal(w)
	if err != nil {
		return err
	}
	tag, err := state.Exec(ctx, `UPDATE pooltune_worker.managed_cron_witnesses SET removed_at=clock_timestamp() WHERE project_id=$1 AND name=$2 AND payload=$3::jsonb AND removed_at IS NULL`, w.Project, w.Name, string(payload))
	if err == nil && tag.RowsAffected() != 1 {
		return errors.New("witness cleanup journal changed")
	}
	return err
}

// SaveManagedCronWitnessFence retains the original generation if the customer
// transaction aborts and the exact proposed generation if it commits. It must
// commit independently before that transaction is allowed to commit.
func SaveManagedCronWitnessFence(ctx context.Context, state *pgx.Conn, w ManagedCronWitness, generation string) error {
	if w.Generation == "" || w.FenceGeneration != "" || generation == "" || generation == w.Generation {
		return errors.New("invalid witness fence generation")
	}
	before, err := json.Marshal(w)
	if err != nil {
		return err
	}
	updated := w
	updated.FenceGeneration = generation
	after, err := json.Marshal(updated)
	if err != nil {
		return err
	}
	tag, err := state.Exec(ctx, `UPDATE pooltune_worker.managed_cron_witnesses SET payload=$4::jsonb WHERE project_id=$1 AND name=$2 AND payload=$3::jsonb AND removed_at IS NULL`, w.Project, w.Name, string(before), string(after))
	if err == nil && tag.RowsAffected() != 1 {
		return errors.New("witness cleanup identity changed before fence")
	}
	return err
}
