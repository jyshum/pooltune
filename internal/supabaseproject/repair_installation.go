package supabaseproject

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// VerifyRepairInstallation checks the identity markers installed by normal
// setup. The caller supplies a verified native connection to this project.
func (p Project) VerifyRepairInstallation(ctx context.Context, db *pgx.Conn) error {
	if p.key == "" || p.ref == "" || db == nil {
		return errors.New("repair access requires a connected project and database login")
	}
	var valid bool
	err := db.QueryRow(ctx, `SELECT EXISTS (
		SELECT FROM pg_catalog.pg_roles r JOIN pg_catalog.pg_namespace n ON n.nspname=r.rolname
		WHERE r.rolname=$1 AND n.nspowner<>r.oid AND r.rolcanlogin AND r.rolconnlimit BETWEEN 1 AND 2
		  AND pg_catalog.obj_description(n.oid,'pg_namespace')=$2
		  AND pg_catalog.shobj_description(r.oid,'pg_authid')=$2
		  AND NOT (r.rolsuper OR r.rolinherit OR r.rolcreaterole OR r.rolcreatedb OR r.rolbypassrls OR r.rolreplication)
		  AND NOT EXISTS (SELECT FROM pg_catalog.pg_auth_members m WHERE m.member=r.oid)
	)`, p.Role(), p.bootstrapMarker()).Scan(&valid)
	if err != nil || !valid {
		return errors.New("the connected project's monitor installation could not be verified")
	}
	return nil
}
