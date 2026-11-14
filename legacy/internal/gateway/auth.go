package gateway

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"time"
)

// Approve is owner-only. The random installation token is returned once; only its
// hash is stored. A copied token shares the original account's quotas.
func (s *Store) Approve(account string, allowance int64, expires time.Time) (string, error) {
	if !idPattern.MatchString(account) || allowance <= 0 || allowance > s.limits.GlobalMicros || !expires.After(time.Now()) || expires.After(time.Now().Add(366*24*time.Hour)) {
		return "", ErrInvalid
	}
	token, e := randomID()
	if e != nil {
		return "", ErrUnavailable
	}
	hash := sha256.Sum256([]byte(token))
	e = s.transaction(func(c *sql.Conn) error {
		if _, e := c.ExecContext(context.Background(), "INSERT INTO accounts(id,token_hash,enabled,allowance,expires) VALUES(?,?,1,?,?)", account, hash[:], allowance, expires.UnixNano()); e != nil {
			return ErrInvalid
		}
		_, e := c.ExecContext(context.Background(), "INSERT INTO audit(account,kind,reference,at) VALUES(?,'approved','',?)", account, time.Now().UnixNano())
		if e != nil {
			return ErrUnavailable
		}
		return nil
	})
	if e != nil {
		return "", e
	}
	return token, nil
}
func (s *Store) Revoke(account string) error {
	return s.transaction(func(c *sql.Conn) error {
		r, e := c.ExecContext(context.Background(), "UPDATE accounts SET enabled=0 WHERE id=?", account)
		if e != nil {
			return ErrUnavailable
		}
		n, _ := r.RowsAffected()
		if n != 1 {
			return ErrInvalid
		}
		_, e = c.ExecContext(context.Background(), "INSERT INTO audit(account,kind,reference,at) VALUES(?,'revoked','',?)", account, time.Now().UnixNano())
		if e != nil {
			return ErrUnavailable
		}
		return nil
	})
}
func (s *Store) Rotate(account string, expires time.Time) (string, error) {
	if !expires.After(time.Now()) || expires.After(time.Now().Add(366*24*time.Hour)) {
		return "", ErrInvalid
	}
	token, e := randomID()
	if e != nil {
		return "", ErrUnavailable
	}
	hash := sha256.Sum256([]byte(token))
	e = s.transaction(func(c *sql.Conn) error {
		r, e := c.ExecContext(context.Background(), "UPDATE accounts SET token_hash=?,expires=? WHERE id=? AND enabled=1", hash[:], expires.UnixNano(), account)
		if e != nil {
			return ErrUnavailable
		}
		n, _ := r.RowsAffected()
		if n != 1 {
			return ErrDenied
		}
		_, e = c.ExecContext(context.Background(), "INSERT INTO audit(account,kind,reference,at) VALUES(?,'rotated','',?)", account, time.Now().UnixNano())
		if e != nil {
			return ErrUnavailable
		}
		return nil
	})
	if e != nil {
		return "", e
	}
	return token, nil
}
