package hostedapp

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/hostedquery"
	"pooltune/internal/labdb"
	"pooltune/internal/supabaseproject"
)

type repairEnrollmentCatalog struct {
	Functions, Jobs []repairEnrollmentTarget
	CronUnavailable string
	Truncated       bool
	Scheduling      map[int64]hostedquery.ManagedCronHistory
}

func (a *App) loadRepairEnrollmentCatalog(ctx context.Context, row projectRecord, secrets projectSecrets) (repairEnrollmentCatalog, error) {
	var empty repairEnrollmentCatalog
	if !row.Configured || row.Epoch <= 0 || secrets.RepairPassword == "" && row.SupabaseConnection == "" {
		return empty, errors.New("connect managed repair access before enrolling a target")
	}
	project, err := row.identity()
	if err != nil {
		return empty, err
	}
	ctx, cfg, err := a.repairContext(ctx, row, secrets)
	if err != nil {
		return empty, err
	}
	ctx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	db, err := supabaseproject.ConnectRepair(ctx, cfg)
	if err != nil {
		return empty, err
	}
	defer labdb.Close(db)
	if err := project.VerifyManagedRepair(ctx, db); err != nil {
		return empty, err
	}
	catalog, err := readRepairEnrollmentCatalog(ctx, db)
	if err != nil {
		return catalog, err
	}
	catalog.Scheduling = make(map[int64]hostedquery.ManagedCronHistory)
	for _, enrollment := range secrets.Enrollments {
		target := enrollment.Target
		if target.Kind != "cron" || enrollment.Window == nil || !catalog.contains(target) {
			continue
		}
		history, err := hostedquery.ReadManagedCronHistory(ctx, db, hostedquery.ManagedCronEnrollment{
			Target:      hostedquery.ProjectCronTarget{Project: project.ID(), DatabaseOID: target.DatabaseOID, OwnerOID: target.OwnerOID, JobID: target.JobID, Name: target.Name, CommandDigest: target.CommandDigest},
			Fingerprint: target.Fingerprint, Generation: target.Generation, Schedule: target.Schedule, Window: *enrollment.Window,
		})
		if err == nil {
			catalog.Scheduling[target.JobID] = history
		}
	}
	return catalog, nil
}

// This reads catalog metadata only. It never reads a function body into the
// application, invokes customer code, or creates a scheduler witness.
func readRepairEnrollmentCatalog(ctx context.Context, db *pgx.Conn) (out repairEnrollmentCatalog, err error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	err = pgx.BeginTxFunc(ctx, db, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='750ms';SET LOCAL lock_timeout='100ms';SET LOCAL search_path=pg_catalog`); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT d.oid,p.oid,p.proowner,n.nspname,p.proname,
 format('%I.%I(%s)',n.nspname,p.proname,pg_get_function_identity_arguments(p.oid)),
 encode(sha256(convert_to((to_jsonb(p)||jsonb_build_object('namespace_oid',n.oid))::text,'UTF8')),'hex'),p.xmin::text
 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace JOIN pg_language l ON l.oid=p.prolang
 JOIN pg_database d ON d.datname=current_database()
 WHERE p.prokind='f' AND NOT p.prosecdef AND l.lanname IN ('sql','plpgsql')
 AND p.proowner=current_user::regrole::oid AND left(n.nspname,3)<>'pg_' AND n.nspname<>'information_schema'
 AND NOT EXISTS(SELECT FROM pg_depend x WHERE x.classid='pg_proc'::regclass AND x.objid=p.oid AND x.deptype='e')
 ORDER BY n.nspname,p.proname,p.oid LIMIT 101`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var target repairEnrollmentTarget
			target.Kind = "memory"
			if err := rows.Scan(&target.DatabaseOID, &target.ObjectOID, &target.OwnerOID, &target.Schema, &target.Name, &target.Signature, &target.Fingerprint, &target.Generation); err != nil {
				rows.Close()
				return err
			}
			out.Functions = append(out.Functions, target)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(out.Functions) > 100 {
			out.Truncated = true
			out.Functions = out.Functions[:100]
		}
		var cronReady bool
		var zone string
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pg_extension e JOIN pg_depend d ON d.refclassid='pg_extension'::regclass AND d.refobjid=e.oid AND d.deptype='e'
 JOIN pg_class c ON c.oid=d.objid AND d.classid='pg_class'::regclass JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE e.extname='pg_cron' AND n.nspname='cron' AND c.relname='job' AND c.relkind='r'),coalesce(current_setting('cron.timezone',true),'')`).Scan(&cronReady, &zone); err != nil {
			return err
		}
		if !cronReady {
			out.CronUnavailable = "Scheduling enrollment needs an installed pg_cron job catalog. Pooltune will not install it or create a job here."
			return nil
		}
		if zone != "GMT" && zone != "UTC" {
			out.CronUnavailable = "Scheduling enrollment currently supports UTC job schedules only."
			return nil
		}
		rows, err = tx.Query(ctx, `SELECT d.oid,current_user::regrole::oid,j.jobid,j.jobname,j.schedule,
 encode(sha256(convert_to(j.command,'UTF8')),'hex'),encode(sha256(convert_to(to_jsonb(j)::text,'UTF8')),'hex'),j.xmin::text
 FROM cron.job j JOIN pg_database d ON d.datname=current_database()
 WHERE j.username::text=current_user AND j.database::text=current_database() AND j.active AND j.jobname IS NOT NULL
 ORDER BY j.jobname,j.jobid LIMIT 101`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var target repairEnrollmentTarget
			target.Kind = "cron"
			if err := rows.Scan(&target.DatabaseOID, &target.OwnerOID, &target.JobID, &target.Name, &target.Schedule, &target.CommandDigest, &target.Fingerprint, &target.Generation); err != nil {
				rows.Close()
				return err
			}
			out.Jobs = append(out.Jobs, target)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(out.Jobs) > 100 {
			out.Truncated = true
			out.Jobs = out.Jobs[:100]
		}
		return nil
	})
	return out, err
}

func sameEnrollmentIdentity(a, b repairEnrollmentTarget) bool { return a == b }

func (c repairEnrollmentCatalog) contains(target repairEnrollmentTarget) bool {
	items := c.Functions
	if target.Kind == "cron" {
		items = c.Jobs
	}
	for _, item := range items {
		if sameEnrollmentIdentity(target, item) {
			return true
		}
	}
	return false
}
