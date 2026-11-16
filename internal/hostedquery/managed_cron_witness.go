package hostedquery

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type ManagedCronWitness struct {
	Project, Name, Fingerprint  string
	Generation, FenceGeneration string
	DatabaseOID, OwnerOID       uint32
	JobID                       int64
	Expires                     time.Time
}

// CreateManagedCronWitness installs only the existing SELECT 1/two-second
// acknowledgement probe. Record must durably retain its identity before commit;
// a supervising lifecycle must clean it up at expiry, interruption or disconnect.
func CreateManagedCronWitness(ctx context.Context, db *pgx.Conn, project, nonce string, expires time.Time, permission func(context.Context) error, record func(context.Context, ManagedCronWitness) error) (out ManagedCronWitness, err error) {
	if db == nil || project == "" || len(nonce) < 16 || len(nonce) > 128 || permission == nil || record == nil || !expires.After(time.Now()) || expires.After(time.Now().Add(5*time.Minute)) {
		return out, errors.New("witness requires a bounded lifetime, project grant and durable cleanup identity")
	}
	digest := sha256.Sum256([]byte(project + "/" + nonce))
	out.Project = project
	out.Name = fmt.Sprintf("pooltune_witness_%x", digest[:16])
	out.Expires = expires
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	err = pgx.BeginTxFunc(ctx, db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL search_path=pg_catalog;SET LOCAL statement_timeout='750ms';SET LOCAL lock_timeout='250ms'`); err != nil {
			return err
		}
		var pid uint32
		var absent, allowed bool
		if err := tx.QueryRow(ctx, `SELECT pg_backend_pid(),d.oid,current_user::regrole::oid,NOT EXISTS(SELECT FROM cron.job WHERE jobname=$1 AND username=current_user),
 current_user=session_user AND has_function_privilege(current_user,'cron.schedule(text,text,text)','EXECUTE') AND has_function_privilege(current_user,'cron.unschedule(bigint)','EXECUTE')
 FROM pg_database d WHERE d.datname=current_database()`, out.Name).Scan(&pid, &out.DatabaseOID, &out.OwnerOID, &absent, &allowed); err != nil {
			return err
		}
		if pid != db.PgConn().PID() || !absent || !allowed {
			return errors.New("native witness permissions unavailable or name occupied")
		}
		if err := permission(ctx); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT cron.schedule($1::text,'2 seconds'::text,'SELECT 1'::text)`, out.Name).Scan(&out.JobID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT encode(sha256(convert_to(to_jsonb(j)::text,'UTF8')),'hex'),j.xmin::text FROM cron.job j WHERE j.jobid=$1 AND j.jobname=$2 AND j.username=current_user AND j.database=current_database() AND j.active AND j.command='SELECT 1' AND j.schedule='2 seconds'`, out.JobID, out.Name).Scan(&out.Fingerprint, &out.Generation); err != nil {
			return err
		}
		if err := record(ctx, out); err != nil {
			return err
		}
		return permission(ctx)
	})
	return out, err
}

// RemoveManagedCronWitness refuses independent edits or replacement. It does
// not replay the probe, modify customer jobs, or grant database permissions.
func RemoveManagedCronWitness(ctx context.Context, db *pgx.Conn, w ManagedCronWitness) error {
	if db == nil || w.Project == "" || w.JobID <= 0 || w.DatabaseOID == 0 || w.OwnerOID == 0 || len(w.Fingerprint) != 64 || w.Generation == "" {
		return errors.New("missing recorded witness identity")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return pgx.BeginTxFunc(ctx, db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL search_path=pg_catalog;SET LOCAL statement_timeout='750ms';SET LOCAL lock_timeout='250ms'`); err != nil {
			return err
		}
		var pid uint32
		var owned bool
		if err := tx.QueryRow(ctx, `SELECT pg_backend_pid(),current_user=session_user AND current_user::regrole::oid=$1 AND (SELECT oid FROM pg_database WHERE datname=current_database())=$2`, w.OwnerOID, w.DatabaseOID).Scan(&pid, &owned); err != nil {
			return err
		}
		if pid != db.PgConn().PID() || !owned {
			return errors.New("witness cleanup native identity changed")
		}
		var fingerprint, generation string
		err := tx.QueryRow(ctx, `SELECT encode(sha256(convert_to(to_jsonb(j)::text,'UTF8')),'hex'),j.xmin::text FROM cron.job j WHERE j.jobid=$1`, w.JobID).Scan(&fingerprint, &generation)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if fingerprint != w.Fingerprint || (generation != w.Generation && generation != w.FenceGeneration) {
			return errors.New("witness changed independently; cleanup refused")
		}
		var removed bool
		if err := tx.QueryRow(ctx, `SELECT cron.unschedule($1::bigint)`, w.JobID).Scan(&removed); err != nil {
			return err
		}
		if !removed {
			return errors.New("witness removal was not confirmed")
		}
		return nil
	})
}
