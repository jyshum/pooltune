// Package auth stores preview accounts and server-side sessions in Pooltune state.
package auth

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	SessionTTL = 24 * time.Hour

	passwordIterations = 600_000 // OWASP work factor for PBKDF2-HMAC-SHA256.
	saltBytes          = 16
	tokenBytes         = 32
	minPasswordRunes   = 15
	maxPasswordBytes   = 1024
	loginAttempts      = 8
	loginWindow        = 15 * time.Minute
)

var (
	ErrInvalidInput       = errors.New("invalid email or password")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrDuplicate          = errors.New("account already exists")
	ErrRateLimited        = errors.New("sign-in rate limit reached")
)

type User struct {
	ID    int64
	Email string
}

type Store struct {
	pool      *pgxpool.Pool
	dummySalt [saltBytes]byte
}

// New opens the account store on Pooltune's state database and initializes its schema.
func New(ctx context.Context, pool *pgxpool.Pool) (*Store, error) {
	store := &Store{pool: pool}
	if _, err := rand.Read(store.dummySalt[:]); err != nil {
		return nil, storeError("initialize credential check", err)
	}

	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(761994345023)"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			CREATE SCHEMA IF NOT EXISTS pooltune_app;
			CREATE TABLE IF NOT EXISTS pooltune_app.users (
				id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
				email text NOT NULL UNIQUE,
				password_salt bytea NOT NULL CHECK (octet_length(password_salt) = 16),
				password_hash bytea NOT NULL CHECK (octet_length(password_hash) = 32),
				password_iterations integer NOT NULL CHECK (password_iterations BETWEEN 600000 AND 2000000),
				login_attempts integer NOT NULL DEFAULT 0,
				login_window_start timestamptz
			);
			CREATE TABLE IF NOT EXISTS pooltune_app.sessions (
				token_hash bytea PRIMARY KEY CHECK (octet_length(token_hash) = 32),
				user_id bigint NOT NULL REFERENCES pooltune_app.users(id) ON DELETE CASCADE,
				expires_at timestamptz NOT NULL
			);
			CREATE INDEX IF NOT EXISTS sessions_by_user ON pooltune_app.sessions(user_id, expires_at);
			REVOKE ALL ON TABLE pooltune_app.users, pooltune_app.sessions FROM PUBLIC;
		`)
		return err
	})
	if err != nil {
		return nil, storeError("initialize account store", err)
	}
	return store, nil
}

func (s *Store) Register(ctx context.Context, email, password string) (User, string, error) {
	address := canonicalEmail(email)
	if address == "" || !passwordInput(password) || utf8.RuneCountInString(password) < minPasswordRunes {
		return User{}, "", ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return User{}, "", err
	}
	var salt [saltBytes]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return User{}, "", storeError("generate password salt", err)
	}
	hash, err := pbkdf2.Key(sha256.New, password, salt[:], passwordIterations, sha256.Size)
	if err != nil {
		return User{}, "", storeError("derive password hash", err)
	}

	user := User{Email: address}
	var token string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO pooltune_app.users (email, password_salt, password_hash, password_iterations)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (email) DO NOTHING
			RETURNING id
		`, address, salt[:], hash, passwordIterations).Scan(&user.ID); err != nil {
			return err
		}
		var sessionErr error
		token, sessionErr = issueSession(ctx, tx, user.ID)
		return sessionErr
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, "", ErrDuplicate
	}
	if err != nil {
		return User{}, "", storeError("register account", err)
	}
	return user, token, nil
}

func (s *Store) SignIn(ctx context.Context, email, password string) (User, string, error) {
	address := canonicalEmail(email)
	if address == "" || !passwordInput(password) {
		return User{}, "", ErrInvalidCredentials
	}

	user := User{Email: address}
	var salt, hash []byte
	var iterations int
	// Reserve a slot before deriving the password hash so concurrent requests
	// cannot all pass a separate read of the account's remaining attempts.
	err := s.pool.QueryRow(ctx, `
		WITH limits AS (
			SELECT statement_timestamp() - ($2::bigint * interval '1 second') AS expired
		), reserved AS (
			UPDATE pooltune_app.users AS u
			SET login_attempts = CASE WHEN login_window_start > limits.expired THEN login_attempts + 1 ELSE 1 END,
			    login_window_start = CASE WHEN login_window_start > limits.expired THEN login_window_start ELSE statement_timestamp() END
			FROM limits
			WHERE u.email = $1
			  AND (login_window_start IS NULL OR login_window_start <= limits.expired OR login_attempts < $3)
			RETURNING u.id, u.password_salt, u.password_hash, u.password_iterations
		)
		SELECT u.id, r.password_salt, r.password_hash, COALESCE(r.password_iterations, 0)
		FROM pooltune_app.users AS u LEFT JOIN reserved AS r USING (id)
		WHERE u.email = $1
	`, address, int64(loginWindow/time.Second), loginAttempts).Scan(&user.ID, &salt, &hash, &iterations)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return User{}, "", storeError("reserve sign-in", err)
	}

	// Use equivalent work for unknown and throttled identities. The application
	// independently limits request volume to bound this CPU cost.
	unknown := errors.Is(err, pgx.ErrNoRows)
	limited := !unknown && iterations == 0
	if unknown || limited {
		salt, hash, iterations = s.dummySalt[:], make([]byte, sha256.Size), passwordIterations
	}
	derived, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	if err != nil {
		return User{}, "", storeError("check password", err)
	}
	matches := subtle.ConstantTimeCompare(derived, hash) == 1
	if limited {
		return User{}, "", ErrRateLimited
	}
	if unknown || !matches {
		return User{}, "", ErrInvalidCredentials
	}

	var token string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE pooltune_app.users SET login_attempts = 0, login_window_start = NULL WHERE id = $1
		`, user.ID); err != nil {
			return err
		}
		var sessionErr error
		token, sessionErr = issueSession(ctx, tx, user.ID)
		return sessionErr
	})
	if err != nil {
		return User{}, "", storeError("start session", err)
	}
	return user, token, nil
}

func (s *Store) UserBySession(ctx context.Context, token string) (User, error) {
	digest, ok := sessionDigest(token)
	if !ok {
		return User{}, ErrInvalidCredentials
	}
	var user User
	err := s.pool.QueryRow(ctx, `
		SELECT u.id, u.email
		FROM pooltune_app.sessions AS s JOIN pooltune_app.users AS u ON s.user_id = u.id
		WHERE s.token_hash = $1 AND s.expires_at > clock_timestamp()
	`, digest[:]).Scan(&user.ID, &user.Email)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrInvalidCredentials
	}
	if err != nil {
		return User{}, storeError("read session", err)
	}
	return user, nil
}

func (s *Store) SignOut(ctx context.Context, token string) error {
	digest, ok := sessionDigest(token)
	if !ok {
		return nil
	}
	if _, err := s.pool.Exec(ctx, "DELETE FROM pooltune_app.sessions WHERE token_hash = $1", digest[:]); err != nil {
		return storeError("close session", err)
	}
	return nil
}

func issueSession(ctx context.Context, tx pgx.Tx, userID int64) (string, error) {
	var raw [tokenBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw[:])
	if _, err := tx.Exec(ctx, `
		DELETE FROM pooltune_app.sessions WHERE user_id = $1 AND expires_at <= clock_timestamp()
	`, userID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO pooltune_app.sessions (token_hash, user_id, expires_at)
		VALUES ($1, $2, clock_timestamp() + ($3::bigint * interval '1 second'))
	`, digest[:], userID, int64(SessionTTL/time.Second)); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func sessionDigest(token string) ([sha256.Size]byte, bool) {
	if len(token) != base64.RawURLEncoding.EncodedLen(tokenBytes) {
		return [sha256.Size]byte{}, false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != tokenBytes {
		return [sha256.Size]byte{}, false
	}
	return sha256.Sum256(raw), true
}

func canonicalEmail(value string) string {
	if len(value) > 512 || !utf8.ValidString(value) {
		return ""
	}
	address := strings.ToLower(strings.TrimSpace(value))
	if len(address) == 0 || len(address) > 254 {
		return ""
	}
	parsed, err := mail.ParseAddress(address)
	if err != nil || parsed.Name != "" || parsed.Address != address {
		return ""
	}
	return address
}

func passwordInput(password string) bool {
	return len(password) > 0 && len(password) <= maxPasswordBytes && utf8.ValidString(password)
}

// PostgreSQL errors can contain bound values. Return only the operation and code.
func storeError(operation string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return fmt.Errorf("%s failed (SQLSTATE %s)", operation, pgErr.Code)
	}
	return fmt.Errorf("%s failed", operation)
}
