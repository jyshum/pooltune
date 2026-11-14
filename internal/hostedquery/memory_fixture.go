package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Definition and catalog identity are retained independently of the action report.
// Only WorkMem is allowed to differ from the enrolled definition.
type MemoryIdentity struct {
	OID           uint32   `json:"oid"`
	Owner         uint32   `json:"owner"`
	Definition    string   `json:"definition"`
	Signature     string   `json:"signature"`
	WorkMem       string   `json:"work_mem"`
	OtherSettings []string `json:"other_settings"`
	Private       bool     `json:"private"`
}

func memoryQuery(schema string) string {
	return "SELECT " + qualified(schema, memoryFunction) + "() AS sum"
}
func memoryInternalQuery(schema string) string {
	return "SELECT sum(lookup) FROM (SELECT lookup FROM ONLY " + qualified(schema, "candidate") + " ORDER BY padding,lookup OFFSET 0) AS sorted"
}
func readMemoryFunction(ctx context.Context, db trialReader, schema string) (f MemoryIdentity, err error) {
	err = db.QueryRow(ctx, `SELECT p.oid,p.proowner,pg_get_functiondef(p.oid),pg_get_function_identity_arguments(p.oid),
 COALESCE((SELECT substr(v,10) FROM unnest(p.proconfig) v WHERE v LIKE 'work_mem=%'),''),
 ARRAY(SELECT v FROM unnest(p.proconfig) v WHERE v NOT LIKE 'work_mem=%' ORDER BY v),
 NOT p.prosecdef AND NOT EXISTS(SELECT FROM aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) a WHERE a.grantee<>p.proowner)
 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=$1 AND p.proname=$2 AND p.pronargs=0`, schema, memoryFunction).Scan(&f.OID, &f.Owner, &f.Definition, &f.Signature, &f.WorkMem, &f.OtherSettings, &f.Private)
	return
}
func expectedMemoryFunction(ctx context.Context, state *pgx.Conn, id string) (f MemoryIdentity, err error) {
	var raw []byte
	err = state.QueryRow(ctx, "SELECT memory_function FROM "+journalTable+" WHERE id=$1", id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &f)
	}
	if err == nil && (f.OID == 0 || f.Owner == 0 || !f.Private || f.WorkMem != memoryOriginal || f.Signature != "" || f.Definition == "") {
		err = errors.New("missing enrolled function identity")
	}
	return
}
func sameMemoryFunction(a, b MemoryIdentity) bool {
	normalize := func(f MemoryIdentity) MemoryIdentity {
		f.Definition = strings.ReplaceAll(f.Definition, "SET work_mem TO '"+f.WorkMem+"'", "SET work_mem TO '<enrolled>'")
		f.WorkMem = ""
		return f
	}
	return a.Private && b.Private && reflect.DeepEqual(normalize(a), normalize(b))
}
func validateMemoryFixture(ctx context.Context, db trialReader, id string, objects fixtureObjects, expected MemoryIdentity, workMem string) error {
	if err := validateSampleFixture(ctx, db, id, objects); err != nil {
		return err
	}
	current, err := readMemoryFunction(ctx, db, trialSchema(id))
	if err != nil {
		return err
	}
	if !sameMemoryFunction(current, expected) || current.WorkMem != workMem {
		return errors.New("memory function identity, body, owner or settings changed")
	}
	return nil
}
func seedMemoryFixture(ctx context.Context, target, state *pgx.Conn, id string) error {
	schema := trialSchema(id)
	return pgx.BeginFunc(ctx, target, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout='5s'; SET LOCAL lock_timeout='250ms'"); err != nil {
			return err
		}
		table := qualified(schema, "candidate")
		if _, err := tx.Exec(ctx, "TRUNCATE "+table); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO "+table+" SELECT n,n,repeat(md5(n::text),4) FROM generate_series(1,100000) n"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "ANALYZE "+table); err != nil {
			return err
		}
		var key uint32
		if err := tx.QueryRow(ctx, "SELECT to_regnamespace($1)::oid", schema).Scan(&key); err != nil {
			return err
		}
		body := fmt.Sprintf(`DECLARE slot integer; admitted boolean := false; answer bigint;
BEGIN
 FOR slot IN 1..3 LOOP
  IF pg_catalog.pg_try_advisory_xact_lock(%d,slot) THEN admitted := true; EXIT; END IF;
 END LOOP;
 IF NOT admitted THEN RAISE EXCEPTION 'memory concurrency limit' USING ERRCODE='53300'; END IF;
 %s INTO answer;
 RETURN answer;
END`, int32(key), memoryInternalQuery(schema))
		sql := "CREATE FUNCTION " + qualified(schema, memoryFunction) + "() RETURNS bigint LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY INVOKER SET search_path=pg_catalog SET work_mem='4MB' SET max_parallel_workers_per_gather=0 SET jit=off AS $memory$" + body + "$memory$"
		if _, err := tx.Exec(ctx, sql); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "REVOKE ALL ON FUNCTION "+qualified(schema, memoryFunction)+"() FROM PUBLIC"); err != nil {
			return err
		}
		f, err := readMemoryFunction(ctx, tx, schema)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(f)
		if err != nil {
			return err
		}
		_, err = state.Exec(ctx, "UPDATE "+journalTable+" SET memory_function=$2::jsonb WHERE id=$1", id, string(raw))
		return err
	})
}
func admitMemory(ctx context.Context, tx pgx.Tx, schema uint32) error {
	for slot := 1; slot <= 3; slot++ {
		var ok bool
		if err := tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1::integer,$2::integer)", int32(schema), slot).Scan(&ok); err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return errors.New("memory concurrency limit")
}
func changeMemorySetting(ctx context.Context, target *pgx.Conn, id string, objects fixtureObjects, fn MemoryIdentity, from, to string) (bool, error) {
	return alterMemorySetting(ctx, target, id, objects, fn, from, to, nil)
}
func alterMemorySetting(ctx context.Context, target *pgx.Conn, id string, objects fixtureObjects, fn MemoryIdentity, from, to string, persistIntent func() error) (applied bool, err error) {
	if (from != memoryOriginal && from != memoryCandidate) || (to != memoryOriginal && to != memoryCandidate) || from == to {
		return false, errors.New("unsupported function memory setting")
	}
	err = pgx.BeginFunc(ctx, target, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL statement_timeout='750ms'; SET LOCAL lock_timeout='250ms'"); err != nil {
			return err
		}
		if err := lockSampleTables(ctx, tx, trialSchema(id)); err != nil {
			return err
		}
		if err := validateMemoryFixture(ctx, tx, id, objects, fn, from); err != nil {
			return err
		}
		// Durable intent follows the locked recheck. An independent move to the
		// candidate value before this check must never become our pending action.
		if persistIntent != nil {
			if err := persistIntent(); err != nil {
				return err
			}
		}
		// A fixed literal chosen by Go. Preserve every other function setting.
		if _, err := tx.Exec(ctx, "ALTER FUNCTION "+qualified(trialSchema(id), memoryFunction)+"() SET work_mem='"+to+"'"); err != nil {
			return err
		}
		applied = true
		return nil
	})
	return
}
func restoreMemory(ctx context.Context, target *pgx.Conn, id string, objects fixtureObjects, report *MemoryReport) (bool, error) {
	current, err := readMemoryFunction(ctx, target, trialSchema(id))
	if err != nil {
		return false, err
	}
	if !sameMemoryFunction(current, report.Function) || current.WorkMem != report.AppliedWorkMem {
		return false, nil
	}
	if report.OriginalWorkMem != memoryOriginal || report.AppliedWorkMem != memoryCandidate {
		return false, errors.New("invalid durable memory intent")
	}
	return changeMemorySetting(ctx, target, id, objects, report.Function, report.AppliedWorkMem, report.OriginalWorkMem)
}
func recoverMemoryPreparations(ctx context.Context, target, state *pgx.Conn, route string) (err error) {
	if err := prepareJournal(ctx, state); err != nil {
		return err
	}
	if err := lockTrial(ctx, target); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, releaseTrialLock(target)) }()
	return reconcileTrialStatuses(ctx, target, state, journalTarget(target, route), []string{"memory_preparing"})
}
func CreateMemoryFixture(ctx context.Context, target, state *pgx.Conn, route string) (ConsumerFixture, error) {
	if err := recoverMemoryPreparations(ctx, target, state, route); err != nil {
		return ConsumerFixture{}, err
	}
	f, err := createConsumerFixture(ctx, target, state, route, "memory")
	if err == nil || f.ID == "" {
		return f, err
	}
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tc, ce := pgx.ConnectConfig(cleanup, target.Config())
	if ce != nil {
		return f, errors.Join(err, ce)
	}
	defer closeVacuumConnection(tc)
	sc, ce := pgx.ConnectConfig(cleanup, state.Config())
	if ce != nil {
		return f, errors.Join(err, ce)
	}
	defer closeVacuumConnection(sc)
	return f, errors.Join(err, RemoveConsumerFixture(cleanup, tc, sc, route, f.ID))
}

// Drop functions before tables and refuse all foreign functions. RESTRICT keeps
// unexpected dependencies intact and rolls back the complete cleanup transaction.
func cleanMemoryFunction(ctx context.Context, tx pgx.Tx, state *pgx.Conn, id, status string) error {
	var count int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM pg_proc WHERE pronamespace=to_regnamespace($1)", trialSchema(id)).Scan(&count); err != nil {
		return err
	}
	if count == 0 && status == "memory_preparing" {
		return nil
	}
	if count != 1 {
		return errors.New("unexpected or missing memory fixture functions")
	}
	expected, err := expectedMemoryFunction(ctx, state, id)
	if err != nil {
		return err
	}
	current, err := readMemoryFunction(ctx, tx, trialSchema(id))
	if err != nil {
		return err
	}
	if !sameMemoryFunction(current, expected) || (current.WorkMem != memoryOriginal && current.WorkMem != memoryCandidate) {
		return errors.New("memory cleanup preserves changed function")
	}
	_, err = tx.Exec(ctx, "DROP FUNCTION "+qualified(trialSchema(id), memoryFunction)+"() RESTRICT")
	return err
}
