package store

import (
	"context"
	"database/sql"
	"time"
)

// RecoverAdminPassword is only for an offline filesystem owner. The caller must
// hold the control directory's exclusive lock. No HTTP route exposes this method.
func (s *Store) RecoverAdminPassword(password []byte) error {
	if len(password) < 50 || len(password) > 128 {
		return ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.immediate(func(c *sql.Conn) error {
		var old []byte
		if e := c.QueryRowContext(context.Background(), "SELECT password_hash FROM admin_owner WHERE id=1").Scan(&old); e != nil {
			return e
		}
		if len(old) == 0 {
			return ErrConflict
		}
		if _, e := c.ExecContext(context.Background(), "UPDATE admin_owner SET password_hash=? WHERE id=1", password); e != nil {
			return e
		}
		if _, e := c.ExecContext(context.Background(), "DELETE FROM admin_sessions"); e != nil {
			return e
		}
		_, e := c.ExecContext(context.Background(), "INSERT INTO admin_audit(kind,recorded_at) VALUES('password_recovered',?)", time.Now().UnixNano())
		return e
	})
}

// ReplaceAdminClaim replaces an unclaimed installation's bootstrap token only.
// Password recovery never returns an already claimed installation to setup.
func (s *Store) ReplaceAdminClaim(claim []byte, expires time.Time) error {
	if len(claim) != 32 || !expires.After(time.Now()) || expires.After(time.Now().Add(24*time.Hour)) {
		return ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.immediate(func(c *sql.Conn) error {
		var password []byte
		if e := c.QueryRowContext(context.Background(), "SELECT password_hash FROM admin_owner WHERE id=1").Scan(&password); e != nil {
			return e
		}
		if len(password) > 0 {
			return ErrConflict
		}
		if _, e := c.ExecContext(context.Background(), "UPDATE admin_owner SET claim_hash=?,claim_expires=? WHERE id=1", claim, expires.UnixNano()); e != nil {
			return e
		}
		_, e := c.ExecContext(context.Background(), "INSERT INTO admin_audit(kind,recorded_at) VALUES('claim_replaced',?)", time.Now().UnixNano())
		return e
	})
}
