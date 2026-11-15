package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var ownedIndexRunID = regexp.MustCompile(`^owned-index-[1-9][0-9]*-[1-9][0-9]*$`)
var errOwnedIndexActionActive = errors.New("the prior index action is still running")

type ownedIndexRecovery struct {
	Status     string `json:"status"`
	Reason     string `json:"reason"`
	IndexOID   uint32 `json:"index_oid,omitempty"`
	IndexBytes int64  `json:"index_bytes,omitempty"`
	intent     json.RawMessage
}

func (r ownedIndexRecovery) settled() bool {
	switch r.Status {
	case "exact_index_present", "equivalent_index_present", "index_absent", "index_invalid",
		"index_name_occupied", "target_changed":
		return true
	default:
		return false
	}
}

// inspectOwnedIndexIntent classifies a recorded action on its dedicated direct
// owner connection. Catalog evidence alone cannot establish who created the
// index or whether the workload improved.
func inspectOwnedIndexIntent(ctx context.Context, db *pgx.Conn, project string, target ProjectIndexTarget,
	ownerOID uint32, columns []int16, intent ProjectIndexActionIntent) (recovery ownedIndexRecovery, err error) {
	changed := ownedIndexRecovery{Status: "target_changed", Reason: "The recorded index action no longer matches this project and table."}
	prefix := "supabase/" + OwnedIndexRookeryRef + "/"
	installation, validProject := strings.CutPrefix(project, prefix)
	if !validProject || !ownedIndexScope.MatchString(target.Schema) || target.Schema != "pt_hosted_ix_"+installation ||
		target.Relation != "workload" || target.DatabaseOID == 0 || target.TableOID == 0 || ownerOID == 0 ||
		target != intent.Target || !ownedIndexRunID.MatchString(intent.RunID) || intent.RelationBytes <= 0 ||
		intent.RelationBytes > projectIndexMaxRelationBytes || len(columns) < 1 || len(columns) > 2 ||
		len(columns) != len(intent.Keys) {
		return changed, nil
	}
	name := fmt.Sprintf("pooltune_ix_%d", target.TableOID)
	for i, column := range columns {
		if column <= 0 || (i > 0 && column == columns[0]) || intent.Keys[i].Number != column || intent.Keys[i].Name == "" {
			return changed, nil
		}
		name += fmt.Sprintf("_%d", column)
	}
	if name != intent.IndexName {
		return changed, nil
	}
	if db == nil || db.PgConn().TxStatus() != 'I' {
		return recovery, errors.New("index recovery requires an idle owner connection")
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var backendPID uint32
	if err := db.QueryRow(ctx, "SELECT pg_catalog.pg_backend_pid()").Scan(&backendPID); err != nil {
		return recovery, err
	}
	if backendPID != db.PgConn().PID() {
		return recovery, errors.New("index recovery requires a native PostgreSQL session")
	}
	var locked bool
	if err := db.QueryRow(ctx, "SELECT pg_catalog.pg_try_advisory_lock($1,$2)", projectIndexLockNamespace, int32(target.TableOID)).Scan(&locked); err != nil {
		// A lost acknowledgement could leave the lock held until this session closes.
		closeVacuumConnection(db)
		return recovery, err
	}
	if !locked {
		return ownedIndexRecovery{Status: "action_active", Reason: "An index action is still running on this table."}, nil
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		var released bool
		unlockErr := db.QueryRow(cleanup, "SELECT pg_catalog.pg_advisory_unlock($1,$2)", projectIndexLockNamespace, int32(target.TableOID)).Scan(&released)
		if unlockErr != nil || !released {
			closeVacuumConnection(db)
			err = errors.Join(err, errors.New("index recovery could not release its lock"), unlockErr)
		}
	}()

	err = pgx.BeginTxFunc(ctx, db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL statement_timeout='750ms'"); err != nil {
			return err
		}
		var owned bool
		err := tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
			WHERE c.oid=$1 AND c.relname::text=$2 AND n.nspname::text=$3
			  AND c.relowner=$4 AND n.nspowner=$4
			  AND c.relowner=current_user::regrole::oid AND current_user=session_user
			  AND obj_description(n.oid,'pg_namespace')=$5
		)`, target.TableOID, target.Relation, target.Schema, ownerOID, "Pooltune hosted index fixture "+project).Scan(&owned)
		if err != nil {
			return err
		}
		if !owned {
			recovery = changed
			return nil
		}

		var names []string
		var covering bool
		err = tx.QueryRow(ctx, projectIndexCatalogSQL, target.DatabaseOID, target.TableOID, target.Schema, target.Relation, columns).Scan(&names, &covering)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !slices.EqualFunc(names, intent.Keys,
			func(name string, key ProjectIndexKey) bool { return name == key.Name })) {
			recovery = changed
			return nil
		}
		if err != nil {
			return err
		}

		var onTarget, live bool
		err = tx.QueryRow(ctx, `
			SELECT COALESCE(i.indrelid=$3,false), COALESCE(i.indisvalid AND i.indisready AND i.indislive,false)
			FROM pg_class ix JOIN pg_namespace n ON n.oid=ix.relnamespace
			LEFT JOIN pg_index i ON i.indexrelid=ix.oid
			WHERE n.nspname::text=$1 AND ix.relname::text=$2
		`, target.Schema, name, target.TableOID).Scan(&onTarget, &live)
		switch {
		case errors.Is(err, pgx.ErrNoRows) && covering:
			recovery = ownedIndexRecovery{Status: "equivalent_index_present", Reason: "A different index already covers the lookup. Pooltune left it in place."}
			return nil
		case errors.Is(err, pgx.ErrNoRows):
			recovery = ownedIndexRecovery{Status: "index_absent", Reason: "The planned index is absent."}
			return nil
		case err != nil:
			return err
		case onTarget && !live:
			recovery = ownedIndexRecovery{Status: "index_invalid", Reason: "An invalid index occupies the planned name. Pooltune left it in place."}
			return nil
		}
		recovery = ownedIndexRecovery{Status: "index_name_occupied", Reason: "An object occupies the planned index name. Pooltune left it in place."}
		if !onTarget {
			return nil
		}
		var oid uint32
		var bytes int64
		var exact bool
		if err := tx.QueryRow(ctx, projectIndexReadbackSQL, target.TableOID, target.Schema, name, columns).Scan(&oid, &bytes, &exact); err != nil {
			return err
		}
		if exact && bytes > 0 && bytes <= projectIndexMaxIndexBytes {
			recovery = ownedIndexRecovery{Status: "exact_index_present", IndexOID: oid, IndexBytes: bytes,
				Reason: "A valid index matches the recorded definition. Its performance benefit remains unverified."}
		}
		return nil
	})
	return recovery, err
}
