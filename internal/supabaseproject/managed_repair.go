package supabaseproject

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"pooltune/internal/labdb"

	"github.com/jackc/pgx/v5"
)

// ValidateManagedRepair checks the native credential before a hosted project is
// activated. Per-target grants and selected operations are checked at action time.
func (p Project) ValidateManagedRepair(ctx context.Context, password string, ca []byte) error {
	if password == "" || len(password) > 512 {
		return errors.New("enter the project's database password to enable managed repairs")
	}
	config, err := DirectConfig(p.Ref(), "postgres", password, ca)
	if err != nil {
		return errors.New("the managed repair connection requires the project's trusted database certificate")
	}
	config.RuntimeParams["application_name"] = "pooltune-managed-repair-check"
	config.RuntimeParams["statement_timeout"] = "2s"
	ctx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	db, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return errors.New("could not verify repair access; check the database password and direct connection availability")
	}
	defer labdb.Close(db)
	return p.VerifyManagedRepair(ctx, db)
}

// VerifyManagedRepair verifies a native, non-superuser owner login against the
// active monitor installation. It does not broaden the owner's repair selections.
func (p Project) VerifyManagedRepair(ctx context.Context, db *pgx.Conn) error {
	if p.ref == "" || p.key == "" || db == nil {
		return errors.New("managed repair access requires the connected project's database login")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var pid int32
	var valid bool
	err := db.QueryRow(ctx, `SELECT pg_catalog.pg_backend_pid(),
        current_user='postgres' AND session_user=current_user
        AND pg_catalog.current_database()='postgres'
        AND NOT pg_catalog.pg_is_in_recovery()
        AND pg_catalog.current_setting('transaction_read_only')='off'
        AND EXISTS (SELECT FROM pg_catalog.pg_roles r WHERE r.rolname=current_user
            AND r.rolcanlogin AND NOT r.rolsuper
            AND (r.rolvaliduntil IS NULL OR r.rolvaliduntil>pg_catalog.statement_timestamp()))`).Scan(&pid, &valid)
	if err != nil || !valid || pid != int32(db.PgConn().PID()) {
		return errors.New("managed repairs require the project's native, writable postgres login")
	}
	return p.VerifyRepairInstallation(ctx, db)
}

// InstallManagedIndexReader adds the read-only discovered-index reader to an
// existing, authenticated monitor installation. Repeating setup is safe.
func (p Project) InstallManagedIndexReader(ctx context.Context, token string) error {
	if p.ref == "" || p.key == "" || token == "" || len(token) > 512 || strings.ContainsAny(token, " \t\r\n") {
		return ErrSetupPermission
	}
	return p.postSetup(ctx, setupHTTP, token, p.managedIndexReaderSQL())
}

func (p Project) managedInstallationGuardSQL() string {
	return fmt.Sprintf(`BEGIN;
SET LOCAL lock_timeout='2s';
SET LOCAL statement_timeout='5s';
DO $identity$
BEGIN
    IF NOT EXISTS (
        SELECT FROM pg_catalog.pg_roles r JOIN pg_catalog.pg_namespace n ON n.nspname=r.rolname
        WHERE r.rolname='%[1]s' AND r.rolcanlogin AND n.nspowner<>r.oid
          AND pg_catalog.obj_description(n.oid,'pg_namespace')='%[2]s'
          AND pg_catalog.shobj_description(r.oid,'pg_authid')='%[2]s'
          AND NOT (r.rolsuper OR r.rolinherit OR r.rolcreaterole OR r.rolcreatedb OR r.rolbypassrls OR r.rolreplication)
          AND NOT EXISTS (SELECT FROM pg_catalog.pg_auth_members m WHERE m.member=r.oid)
    ) THEN
        RAISE EXCEPTION 'the connected monitor installation could not be verified';
    END IF;
END
$identity$;
`, p.Role(), p.bootstrapMarker())
}

func (p Project) managedIndexReaderSQL() string {
	script := strings.Replace(p.DiscoveredIndexEvidenceSQL(), "BEGIN;", p.managedInstallationGuardSQL(), 1)
	return strings.Replace(script, "CREATE FUNCTION ", "CREATE OR REPLACE FUNCTION ", 1)
}
