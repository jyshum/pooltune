package hostedquery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ProjectIndexTarget describes an exact relation selected by the caller's
// authorization logic on a verified project route. OIDs are not global IDs.
type ProjectIndexTarget struct {
	DatabaseOID uint32
	TableOID    uint32
	Schema      string
	Relation    string
}

type ProjectIndexKey struct {
	Number int16
	Name   string
}

type ProjectIndexCatalog struct {
	Target   ProjectIndexTarget
	Keys     []ProjectIndexKey
	Covering bool
}

var ErrProjectIndexTargetChanged = errors.New("project index relation or key identity did not match")

// ResolveProjectIndex reads the current names for an already authorized choice
// of target and keys. Covering means a live, ready, valid, unconditional B-tree
// has the same leading keys, default operator classes, attribute collations and
// default ordering. The caller must separately check permissions and capacity
// and revalidate the catalog at the point of a repair.
func ResolveProjectIndex(ctx context.Context, db *pgx.Conn, target ProjectIndexTarget, column int16, columns []int16) (ProjectIndexCatalog, error) {
	if db == nil || target.DatabaseOID == 0 || target.TableOID == 0 || target.Schema == "" || target.Relation == "" || !ValidIndexColumns(column, columns) {
		return ProjectIndexCatalog{}, errors.New("invalid project index target or keys")
	}
	if len(columns) == 0 {
		columns = []int16{column}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var names []string
	var covering bool
	err := db.QueryRow(ctx, projectIndexCatalogSQL, target.DatabaseOID, target.TableOID, target.Schema, target.Relation, columns).Scan(&names, &covering)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectIndexCatalog{}, ErrProjectIndexTargetChanged
	}
	if err != nil {
		return ProjectIndexCatalog{}, fmt.Errorf("read project index catalog: %w", err)
	}
	keys := make([]ProjectIndexKey, len(columns))
	for i, number := range columns {
		keys[i] = ProjectIndexKey{Number: number, Name: names[i]}
	}
	return ProjectIndexCatalog{Target: target, Keys: keys, Covering: covering}, nil
}

const projectIndexCatalogSQL = `
	WITH target AS (
		SELECT c.oid
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_catalog.pg_database d ON d.datname = pg_catalog.current_database()
		WHERE d.oid = $1 AND c.oid = $2 AND n.nspname::text = $3 AND c.relname::text = $4
		  AND c.relkind = 'r' AND NOT c.relispartition AND c.relpersistence <> 't'
		  AND pg_catalog.left(n.nspname::text, 3) <> 'pg_' AND n.nspname <> 'information_schema'
	), keys AS (
		SELECT k.position, a.attnum, a.attname, a.attcollation
		FROM target t
		JOIN pg_catalog.pg_attribute a ON a.attrelid = t.oid AND a.attnum > 0 AND NOT a.attisdropped
		JOIN pg_catalog.unnest($5::smallint[]) WITH ORDINALITY k (number, position) ON k.number = a.attnum
	)
	SELECT ARRAY(SELECT attname::text FROM keys ORDER BY position), EXISTS (
		SELECT FROM pg_catalog.pg_index i
		JOIN pg_catalog.pg_class ix ON ix.oid = i.indexrelid
		JOIN pg_catalog.pg_am am ON am.oid = ix.relam
		WHERE i.indrelid = t.oid AND i.indisvalid AND i.indisready AND i.indislive
		  AND i.indpred IS NULL AND am.amname = 'btree'
		  AND i.indnkeyatts >= pg_catalog.cardinality($5::smallint[])
		  AND NOT EXISTS (
			SELECT FROM keys k
			LEFT JOIN pg_catalog.pg_opclass opc ON opc.oid = i.indclass[k.position - 1]
			WHERE i.indkey[k.position - 1] <> k.attnum OR i.indcollation[k.position - 1] <> k.attcollation
			   OR i.indoption[k.position - 1] <> 0 OR opc.opcdefault IS DISTINCT FROM true
		  )
	)
	FROM target t
	WHERE (SELECT count(*) FROM keys) = pg_catalog.cardinality($5::smallint[])
`
