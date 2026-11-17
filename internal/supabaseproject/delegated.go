package supabaseproject

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"weak"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// TemporaryLogin is issued by the fixed Management API endpoint, never a form.
type TemporaryLogin struct {
	Role      string    `json:"role"`
	Password  string    `json:"password"`
	ExpiresAt time.Time `json:"expires_at"`
}
type delegatedContextKey struct{}
type verifiedSession struct {
	conn   weak.Pointer[pgx.Conn]
	expiry time.Time
}
type delegatedConnector struct {
	project     Project
	config      *pgx.ConnConfig
	credentials func(context.Context, string) (TemporaryLogin, error)
	verified    sync.Map
}

// WithDelegatedRepair binds all native repair connections to one selected project.
// The resolver must enforce the account, installation epoch and current provider access.
// A stale password requests one serialized invalidation/remint, without deleting CLI roles.
func WithDelegatedRepair(ctx context.Context, p Project, ca []byte, get func(context.Context, string) (TemporaryLogin, error)) (context.Context, *pgx.ConnConfig, error) {
	cfg, err := DirectConfig(p.Ref(), "cli_login_postgres", "provider-resolved", ca)
	if err != nil || get == nil {
		return ctx, nil, errors.New("delegated repair connection unavailable")
	}
	return context.WithValue(ctx, delegatedContextKey{}, &delegatedConnector{project: p, config: cfg, credentials: get}), cfg, nil
}

// ConnectRepair preserves direct logins and resolves temporary credentials for
// every new managed session, including connections cloned for measurement locks.
func ConnectRepair(ctx context.Context, cfg *pgx.ConnConfig) (*pgx.Conn, error) {
	if cfg == nil {
		return nil, errors.New("repair connection unavailable")
	}
	if cfg.User != "cli_login_postgres" {
		return pgx.ConnectConfig(ctx, cfg.Copy())
	}
	provider, ok := ctx.Value(delegatedContextKey{}).(*delegatedConnector)
	if !ok || cfg.Host != provider.config.Host || cfg.Port != provider.config.Port || cfg.Database != provider.config.Database {
		return nil, errors.New("temporary repair login requires its project connector")
	}
	provider.verified.Range(func(key, value any) bool {
		if entry := value.(verifiedSession); entry.conn.Value() == nil || !time.Now().Before(entry.expiry) {
			provider.verified.Delete(key)
		}
		return true
	})
	stale := ""
	for attempt := 0; attempt < 2; attempt++ {
		login, err := provider.credentials(ctx, stale)
		if err != nil {
			return nil, err
		}
		if login.Role != "cli_login_postgres" || login.Password == "" || len(login.Password) > 1024 || !time.Now().Add(5*time.Second).Before(login.ExpiresAt) {
			return nil, errors.New("Supabase returned unusable temporary database access")
		}
		fresh := provider.config.Copy()
		fresh.Password = login.Password
		// Preserve the caller's bounded action settings, never its TLS or destination.
		fresh.RuntimeParams = cfg.Copy().RuntimeParams
		fresh.OnNotice = cfg.OnNotice
		db, err := pgx.ConnectConfig(ctx, fresh)
		if err != nil {
			var pgErr *pgconn.PgError
			if attempt == 0 && errors.As(err, &pgErr) && pgErr.Code == "28P01" {
				stale = login.Password
				continue
			}
			return nil, errors.New("could not open verified temporary database access")
		}
		if err = prepareDelegatedSession(ctx, db); err == nil {
			provider.verified.Store(db.PgConn().PID(), verifiedSession{weak.Make(db), login.ExpiresAt})
			err = provider.project.VerifyManagedRepair(ctx, db)
		}
		if err != nil {
			provider.verified.Delete(db.PgConn().PID())
			_ = db.Close(context.Background())
			return nil, err
		}
		return db, nil
	}
	return nil, errors.New("temporary database login could not be renewed")
}

// RepairSessionNeedsRenewal reports whether a collector must replace its native
// session before starting work with the given bounded duration. It does not extend
// authority on the old connection; ConnectRepair must authenticate its replacement.
func RepairSessionNeedsRenewal(ctx context.Context, db *pgx.Conn, duration time.Duration) bool {
	if db == nil || db.IsClosed() {
		return true
	}
	if db.Config().User != "cli_login_postgres" {
		return false
	}
	provider, ok := ctx.Value(delegatedContextKey{}).(*delegatedConnector)
	if !ok {
		return true
	}
	entry, ok := provider.verified.Load(db.PgConn().PID())
	if !ok {
		return true
	}
	verified := entry.(verifiedSession)
	return verified.conn.Value() != db || !time.Now().Add(duration).Before(verified.expiry)
}

const delegatedRoleSQL = `current_user='postgres' AND session_user='cli_login_postgres'
 AND EXISTS(SELECT FROM pg_catalog.pg_roles login JOIN pg_catalog.pg_roles owner ON owner.rolname='postgres'
 WHERE login.rolname=session_user AND login.rolcanlogin AND NOT login.rolsuper
 AND login.rolvaliduntil>pg_catalog.statement_timestamp() AND NOT owner.rolsuper
 AND pg_catalog.pg_has_role(login.oid,owner.oid,'SET'))`

func prepareDelegatedSession(ctx context.Context, db *pgx.Conn) error {
	var pid int32
	var valid bool
	err := db.QueryRow(ctx, `SELECT pg_catalog.pg_backend_pid(),current_user=session_user AND session_user='cli_login_postgres'
 AND current_database()='postgres' AND NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off'
 AND EXISTS(SELECT FROM pg_catalog.pg_roles login JOIN pg_catalog.pg_roles owner ON owner.rolname='postgres'
 WHERE login.rolname=session_user AND login.rolcanlogin AND NOT login.rolsuper AND NOT owner.rolsuper
 AND login.rolvaliduntil>statement_timestamp() AND pg_has_role(login.oid,owner.oid,'SET'))`).Scan(&pid, &valid)
	if err != nil || !valid || pid != int32(db.PgConn().PID()) {
		return errors.New("temporary native database identity was rejected")
	}
	if _, err = db.Exec(ctx, "SET SESSION ROLE postgres"); err != nil {
		return errors.New("temporary database role selection was rejected")
	}
	return nil
}

// RepairIdentitySQL widens equality only on a connection that this operation's
// project connector authenticated and verified. It rechecks membership and expiry
// inside the same catalog/action query. Application-role checks do not use it.
func RepairIdentitySQL(ctx context.Context, q any) string {
	var db *pgx.Conn
	switch v := q.(type) {
	case *pgx.Conn:
		db = v
	case pgx.Tx:
		db = v.Conn()
	}
	if db == nil {
		return "current_user=session_user"
	}
	if db.Config().User != "cli_login_postgres" {
		return "current_user=session_user"
	}
	provider, ok := ctx.Value(delegatedContextKey{}).(*delegatedConnector)
	if !ok {
		return "false"
	}
	entry, ok := provider.verified.Load(db.PgConn().PID())
	if !ok {
		return "false"
	}
	verified := entry.(verifiedSession)
	if verified.conn.Value() != db || !time.Now().Before(verified.expiry) {
		return "false"
	}
	return fmt.Sprintf("(%s AND pg_catalog.pg_backend_pid()=%d AND pg_catalog.statement_timestamp()<pg_catalog.to_timestamp(%d))", delegatedRoleSQL, db.PgConn().PID(), verified.expiry.Unix())
}

// RepairQuery substitutes the identity fence only for the specific native repair
// catalog statements whose caller already enforces project and target authority.
func RepairQuery(ctx context.Context, q any, sql string) string {
	return strings.ReplaceAll(sql, "current_user=session_user", RepairIdentitySQL(ctx, q))
}
