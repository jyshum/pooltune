//go:build integration

package hostedquery

import (
	"context"
	"errors"
	"pooltune/internal/testpg"
	"testing"
	"time"
)

func TestManagedCronWitnessJournalBeforeCommitAndExactCleanup(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t)
	db := openMonitorConn(t, ctx, cfg)
	if _, err := db.Exec(ctx, `CREATE SCHEMA cron;CREATE TABLE cron.job(jobid bigserial PRIMARY KEY,jobname text UNIQUE,username text,database text,command text,schedule text,active bool);
 CREATE FUNCTION cron.schedule(text,text,text) RETURNS bigint LANGUAGE plpgsql AS $$DECLARE id bigint;BEGIN INSERT INTO cron.job(jobname,username,database,command,schedule,active) VALUES($1,current_user,current_database(),$3,$2,true) RETURNING jobid INTO id;RETURN id;END$$;
 CREATE FUNCTION cron.unschedule(bigint) RETURNS bool LANGUAGE plpgsql AS $$BEGIN DELETE FROM cron.job WHERE jobid=$1;RETURN FOUND;END$$;`); err != nil {
		t.Fatal(err)
	}
	allow := func(context.Context) error { return nil }
	if _, err := CreateManagedCronWitness(ctx, db, "project", "a-long-session-nonce", time.Now().Add(time.Minute), allow, func(context.Context, ManagedCronWitness) error { return errors.New("durable state unavailable") }); err == nil {
		t.Fatal("probe committed without durable identity")
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM cron.job`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed journal left active witness")
	}
	var saved ManagedCronWitness
	w, err := CreateManagedCronWitness(ctx, db, "project", "a-long-session-nonce", time.Now().Add(time.Minute), allow, func(_ context.Context, w ManagedCronWitness) error { saved = w; return nil })
	if err != nil || saved.JobID != w.JobID {
		t.Fatalf("witness %+v %v", w, err)
	}
	if _, err := CreateManagedCronWitness(ctx, db, "project", "a-long-session-nonce", time.Now().Add(time.Minute), allow, func(context.Context, ManagedCronWitness) error { return nil }); err == nil {
		t.Fatal("existing witness overwritten")
	}
	if _, err := db.Exec(ctx, `UPDATE cron.job SET command='SELECT 99' WHERE jobid=$1`, w.JobID); err != nil {
		t.Fatal(err)
	}
	if err := RemoveManagedCronWitness(ctx, db, w); err == nil {
		t.Fatal("independently edited job removed")
	}
	if _, err := db.Exec(ctx, `UPDATE cron.job SET command='SELECT 1' WHERE jobid=$1`, w.JobID); err != nil {
		t.Fatal(err)
	}
	if err := RemoveManagedCronWitness(ctx, db, w); err == nil {
		t.Fatal("edited-and-restored job lost its independent generation")
	}
	if _, err := db.Exec(ctx, `DELETE FROM cron.job WHERE jobid=$1`, w.JobID); err != nil {
		t.Fatal(err)
	}
	w, err = CreateManagedCronWitness(ctx, db, "project", "a-new-session-nonce", time.Now().Add(time.Minute), allow, func(context.Context, ManagedCronWitness) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	// The native action intentionally fences an active witness with a same-value
	// update. Only its durably captured generation may be accepted afterward.
	if err := db.QueryRow(ctx, `UPDATE cron.job SET active=true WHERE jobid=$1 RETURNING xmin::text`, w.JobID).Scan(&w.FenceGeneration); err != nil {
		t.Fatal(err)
	}
	if err := RemoveManagedCronWitness(ctx, db, w); err != nil {
		t.Fatal(err)
	}
	if err := RemoveManagedCronWitness(ctx, db, w); err != nil {
		t.Fatal("absent witness not reconciled")
	}
}
