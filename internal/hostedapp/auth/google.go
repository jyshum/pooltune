package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const GoogleFlowTTL = 10 * time.Minute

var (
	ErrGoogleUnknown      = errors.New("Google account is not registered")
	ErrGoogleLinkRequired = errors.New("sign in to your existing account to link Google")
)

func prepareGoogle(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `
 ALTER TABLE pooltune_app.users ALTER COLUMN password_salt DROP NOT NULL,
   ALTER COLUMN password_hash DROP NOT NULL, ALTER COLUMN password_iterations DROP NOT NULL;
 DO $$ BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'pooltune_app.users'::regclass AND conname = 'password_all_or_none') THEN
 ALTER TABLE pooltune_app.users ADD CONSTRAINT password_all_or_none CHECK (
 (password_salt IS NULL AND password_hash IS NULL AND password_iterations IS NULL) OR
 (password_salt IS NOT NULL AND password_hash IS NOT NULL AND password_iterations IS NOT NULL));
 END IF; END $$;
 CREATE TABLE IF NOT EXISTS pooltune_app.google_identities (
 subject text PRIMARY KEY CHECK (length(subject) BETWEEN 1 AND 255),
 user_id bigint NOT NULL UNIQUE REFERENCES pooltune_app.users(id) ON DELETE CASCADE
 );
 CREATE TABLE IF NOT EXISTS pooltune_app.google_flows (
 state_hash bytea PRIMARY KEY CHECK (octet_length(state_hash) = 32),
 browser_hash bytea NOT NULL CHECK (octet_length(browser_hash) = 32),
 mode text NOT NULL CHECK (mode IN ('signin','signup','link')),
 nonce text NOT NULL, verifier text NOT NULL,
 link_user_id bigint REFERENCES pooltune_app.users(id) ON DELETE CASCADE,
 link_session_hash bytea,
 expires_at timestamptz NOT NULL,
 CHECK ((mode = 'link' AND link_user_id IS NOT NULL AND link_session_hash IS NOT NULL) OR
 (mode <> 'link' AND link_user_id IS NULL AND link_session_hash IS NULL))
 );
 CREATE INDEX IF NOT EXISTS google_flows_expiry ON pooltune_app.google_flows(expires_at);
 REVOKE ALL ON TABLE pooltune_app.google_identities, pooltune_app.google_flows FROM PUBLIC;
 `)
	return err
}

type GoogleFlow struct {
	State, Mode, Nonce, Verifier string
	LinkUserID                   int64
	sessionHash                  []byte
}

func (f GoogleFlow) MatchesSession(token string) bool {
	digest, ok := sessionDigest(token)
	return ok && subtle.ConstantTimeCompare(digest[:], f.sessionHash) == 1
}

func googleRandom() string {
	var raw [32]byte
	_, _ = rand.Read(raw[:])
	return base64.RawURLEncoding.EncodeToString(raw[:])
}

func (s *Store) BeginGoogleFlow(ctx context.Context, mode string, userID int64, session string) (GoogleFlow, string, error) {
	if mode != "signin" && mode != "signup" && mode != "link" {
		return GoogleFlow{}, "", ErrInvalidInput
	}
	flow := GoogleFlow{State: googleRandom(), Mode: mode, Nonce: googleRandom(), Verifier: googleRandom(), LinkUserID: userID}
	var linkID any
	if mode == "link" {
		user, err := s.UserBySession(ctx, session)
		if err != nil || user.ID != userID {
			return GoogleFlow{}, "", ErrInvalidCredentials
		}
		digest, _ := sessionDigest(session)
		flow.sessionHash = digest[:]
		linkID = userID
	} else if userID != 0 || session != "" {
		return GoogleFlow{}, "", ErrInvalidInput
	}
	browser := googleRandom()
	stateHash, _ := sessionDigest(flow.State)
	browserHash, _ := sessionDigest(browser)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM pooltune_app.google_flows WHERE expires_at <= clock_timestamp()`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO pooltune_app.google_flows(state_hash,browser_hash,mode,nonce,verifier,link_user_id,link_session_hash,expires_at)
   VALUES ($1,$2,$3,$4,$5,$6,$7,clock_timestamp() + interval '10 minutes')`, stateHash[:], browserHash[:], mode, flow.Nonce, flow.Verifier, linkID, flow.sessionHash)
		return err
	})
	if err != nil {
		return GoogleFlow{}, "", storeError("begin Google sign-in", err)
	}
	return flow, browser, nil
}

func (s *Store) ConsumeGoogleFlow(ctx context.Context, state, browser string) (GoogleFlow, error) {
	stateHash, ok := sessionDigest(state)
	browserHash, browserOK := sessionDigest(browser)
	if !ok || !browserOK {
		return GoogleFlow{}, ErrInvalidCredentials
	}
	var flow GoogleFlow
	err := s.pool.QueryRow(ctx, `DELETE FROM pooltune_app.google_flows WHERE state_hash=$1 AND browser_hash=$2 AND expires_at > clock_timestamp()
 RETURNING mode,nonce,verifier,COALESCE(link_user_id,0),link_session_hash`, stateHash[:], browserHash[:]).Scan(&flow.Mode, &flow.Nonce, &flow.Verifier, &flow.LinkUserID, &flow.sessionHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return GoogleFlow{}, ErrInvalidCredentials
	}
	if err != nil {
		return GoogleFlow{}, storeError("consume Google sign-in", err)
	}
	return flow, nil
}

// GoogleAccount accepts only a subject and verified email from the OIDC verifier.
// Signup callers must first validate the preview invite. Linking requires the same
// live session that initiated the flow, checked again inside this transaction.
func (s *Store) GoogleAccount(ctx context.Context, mode, subject, email string, linkID int64, linkSession string) (User, string, error) {
	address := canonicalEmail(email)
	if address == "" || len(subject) == 0 || len(subject) > 255 || (mode != "signin" && mode != "signup" && mode != "link") {
		return User{}, "", ErrInvalidInput
	}
	var user User
	var token string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Serialize registrations for a subject across replicas without locking all accounts.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 621783))`, subject); err != nil {
			return err
		}
		var linkUser User
		if mode == "link" {
			digest, ok := sessionDigest(linkSession)
			if !ok {
				return ErrInvalidCredentials
			}
			err := tx.QueryRow(ctx, `SELECT u.id,u.email FROM pooltune_app.sessions s JOIN pooltune_app.users u ON u.id=s.user_id
    WHERE s.token_hash=$1 AND s.user_id=$2 AND s.expires_at>clock_timestamp() FOR SHARE OF s,u`, digest[:], linkID).Scan(&linkUser.ID, &linkUser.Email)
			if errors.Is(err, pgx.ErrNoRows) || (err == nil && linkUser.Email != address) {
				return ErrInvalidCredentials
			}
			if err != nil {
				return err
			}
		}
		err := tx.QueryRow(ctx, `SELECT u.id,u.email FROM pooltune_app.google_identities g JOIN pooltune_app.users u ON u.id=g.user_id WHERE g.subject=$1`, subject).Scan(&user.ID, &user.Email)
		if err == nil {
			if mode == "link" && user.ID != linkUser.ID {
				return ErrGoogleLinkRequired
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		} else {
			if mode == "link" {
				user = linkUser
			} else {
				var existingID int64
				e := tx.QueryRow(ctx, `SELECT id FROM pooltune_app.users WHERE email=$1`, address).Scan(&existingID)
				if e == nil {
					return ErrGoogleLinkRequired
				}
				if !errors.Is(e, pgx.ErrNoRows) {
					return e
				}
				if mode == "signin" {
					return ErrGoogleUnknown
				}
				user.Email = address
				err = tx.QueryRow(ctx, `INSERT INTO pooltune_app.users(email) VALUES($1) ON CONFLICT(email) DO NOTHING RETURNING id`, address).Scan(&user.ID)
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrGoogleLinkRequired
				}
				if err != nil {
					return err
				}
			}
			result, err := tx.Exec(ctx, `INSERT INTO pooltune_app.google_identities(subject,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, subject, user.ID)
			if err != nil {
				return err
			}
			if result.RowsAffected() != 1 {
				return ErrGoogleLinkRequired
			}
		}
		var errSession error
		token, errSession = issueSession(ctx, tx, user.ID)
		return errSession
	})
	if err != nil {
		if errors.Is(err, ErrGoogleUnknown) || errors.Is(err, ErrGoogleLinkRequired) || errors.Is(err, ErrInvalidCredentials) {
			return User{}, "", err
		}
		return User{}, "", storeError("Google account", err)
	}
	return user, token, nil
}
