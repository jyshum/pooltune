package hostedquery

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type ProjectFunctionMemoryTarget struct {
	Project, Schema, Function string
	DatabaseOID, FunctionOID  uint32
}

type ProjectFunctionMemorySnapshot struct {
	Signature, Fingerprint, Generation, WorkMem string
	WorkMemBytes                                int64
}

type ProjectFunctionMemoryEvidence struct {
	RunID    string
	At       time.Time
	Baseline ProjectFunctionMemorySnapshot
	Spill    MemoryEvidence
}

type ProjectFunctionMemoryIntent struct {
	Phase, RunID, Signature, Fingerprint, PreviousWorkMem, AppliedWorkMem string
	Target                                                                ProjectFunctionMemoryTarget
	MaxConcurrentCalls                                                    int
	CandidateBytes, AggregateBytes                                        int64
}

type ProjectFunctionMemoryGuards struct {
	CheckPermission func(context.Context, ProjectFunctionMemoryIntent) error
	CheckCapacity   func(context.Context, ProjectFunctionMemoryIntent) error
	// RecordIntent must commit the exact action in independent durable state.
	RecordIntent func(context.Context, ProjectFunctionMemoryIntent) error
}

type ProjectFunctionMemoryChange struct {
	Intent ProjectFunctionMemoryIntent
	Before ProjectFunctionMemorySnapshot
	After  ProjectFunctionMemorySnapshot
}

// ReadProjectFunctionMemory resolves an enrolled signature on its authorized
// native route. Function OIDs are local to the verified database and project.
func ReadProjectFunctionMemory(ctx context.Context, db *pgx.Conn, target ProjectFunctionMemoryTarget) (ProjectFunctionMemorySnapshot, error) {
	if db == nil {
		return ProjectFunctionMemorySnapshot{}, errors.New("missing function catalog connection")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return readProjectFunctionMemory(ctx, db, target)
}

func readProjectFunctionMemory(ctx context.Context, db trialReader, t ProjectFunctionMemoryTarget) (ProjectFunctionMemorySnapshot, error) {
	var s ProjectFunctionMemorySnapshot
	if t.Project == "" || t.DatabaseOID == 0 || t.FunctionOID == 0 || t.Schema == "" || t.Function == "" {
		return s, errors.New("missing enrolled function identity")
	}
	var args []string
	var definition []byte
	err := db.QueryRow(ctx, `
		SELECT ARRAY(SELECT pg_catalog.format('%I.%I',tn.nspname,typ.typname)
		       FROM pg_catalog.unnest(p.proargtypes) WITH ORDINALITY a(oid,pos)
		       JOIN pg_catalog.pg_type typ ON typ.oid=a.oid JOIN pg_catalog.pg_namespace tn ON tn.oid=typ.typnamespace ORDER BY a.pos),
		       (pg_catalog.to_jsonb(p)-'proconfig') || pg_catalog.jsonb_build_array(n.oid, ARRAY(SELECT v FROM pg_catalog.unnest(p.proconfig) v WHERE v NOT LIKE 'work_mem=%' ORDER BY v)),
		       p.xmin::text, coalesce(mem.setting,''), coalesce(pg_catalog.pg_size_bytes(mem.setting),0)
		FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace JOIN pg_catalog.pg_language lang ON lang.oid=p.prolang
		JOIN pg_catalog.pg_database d ON d.datname=pg_catalog.current_database()
		LEFT JOIN LATERAL (SELECT pg_catalog.substr(v,10) AS setting FROM pg_catalog.unnest(p.proconfig) v WHERE v LIKE 'work_mem=%') mem ON TRUE
		WHERE p.oid=$1 AND d.oid=$2 AND n.nspname::text=$3 AND p.proname::text=$4 AND p.prokind='f'
		  AND NOT p.prosecdef AND p.proparallel='u' AND lang.lanname IN ('sql','plpgsql')
		  AND 'max_parallel_workers_per_gather=0'=ANY(p.proconfig)
		  AND pg_catalog.left(n.nspname::text,3)<>'pg_' AND n.nspname<>'information_schema' AND NOT pg_catalog.pg_is_in_recovery()
		  AND current_user=session_user AND p.proowner=(SELECT oid FROM pg_catalog.pg_roles WHERE rolname=current_user)
	`, t.FunctionOID, t.DatabaseOID, t.Schema, t.Function).Scan(&args, &definition, &s.Generation, &s.WorkMem, &s.WorkMemBytes)
	if err != nil {
		return s, fmt.Errorf("read exact enrolled function as its owner: %w", err)
	}
	s.Signature = qualified(t.Schema, t.Function) + "(" + strings.Join(args, ", ") + ")"
	s.Fingerprint = fmt.Sprintf("%x", sha256.Sum256(definition))
	return s, nil
}

// ApplyProjectFunctionMemory requires a matched outer call and internal sort
// observation plus a caller-enforced concurrency limit. The catalog enforces
// serial execution for the enrolled function's internal statements.
func ApplyProjectFunctionMemory(ctx context.Context, cfg *pgx.ConnConfig, target ProjectFunctionMemoryTarget, evidence ProjectFunctionMemoryEvidence, guards ProjectFunctionMemoryGuards) (ProjectFunctionMemoryChange, error) {
	if err := validateProjectFunctionMemoryEvidence(target, evidence, time.Now()); err != nil {
		return ProjectFunctionMemoryChange{}, err
	}
	intent := ProjectFunctionMemoryIntent{
		Phase: "apply", Target: target, RunID: evidence.RunID, Signature: evidence.Baseline.Signature, Fingerprint: evidence.Baseline.Fingerprint,
		PreviousWorkMem: evidence.Baseline.WorkMem, AppliedWorkMem: fmt.Sprintf("%dkB", evidence.Spill.CandidateBytes/1024),
		MaxConcurrentCalls: evidence.Spill.MaxConcurrentCalls, CandidateBytes: evidence.Spill.CandidateBytes, AggregateBytes: evidence.Spill.AggregateBytes,
	}
	after, err := changeProjectFunctionMemory(ctx, cfg, intent, evidence.Baseline, intent.AppliedWorkMem, evidence.Spill.CandidateBytes, guards, func() error {
		return validateProjectFunctionMemoryEvidence(target, evidence, time.Now())
	})
	return ProjectFunctionMemoryChange{Intent: intent, Before: evidence.Baseline, After: after}, err
}

func validateProjectFunctionMemoryEvidence(target ProjectFunctionMemoryTarget, e ProjectFunctionMemoryEvidence, now time.Time) error {
	m := e.Spill
	if e.RunID == "" || len(e.RunID) > 64 || e.At.IsZero() || e.At.After(now) || now.Sub(e.At) > 30*time.Second ||
		m.FunctionOID != target.FunctionOID || !m.IdentityVerified || !nonnegativeFinite(m.TempBytes) || !nonnegativeFinite(m.InternalTempBytes) ||
		m.TempBytes < memoryMinSpill || m.InternalTempBytes < memoryMinSpill || m.SortNodes != 1 || m.MaxConcurrentCalls < 1 || m.MaxConcurrentCalls > 3 ||
		m.CurrentWorkMem != e.Baseline.WorkMem || e.Baseline.WorkMemBytes <= 0 || m.CandidateBytes <= e.Baseline.WorkMemBytes ||
		m.CandidateBytes > 32<<20 || m.CandidateBytes%1024 != 0 || m.AggregateBytes != int64(m.MaxConcurrentCalls)*m.CandidateBytes {
		return errors.New("function-memory evidence or capacity bounds do not qualify")
	}
	return nil
}

// RestoreProjectFunctionMemory requires the post-commit catalog generation.
// A durable intent alone cannot establish that the worker owns the active value.
func RestoreProjectFunctionMemory(ctx context.Context, cfg *pgx.ConnConfig, change ProjectFunctionMemoryChange, guards ProjectFunctionMemoryGuards) error {
	i, before, after := change.Intent, change.Before, change.After
	if i.Phase != "apply" || before.Generation == "" || after.Generation == "" || before.Generation == after.Generation ||
		i.Fingerprint != before.Fingerprint || i.Fingerprint != after.Fingerprint || i.Signature != before.Signature || i.Signature != after.Signature ||
		i.AppliedWorkMem != after.WorkMem || i.PreviousWorkMem != before.WorkMem || before.WorkMemBytes <= 0 || after.WorkMemBytes <= before.WorkMemBytes || after.WorkMemBytes > 32<<20 ||
		i.CandidateBytes != after.WorkMemBytes || i.MaxConcurrentCalls < 1 || i.MaxConcurrentCalls > 3 || i.AggregateBytes != int64(i.MaxConcurrentCalls)*i.CandidateBytes {
		return errors.New("missing verified function-memory change for restoration")
	}
	i.Phase = "restore"
	_, err := changeProjectFunctionMemory(ctx, cfg, i, after, before.WorkMem, before.WorkMemBytes, guards, nil)
	return err
}

func changeProjectFunctionMemory(ctx context.Context, cfg *pgx.ConnConfig, intent ProjectFunctionMemoryIntent, expected ProjectFunctionMemorySnapshot, value string, expectedBytes int64, guards ProjectFunctionMemoryGuards, recheck func() error) (out ProjectFunctionMemorySnapshot, err error) {
	if cfg == nil || guards.CheckPermission == nil || guards.CheckCapacity == nil || guards.RecordIntent == nil {
		return out, errors.New("function memory requires a native route, permission, capacity and durable intent")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	db, err := pgx.ConnectConfig(ctx, cfg.Copy())
	if err != nil {
		return out, err
	}
	defer closeVacuumConnection(db)
	var pid int32
	if err := db.QueryRow(ctx, "SELECT pg_catalog.pg_backend_pid()").Scan(&pid); err != nil || pid != int32(db.PgConn().PID()) {
		return out, errors.New("function memory requires a native PostgreSQL session")
	}
	err = pgx.BeginTxFunc(ctx, db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL statement_timeout='750ms'; SET LOCAL lock_timeout='250ms'; SET LOCAL idle_in_transaction_session_timeout='5s'"); err != nil {
			return err
		}
		var locked bool
		if err := tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1,$2)", int32(1514145798), int32(intent.Target.FunctionOID)).Scan(&locked); err != nil || !locked {
			return errors.New("another function-memory action holds this target")
		}
		current, err := readProjectFunctionMemory(ctx, tx, intent.Target)
		if err != nil || current != expected {
			return errors.New("enrolled function or setting changed before the action")
		}
		for _, guard := range []func(context.Context, ProjectFunctionMemoryIntent) error{guards.CheckPermission, guards.CheckCapacity, guards.RecordIntent, guards.CheckPermission} {
			if err := guard(ctx, intent); err != nil {
				return err
			}
		}
		if recheck != nil {
			if err := recheck(); err != nil {
				return err
			}
		}
		var literal string
		if err := tx.QueryRow(ctx, "SELECT quote_literal($1::text)", value).Scan(&literal); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "ALTER FUNCTION "+current.Signature+" SET work_mem="+literal); err != nil {
			return err
		}
		updated, err := readProjectFunctionMemory(ctx, tx, intent.Target)
		if err != nil || updated.Signature != current.Signature || updated.Fingerprint != current.Fingerprint || updated.Generation == current.Generation || updated.WorkMem != value || updated.WorkMemBytes != expectedBytes {
			return errors.New("function-memory alteration failed exact catalog readback")
		}
		out = updated
		return nil
	})
	if err != nil {
		return ProjectFunctionMemorySnapshot{}, err
	}
	return out, nil
}
