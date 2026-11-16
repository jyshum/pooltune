package hostedapp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/hostedquery"
	"pooltune/internal/supabaseproject"
)

// projectSupabaseToken checks current installation ownership and selected
// organization before each new provider/native admission. A project denial never
// revokes an account's other connections.
func (a *App) projectSupabaseToken(ctx context.Context, row projectRecord) (string, error) {
	var current bool
	err := a.projects.db.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pooltune_app.projects WHERE id=$1 AND owner_id=$2 AND epoch=$3 AND supabase_connection=$4 AND supabase_org=$5 AND NOT supabase_blocked)`, row.ID, row.Owner, row.Epoch, row.SupabaseConnection, row.SupabaseOrg).Scan(&current)
	if err != nil {
		return "", err
	}
	if !current {
		return "", hostedquery.ErrProjectDisconnected
	}
	token, err := a.supabaseToken(ctx, row.Owner, row.SupabaseConnection)
	if err != nil {
		return "", err
	}
	projects, err := a.supabase.projects(ctx, token)
	if err != nil {
		return "", err
	}
	for _, p := range projects {
		if p.Ref == row.Ref && p.Organization == row.SupabaseOrg && p.Status == "ACTIVE_HEALTHY" {
			return token, nil
		}
	}
	return "", errSupabaseProjectAccess
}

// CLI credentials are shared by Supabase per database. The transaction lock and
// encrypted cache are database-wide, so concurrent installations/replicas reuse
// the same issuance. Access is revalidated before reading even cached credentials.
func (a *App) temporaryLogin(ctx context.Context, row projectRecord, stale string) (supabaseproject.TemporaryLogin, error) {
	token, err := a.projectSupabaseToken(ctx, row)
	if err != nil {
		return supabaseproject.TemporaryLogin{}, err
	}
	var login supabaseproject.TemporaryLogin
	err = pgx.BeginFunc(ctx, a.projects.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,761994345011))`, row.Ref); err != nil {
			return err
		}
		binding := []byte("pooltune/supabase-cli/v1/" + row.Ref)
		var sealed []byte
		var expiry time.Time
		err := tx.QueryRow(ctx, `SELECT secret,expires_at FROM pooltune_app.supabase_cli_credentials WHERE ref=$1`, row.Ref).Scan(&sealed, &expiry)
		if err == nil {
			plain, e := a.vault.Open(nil, nil, sealed, binding)
			if e != nil {
				return errSupabaseUnavailable
			}
			if json.Unmarshal(plain, &login) != nil {
				return errSupabaseUnavailable
			}
			if time.Now().Add(30*time.Second).Before(expiry) && login.Password != stale {
				return nil
			}
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var response struct {
			Role     string `json:"role"`
			Password string `json:"password"`
			TTL      int64  `json:"ttl_seconds"`
		}
		before := time.Now()
		err = a.supabase.request(ctx, "POST", "/v1/projects/"+row.Ref+"/cli/login-role", token, "application/json", strings.NewReader(`{"read_only":false}`), &response)
		if err != nil {
			return err
		}
		if response.Role != "cli_login_postgres" || response.Password == "" || len(response.Password) > 1024 || strings.ContainsRune(response.Password, 0) || response.TTL < 60 || response.TTL > 86400 {
			return errors.New("Supabase returned unsupported temporary database access. Retry or reconnect")
		}
		login = supabaseproject.TemporaryLogin{Role: response.Role, Password: response.Password, ExpiresAt: before.Add(time.Duration(response.TTL) * time.Second)}
		plain, err := json.Marshal(login)
		if err != nil {
			return err
		}
		sealed = a.vault.Seal(nil, nil, plain, binding)
		_, err = tx.Exec(ctx, `INSERT INTO pooltune_app.supabase_cli_credentials(ref,secret,expires_at) VALUES($1,$2,$3) ON CONFLICT(ref) DO UPDATE SET secret=excluded.secret,expires_at=excluded.expires_at`, row.Ref, sealed, login.ExpiresAt)
		return err
	})
	return login, err
}
func (a *App) repairContext(ctx context.Context, row projectRecord, secrets projectSecrets) (context.Context, *pgx.ConnConfig, error) {
	if row.SupabaseConnection == "" {
		cfg, err := supabaseproject.DirectConfig(row.Ref, "postgres", secrets.RepairPassword, []byte(secrets.CA))
		return ctx, cfg, err
	}
	identity, err := row.identity()
	if err != nil {
		return ctx, nil, err
	}
	bound, cfg, err := supabaseproject.WithDelegatedRepair(ctx, identity, []byte(secrets.CA), func(ctx context.Context, stale string) (supabaseproject.TemporaryLogin, error) {
		return a.temporaryLogin(ctx, row, stale)
	})
	if err == nil {
		cfg.Password = secrets.Password
	} // Stable secret for existing input fingerprints; the connector resolves actual login passwords.
	return bound, cfg, err
}
func (a *App) blockSupabaseProject(ctx context.Context, row projectRecord) error {
	_, err := a.projects.db.Exec(ctx, `UPDATE pooltune_app.projects SET supabase_blocked=true WHERE id=$1 AND owner_id=$2 AND epoch=$3 AND supabase_connection=$4`, row.ID, row.Owner, row.Epoch, row.SupabaseConnection)
	return err
}
