package hostedquery

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ManagedMemoryEnrollment is explicit owner intent from the catalog picker.
// The fingerprint pins the complete pg_proc row and namespace identity.
type ManagedMemoryEnrollment struct {
	Target      ProjectFunctionMemoryTarget `json:"target"`
	Fingerprint string                      `json:"fingerprint"`
	Generation  string                      `json:"generation"`
}

type ManagedMemoryFunction struct {
	Target                  ProjectFunctionMemoryTarget   `json:"target"`
	Snapshot                ProjectFunctionMemorySnapshot `json:"snapshot"`
	OriginalDefinition      string                        `json:"original_definition"`
	OriginalBody            string                        `json:"original_body"`
	CatalogFingerprint      string                        `json:"catalog_fingerprint"`
	Query                   string                        `json:"query"`
	Table                   ProjectIndexTarget            `json:"table"`
	ValueColumn, SortColumn string
	ValueColumnNumber       int16
	RelationBytes           int64 `json:"relation_bytes"`
}

// Deliberately narrow executable grammar: one built-in sum over one explicit
// serial sort. Everything executed is rebuilt from catalog-pinned identifiers.
// PL/pgSQL variables, dynamic SQL, subcalls, arguments and side effects refuse.
const managedIdent = `(?:"(?:[^"]|"")+"|[a-zA-Z_][a-zA-Z0-9_$]*)`

var managedMemorySource = regexp.MustCompile(`(?is)^\s*BEGIN\s+RETURN\s*\(\s*SELECT\s+(?:pg_catalog\.)?sum\s*\(\s*(` + managedIdent + `)\s*\)\s+FROM\s*\(\s*SELECT\s+(` + managedIdent + `)\s+FROM\s+ONLY\s+(` + managedIdent + `)\s*\.\s*(` + managedIdent + `)\s+ORDER\s+BY\s+(` + managedIdent + `)\s*,\s*(` + managedIdent + `)\s+OFFSET\s+0\s*\)\s+(?:AS\s+)?sorted\s*\)\s*;\s*END\s*;?\s*$`)

func managedUnquoteIdentifier(raw string) string {
	if strings.HasPrefix(raw, `"`) {
		return strings.ReplaceAll(raw[1:len(raw)-1], `""`, `"`)
	}
	return strings.ToLower(raw)
}

func ReadManagedMemoryFunction(ctx context.Context, db *pgx.Conn, enrollment ManagedMemoryEnrollment) (ManagedMemoryFunction, error) {
	out := ManagedMemoryFunction{Target: enrollment.Target}
	t := enrollment.Target
	snapshot, err := ReadProjectFunctionMemory(ctx, db, t)
	if err != nil {
		return out, err
	}
	out.Snapshot = snapshot
	var canonical []byte
	var body, definition string
	err = db.QueryRow(ctx, `SELECT p.prosrc,pg_get_functiondef(p.oid),(to_jsonb(p)||jsonb_build_object('namespace_oid',n.oid))::text FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE p.oid=$1 AND p.pronargs=0 AND p.prorettype='int8'::regtype AND NOT p.proretset AND p.provolatile='v' AND p.prolang=(SELECT oid FROM pg_language WHERE lanname='plpgsql') AND 'search_path=pg_catalog'=ANY(p.proconfig) AND 'jit=off'=ANY(p.proconfig)`, t.FunctionOID).Scan(&body, &definition, &canonical)
	if err != nil {
		return out, err
	}
	out.CatalogFingerprint = fmt.Sprintf("%x", sha256.Sum256(canonical))
	if enrollment.Generation != "" && enrollment.Generation != snapshot.Generation {
		return out, errors.New("the selected function generation changed")
	}
	// Enrollment adapters may use their existing canonical catalog fingerprint;
	// exact snapshot generation still fences changes before this action.
	if enrollment.Fingerprint != "" && enrollment.Fingerprint != out.CatalogFingerprint {
		return out, errors.New("the selected function fingerprint changed")
	}
	parts := managedMemorySource.FindStringSubmatch(body)
	if len(parts) != 7 {
		return out, errors.New("function body is outside the supported read-only single-sort shape")
	}
	names := make([]string, 6)
	for i := range names {
		names[i] = managedUnquoteIdentifier(parts[i+1])
	}
	if names[0] != names[1] || names[0] != names[5] || names[0] == names[4] {
		return out, errors.New("the function has unmatched sort and aggregate columns")
	}
	out.Table = ProjectIndexTarget{DatabaseOID: t.DatabaseOID, Schema: names[2], Relation: names[3]}
	out.ValueColumn, out.SortColumn = names[0], names[4]
	var valid bool
	err = db.QueryRow(ctx, `SELECT c.oid,pg_total_relation_size(c.oid),c.relkind='r' AND NOT c.relispartition AND NOT c.relrowsecurity AND NOT c.relforcerowsecurity AND NOT c.relhasrules AND NOT EXISTS(SELECT FROM pg_inherits WHERE inhrelid=c.oid OR inhparent=c.oid) AND (SELECT count(*) FROM pg_attribute a WHERE a.attrelid=c.oid AND NOT a.attisdropped AND ((a.attname=$3 AND a.atttypid='int4'::regtype) OR (a.attname=$4 AND a.atttypid='text'::regtype AND a.attcollation='pg_catalog."default"'::regcollation)))=2,(SELECT attnum FROM pg_attribute WHERE attrelid=c.oid AND attname=$3 AND NOT attisdropped) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2`, out.Table.Schema, out.Table.Relation, out.ValueColumn, out.SortColumn).Scan(&out.Table.TableOID, &out.RelationBytes, &valid, &out.ValueColumnNumber)
	if err != nil || !valid || out.RelationBytes <= 0 || out.RelationBytes > projectIndexMaxRelationBytes {
		return out, errors.Join(err, errors.New("the memory relation is outside the bounded read-only envelope"))
	}
	if snapshot.WorkMemBytes < 64<<10 || snapshot.WorkMemBytes >= 32<<20 {
		return out, errors.New("function baseline memory is outside the supported range")
	}
	out.OriginalBody, out.OriginalDefinition = body, definition
	val, sort := pgx.Identifier{out.ValueColumn}.Sanitize(), pgx.Identifier{out.SortColumn}.Sanitize()
	out.Query = "SELECT pg_catalog.sum(" + val + ") FROM (SELECT " + val + " FROM ONLY " + qualified(out.Table.Schema, out.Table.Relation) + " ORDER BY " + sort + "," + val + " OFFSET 0) AS sorted"
	return out, nil
}

func managedMemoryDefinition(original, body string) (string, error) {
	// pg_get_functiondef emits a dollar-quoted body. Replacing exactly its body
	// preserves OID, signature, result, volatility, settings and permissions.
	start := strings.Index(original, "AS $function$")
	end := strings.LastIndex(original, "$function$")
	if start < 0 || end <= start+len("AS $function$") {
		return "", errors.New("unsupported canonical function definition")
	}
	return original[:start] + "AS '" + strings.ReplaceAll(body, "'", "''") + "'" + original[end+len("$function$"):], nil
}
