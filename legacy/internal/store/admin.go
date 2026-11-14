package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"time"
)

func (s *Store) InitAdmin(hash []byte, expires time.Time) error {
	if len(hash) != 32 {
		return ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.immediate(func(c *sql.Conn) error {
		var existing, password []byte
		e := c.QueryRowContext(context.Background(), "SELECT claim_hash,password_hash FROM admin_owner WHERE id=1").Scan(&existing, &password)
		if e == nil {
			if len(password) > 0 || subtle.ConstantTimeCompare(existing, hash) == 1 {
				return nil
			}
			return ErrConflict
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if !expires.After(time.Now()) || expires.After(time.Now().Add(24*time.Hour)) {
			return ErrConflict
		}
		_, e = c.ExecContext(context.Background(), "INSERT INTO admin_owner(id,claim_hash,claim_expires) VALUES(1,?,?)", hash, expires.UnixNano())
		return e
	})
}
func (s *Store) AdminPasswordHash() ([]byte, error) {
	var hash []byte
	e := s.db.QueryRow("SELECT password_hash FROM admin_owner WHERE id=1").Scan(&hash)
	return hash, e
}
func (s *Store) ClaimAdmin(claim, password, session []byte, csrf string, expires time.Time) error {
	if len(claim) != 32 || len(password) < 50 || len(password) > 128 || len(session) != 32 || len(csrf) != 43 {
		return ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.immediate(func(c *sql.Conn) error {
		var existing, owner []byte
		var until int64
		if e := c.QueryRowContext(context.Background(), "SELECT claim_hash,claim_expires,password_hash FROM admin_owner WHERE id=1").Scan(&existing, &until, &owner); e != nil {
			return e
		}
		if len(owner) > 0 || until <= time.Now().UnixNano() || subtle.ConstantTimeCompare(existing, claim) != 1 {
			return ErrConflict
		}
		if _, e := c.ExecContext(context.Background(), "UPDATE admin_owner SET password_hash=?,claimed_at=? WHERE id=1", password, time.Now().UnixNano()); e != nil {
			return e
		}
		if e := insertAdminSession(c, session, csrf, expires); e != nil {
			return e
		}
		_, e := c.ExecContext(context.Background(), "INSERT INTO admin_audit(kind,recorded_at) VALUES('claimed',?)", time.Now().UnixNano())
		return e
	})
}
func insertAdminSession(c *sql.Conn, hash []byte, csrf string, expires time.Time) error {
	if len(hash) != 32 || len(csrf) != 43 || !expires.After(time.Now()) || expires.After(time.Now().Add(24*time.Hour)) {
		return ErrConflict
	}
	if _, e := c.ExecContext(context.Background(), "DELETE FROM admin_sessions WHERE expires<=?", time.Now().UnixNano()); e != nil {
		return e
	}
	var count int
	if e := c.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM admin_sessions").Scan(&count); e != nil {
		return e
	}
	if count >= 8 {
		return ErrConflict
	}
	_, e := c.ExecContext(context.Background(), "INSERT INTO admin_sessions(token_hash,csrf,expires) VALUES(?,?,?)", hash, csrf, expires.UnixNano())
	return e
}
func (s *Store) CreateAdminSession(password, hash []byte, csrf string, expires time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.immediate(func(c *sql.Conn) error {
		var current []byte
		if e := c.QueryRowContext(context.Background(), "SELECT password_hash FROM admin_owner WHERE id=1").Scan(&current); e != nil {
			return e
		}
		if len(current) == 0 || subtle.ConstantTimeCompare(current, password) != 1 {
			return ErrConflict
		}
		if e := insertAdminSession(c, hash, csrf, expires); e != nil {
			return e
		}
		_, e := c.ExecContext(context.Background(), "INSERT INTO admin_audit(kind,recorded_at) VALUES('login',?)", time.Now().UnixNano())
		return e
	})
}
func (s *Store) AdminSession(hash []byte) (string, time.Time, error) {
	var csrf string
	var expires int64
	if len(hash) != 32 {
		return "", time.Time{}, ErrConflict
	}
	e := s.db.QueryRow("SELECT csrf,expires FROM admin_sessions WHERE token_hash=? AND expires>?", hash, time.Now().UnixNano()).Scan(&csrf, &expires)
	return csrf, time.Unix(0, expires), e
}
func (s *Store) RevokeAdminSession(hash []byte, all bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.immediate(func(c *sql.Conn) error {
		var count int
		if e := c.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM admin_sessions WHERE token_hash=? AND expires>?", hash, time.Now().UnixNano()).Scan(&count); e != nil {
			return e
		}
		if count != 1 {
			return ErrConflict
		}
		query := "DELETE FROM admin_sessions WHERE token_hash=?"
		args := []any{hash}
		kind := "logout"
		if all {
			query = "DELETE FROM admin_sessions"
			args = nil
			kind = "logout_all"
		}
		if _, e := c.ExecContext(context.Background(), query, args...); e != nil {
			return e
		}
		_, e := c.ExecContext(context.Background(), "INSERT INTO admin_audit(kind,recorded_at) VALUES(?,?)", kind, time.Now().UnixNano())
		return e
	})
}
