package hostedquery

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
)

type ManagedMemoryIntent struct {
	RunID             string                `json:"run_id"`
	Function          ManagedMemoryFunction `json:"function"`
	AppliedDefinition string                `json:"applied_definition"`
	PolicySchema      string                `json:"policy_schema"`
}

type ManagedMemoryChange struct {
	Intent            ManagedMemoryIntent `json:"intent"`
	AppliedGeneration string              `json:"applied_generation"`
	AppliedDigest     string              `json:"applied_digest"`
}

type ManagedMemoryGuards struct {
	CheckPermission func(context.Context) error
	RecordIntent    func(context.Context, ManagedMemoryIntent) error
	// Persist the observed committed generation. A lost acknowledgement leaves
	// the original intent unresolved; expiry independently disables extra memory.
	RecordApplied func(context.Context, ManagedMemoryChange) error
}

func managedMemorySchema(project string) string {
	return fmt.Sprintf("pt_memory_%x", sha256.Sum256([]byte(project)))[:26]
}

func ensureManagedMemoryPolicy(ctx context.Context, tx pgx.Tx, project, schema string) error {
	var existing *string
	var owner bool
	if err := tx.QueryRow(ctx, `SELECT obj_description(n.oid,'pg_namespace'),n.nspowner=current_user::regrole::oid FROM pg_namespace n WHERE n.nspname=$1`, schema).Scan(&existing, &owner); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	} else if err == nil && (!owner || existing == nil || *existing != "Pooltune memory allowance v1 "+project) {
		return errors.New("memory policy schema is occupied")
	}
	q := pgx.Identifier{schema}.Sanitize()
	if _, err := tx.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+q); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "COMMENT ON SCHEMA "+q+" IS '"+strings.ReplaceAll("Pooltune memory allowance v1 "+project, "'", "''")+"'"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+qualified(schema, "allowances")+`(function_oid oid PRIMARY KEY, generation text NOT NULL,digest text NOT NULL,candidate_kb integer NOT NULL CHECK(candidate_kb BETWEEN 1 AND 32768),slots integer NOT NULL CHECK(slots BETWEEN 0 AND 3),valid_until timestamptz NOT NULL)`); err != nil {
		return err
	}
	body := `SELECT a.candidate_kb,a.slots FROM ` + qualified(schema, "allowances") + ` a JOIN pg_catalog.pg_proc p ON p.oid=a.function_oid WHERE a.function_oid=target AND p.xmin::text=a.generation AND pg_catalog.md5(pg_catalog.to_jsonb(p)::text)=a.digest AND a.valid_until>pg_catalog.clock_timestamp() AND a.valid_until<=pg_catalog.clock_timestamp()+interval '75 seconds'`
	sql := "CREATE OR REPLACE FUNCTION " + qualified(schema, "allowance") + `(target oid) RETURNS TABLE(candidate_kb integer,slots integer) LANGUAGE sql VOLATILE PARALLEL UNSAFE SECURITY DEFINER SET search_path=pg_catalog AS '` + strings.ReplaceAll(body, "'", "''") + "'"
	if _, err := tx.Exec(ctx, sql); err != nil {
		return err
	}
	// The helper only returns an expiry-bound numeric allowance for a matching
	// catalog object. No application rows, credentials, or writable API is exposed.
	_, err := tx.Exec(ctx, "REVOKE ALL ON "+qualified(schema, "allowances")+" FROM PUBLIC; GRANT USAGE ON SCHEMA "+q+" TO PUBLIC; GRANT EXECUTE ON FUNCTION "+qualified(schema, "allowance")+"(oid) TO PUBLIC")
	return err
}

func ApplyManagedMemory(ctx context.Context, db *pgx.Conn, fn ManagedMemoryFunction, runID string, headroom supabaseproject.MemoryHeadroom, guards ManagedMemoryGuards) (change ManagedMemoryChange, err error) {
	if db == nil || runID == "" || len(runID) > 64 || guards.CheckPermission == nil || guards.RecordIntent == nil || guards.RecordApplied == nil || headroom.MemoryAllowanceSlots(time.Now()) < 1 {
		return change, errors.New("managed memory requires current headroom, permission and durable action state")
	}
	if !strings.HasPrefix(fn.Target.Project, "supabase/"+headroom.ProjectRef+"/") {
		return change, errors.New("memory headroom belongs to another project")
	}
	checked, checkErr := ReadManagedMemoryFunction(ctx, db, ManagedMemoryEnrollment{Target: fn.Target, Fingerprint: fn.CatalogFingerprint, Generation: fn.Snapshot.Generation})
	if checkErr != nil || checked != fn {
		return change, errors.Join(checkErr, errors.New("memory action requires freshly validated supported function"))
	}
	schema := managedMemorySchema(fn.Target.Project)
	body, err := managedMemoryBody(fn.Query, qualified(schema, "allowance")+fmt.Sprintf("(%d::oid)", fn.Target.FunctionOID))
	if err != nil {
		return change, err
	}
	definition, err := managedMemoryDefinition(fn.OriginalDefinition, body)
	if err != nil {
		return change, err
	}
	change.Intent = ManagedMemoryIntent{RunID: runID, Function: fn, AppliedDefinition: definition, PolicySchema: schema}
	err = pgx.BeginTxFunc(ctx, db, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout='250ms'; SET LOCAL statement_timeout='2s'; SET LOCAL search_path=pg_catalog`); err != nil {
			return err
		}
		var locked bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1,$2)`, int32(1514145798), int32(fn.Target.FunctionOID)).Scan(&locked); err != nil || !locked {
			return errors.Join(err, errors.New("function memory target is active"))
		}
		lockedGeneration, err := lockManagedMemoryFunction(ctx, tx, fn.Target, fn.Snapshot.Generation)
		if err != nil {
			return err
		}
		current, err := readProjectFunctionMemory(ctx, tx, fn.Target)
		expected := fn.Snapshot
		expected.Generation = lockedGeneration
		if err != nil || current != expected {
			return errors.Join(err, errors.New("selected function changed before memory allowance"))
		}
		if err := guards.CheckPermission(ctx); err != nil {
			return err
		}
		if headroom.MemoryAllowanceSlots(time.Now()) < 1 {
			return supabaseproject.ErrMemoryHeadroomUnavailable
		}
		if err := guards.RecordIntent(ctx, change.Intent); err != nil {
			return err
		}
		if err := ensureManagedMemoryPolicy(ctx, tx, fn.Target.Project, schema); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, definition); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT xmin::text,md5(to_jsonb(p)::text) FROM pg_proc p WHERE oid=$1`, fn.Target.FunctionOID).Scan(&change.AppliedGeneration, &change.AppliedDigest); err != nil {
			return err
		}
		if change.AppliedGeneration == fn.Snapshot.Generation {
			return errors.New("function rewrite did not create a new catalog generation")
		}
		if err := refreshManagedMemoryPolicy(ctx, tx, change, headroom); err != nil {
			return err
		}
		if err := guards.CheckPermission(ctx); err != nil {
			return err
		}
		return nil
	})
	if err == nil {
		err = guards.RecordApplied(ctx, change)
	}
	return change, err
}

func refreshManagedMemoryPolicy(ctx context.Context, db trialReader, change ManagedMemoryChange, h supabaseproject.MemoryHeadroom) error {
	slots := h.MemoryAllowanceSlots(time.Now())
	if slots < 1 {
		return supabaseproject.ErrMemoryHeadroomUnavailable
	}
	// A successful scrape grants at most sixty seconds, never indefinitely. The
	// wrapper falls back even if the worker dies before explicit revocation.
	var oid uint32
	return db.QueryRow(ctx, "INSERT INTO "+qualified(change.Intent.PolicySchema, "allowances")+`(function_oid,generation,digest,candidate_kb,slots,valid_until) SELECT p.oid,p.xmin::text,md5(to_jsonb(p)::text),32768,$4,clock_timestamp()+interval '60 seconds' FROM pg_proc p WHERE p.oid=$1 AND p.xmin::text=$2 AND md5(to_jsonb(p)::text)=$3 ON CONFLICT(function_oid) DO UPDATE SET generation=excluded.generation,digest=excluded.digest,candidate_kb=excluded.candidate_kb,slots=excluded.slots,valid_until=excluded.valid_until RETURNING function_oid`, change.Intent.Function.Target.FunctionOID, change.AppliedGeneration, change.AppliedDigest, slots).Scan(&oid)
}

// RefreshManagedMemoryAllowance must be called only while the owner's memory
// capability and installation epoch remain active. No refresh means fallback.
func RefreshManagedMemoryAllowance(ctx context.Context, db *pgx.Conn, change ManagedMemoryChange, h supabaseproject.MemoryHeadroom) error {
	return refreshManagedMemoryPolicy(ctx, db, change, h)
}

func RestoreManagedMemory(ctx context.Context, db *pgx.Conn, change ManagedMemoryChange, guard func(context.Context) error) error {
	if guard == nil || change.AppliedDigest == "" || change.AppliedGeneration == "" {
		return errors.New("memory restore needs an exact committed generation")
	}
	return pgx.BeginTxFunc(ctx, db, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout='250ms'; SET LOCAL statement_timeout='2s'; SET LOCAL search_path=pg_catalog`); err != nil {
			return err
		}
		var locked bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1,$2)`, int32(1514145798), int32(change.Intent.Function.Target.FunctionOID)).Scan(&locked); err != nil || !locked {
			return errors.Join(err, errors.New("function memory target is active"))
		}
		lockedGeneration, err := lockManagedMemoryFunction(ctx, tx, change.Intent.Function.Target, change.AppliedGeneration)
		if err != nil {
			return err
		}
		var owned bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pg_proc p WHERE p.oid=$1 AND p.xmin::text=$2 AND md5(to_jsonb(p)::text)=$3 AND p.proowner=current_user::regrole::oid AND current_user=session_user)`, change.Intent.Function.Target.FunctionOID, lockedGeneration, change.AppliedDigest).Scan(&owned); err != nil {
			return err
		}
		if !owned {
			return errors.New("the installed memory function changed independently")
		}
		if err := guard(ctx); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM "+qualified(change.Intent.PolicySchema, "allowances")+" WHERE function_oid=$1", change.Intent.Function.Target.FunctionOID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, change.Intent.Function.OriginalDefinition)
		return err
	})
}

// The native rename takes the catalog tuple lock without requiring catalog
// UPDATE privileges. Both names are transaction-local; callers keep seeing the
// original committed function. An identical-content owner save during lock
// acquisition is allowed, but every changed catalog value or OID is refused.
var errManagedMemoryOwnerChanged = errors.New("the selected function changed independently")

func lockManagedMemoryFunction(ctx context.Context, tx pgx.Tx, target ProjectFunctionMemoryTarget, expectedGeneration string) (string, error) {
	const catalog = `SELECT to_jsonb(p)::text,p.xmin::text FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace WHERE p.oid=$1 AND n.nspname=$2 AND p.proname=$3 AND p.pronargs=0 AND p.proowner=current_user::regrole::oid AND current_user=session_user AND (SELECT oid FROM pg_database WHERE datname=current_database())=$4`
	var before, generation string
	if err := tx.QueryRow(ctx, catalog, target.FunctionOID, target.Schema, target.Function, target.DatabaseOID).Scan(&before, &generation); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", errors.Join(errManagedMemoryOwnerChanged, err)
		}
		return "", err
	}
	if generation != expectedGeneration {
		return "", errManagedMemoryOwnerChanged
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	temporary := fmt.Sprintf("pt_memory_fence_%x", nonce)
	if _, err := tx.Exec(ctx, "ALTER FUNCTION "+qualified(target.Schema, target.Function)+"() RENAME TO "+pgx.Identifier{temporary}.Sanitize()); err != nil {
		return "", err
	}
	// Check while renamed, before restoring the public name. If name lookup
	// acquired a replacement object instead, the exact OID lookup fails and the
	// transaction rolls back both provisional renames.
	var after string
	if err := tx.QueryRow(ctx, `SELECT (to_jsonb(p)||jsonb_build_object('proname',$3::text))::text,p.xmin::text FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE p.oid=$1 AND n.nspname=$2 AND p.proname=$4 AND p.pronargs=0 AND p.proowner=current_user::regrole::oid`, target.FunctionOID, target.Schema, target.Function, temporary).Scan(&after, &generation); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", errors.Join(errManagedMemoryOwnerChanged, err)
		}
		return "", err
	}
	if after != before {
		return "", errManagedMemoryOwnerChanged
	}
	if _, err := tx.Exec(ctx, "ALTER FUNCTION "+qualified(target.Schema, temporary)+"() RENAME TO "+pgx.Identifier{target.Function}.Sanitize()); err != nil {
		return "", err
	}
	return generation, nil
}

// This fence is rolled back, so final verification does not change the installed
// generation. Keep owner edits after measurement out of verified outcomes.
func verifyManagedMemoryInstalled(ctx context.Context, db *pgx.Conn, change ManagedMemoryChange) (err error) {
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err = errors.Join(err, tx.Rollback(cleanup))
	}()
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='250ms'; SET LOCAL statement_timeout='750ms'; SET LOCAL search_path=pg_catalog`); err != nil {
		return err
	}
	if _, err = lockManagedMemoryFunction(ctx, tx, change.Intent.Function.Target, change.AppliedGeneration); err != nil {
		return err
	}
	var exact bool
	if err = tx.QueryRow(ctx, `SELECT md5(to_jsonb(p)::text)=$2 FROM pg_proc p WHERE oid=$1`, change.Intent.Function.Target.FunctionOID, change.AppliedDigest).Scan(&exact); err != nil {
		return err
	}
	if !exact {
		return errManagedMemoryOwnerChanged
	}
	return nil
}
