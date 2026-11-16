package hostedquery

import (
	"context"
	"errors"
	"fmt"
	"pooltune/internal/supabaseproject"
	"regexp"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

var managedIndexRunID = regexp.MustCompile(`^managed-index-([1-9][0-9]*)-[1-9][0-9]*$`)

func inspectManagedIndexIntent(ctx context.Context, db *pgx.Conn, claim investigationClaim, intent ProjectIndexActionIntent) (result ownedIndexRecovery, err error) {
	changed := ownedIndexRecovery{Status: "target_changed", Reason: "The recorded index target or owner has changed."}
	parts := managedIndexRunID.FindStringSubmatch(intent.RunID)
	if len(parts) != 2 || parts[1] != fmt.Sprint(claim.ID) || intent.Target.DatabaseOID != claim.Database || intent.Target.TableOID == 0 || len(intent.Keys) < 1 || len(intent.Keys) > 2 || intent.RelationBytes <= 0 || intent.RelationBytes > projectIndexMaxRelationBytes {
		return changed, nil
	}
	columns := make([]int16, len(intent.Keys))
	name := fmt.Sprintf("pooltune_ix_%d", intent.Target.TableOID)
	for i, key := range intent.Keys {
		if key.Number <= 0 || key.Name == "" {
			return changed, nil
		}
		columns[i] = key.Number
		name += fmt.Sprintf("_%d", key.Number)
	}
	optional := columns
	if len(columns) == 1 {
		optional = nil
	}
	if !ValidIndexColumns(columns[0], optional) || name != intent.IndexName {
		return changed, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var pid uint32
	var owned bool
	if err := db.QueryRow(ctx, supabaseproject.RepairQuery(ctx, db, `SELECT pg_backend_pid(),EXISTS(SELECT FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.oid=$1 AND c.relname=$2 AND n.nspname=$3 AND c.relowner=current_user::regrole::oid AND current_user=session_user)`), intent.Target.TableOID, intent.Target.Relation, intent.Target.Schema).Scan(&pid, &owned); err != nil {
		return result, err
	}
	if pid != db.PgConn().PID() {
		return result, errors.New("managed index recovery requires a native session")
	}
	if !owned {
		return changed, nil
	}
	var locked bool
	if err := db.QueryRow(ctx, `SELECT pg_try_advisory_lock($1,$2)`, projectIndexLockNamespace, int32(intent.Target.TableOID)).Scan(&locked); err != nil {
		return result, err
	}
	if !locked {
		return result, errOwnedIndexActionActive
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		var released bool
		unlockErr := db.QueryRow(cleanup, `SELECT pg_advisory_unlock($1,$2)`, projectIndexLockNamespace, int32(intent.Target.TableOID)).Scan(&released)
		if unlockErr != nil || !released {
			closeVacuumConnection(db)
			err = errors.Join(err, errors.New("managed recovery lock release failed"), unlockErr)
		}
	}()
	catalog, err := ResolveProjectIndex(ctx, db, intent.Target, columns[0], optional)
	if errors.Is(err, ErrProjectIndexTargetChanged) {
		return changed, nil
	}
	if err != nil {
		return result, err
	}
	if !slices.Equal(catalog.Keys, intent.Keys) {
		return changed, nil
	}
	var onTarget, live bool
	err = db.QueryRow(ctx, `SELECT COALESCE(i.indrelid=$3,false),COALESCE(i.indisvalid AND i.indisready AND i.indislive,false) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_index i ON i.indexrelid=c.oid WHERE n.nspname=$1 AND c.relname=$2`, intent.Target.Schema, intent.IndexName, intent.Target.TableOID).Scan(&onTarget, &live)
	switch {
	case errors.Is(err, pgx.ErrNoRows) && catalog.Covering:
		return ownedIndexRecovery{Status: "equivalent_index_present", Reason: "An equivalent index is present. The interrupted repair has no verified performance result."}, nil
	case errors.Is(err, pgx.ErrNoRows):
		return ownedIndexRecovery{Status: "index_absent", Reason: "The recorded index is absent after interruption."}, nil
	case err != nil:
		return result, err
	case !onTarget:
		return ownedIndexRecovery{Status: "index_name_occupied", Reason: "Another database object occupies the recorded index name."}, nil
	case !live:
		return ownedIndexRecovery{Status: "index_invalid", Reason: "The interrupted action left an invalid index that needs review."}, nil
	}
	var oid uint32
	var bytes int64
	var exact bool
	if err := db.QueryRow(ctx, projectIndexReadbackSQL, intent.Target.TableOID, intent.Target.Schema, intent.IndexName, columns).Scan(&oid, &bytes, &exact); err != nil {
		return result, err
	}
	if (intent.RollbackRequested && (intent.AppliedIndexOID == 0 || intent.AppliedIndexOID != oid)) || !exact || bytes <= 0 || bytes > projectIndexMaxIndexBytes {
		return ownedIndexRecovery{Status: "index_name_occupied", Reason: "The index does not match the recorded definition."}, nil
	}
	if intent.RollbackRequested {
		return ownedIndexRecovery{Status: "exact_index_present", IndexOID: oid, IndexBytes: bytes, Reason: "The index remains after an interrupted regression rollback and needs review. The rollback was not repeated."}, nil
	}
	return ownedIndexRecovery{Status: "exact_index_present", IndexOID: oid, IndexBytes: bytes, Reason: "The recorded index is valid. Performance verification was interrupted."}, nil
}
