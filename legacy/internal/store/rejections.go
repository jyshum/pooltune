package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"time"
)

type Rejection struct{ Revision, Checksum, Epoch, RecordedAt string }

func (s *Store) RecordRejection(target, revision, checksum string, epoch uint64) error {
	hash, err := hex.DecodeString(checksum)
	if !identifier.MatchString(target) || !identifier.MatchString(revision) || err != nil || len(hash) != 32 || epoch == 0 {
		return ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.immediate(func(c *sql.Conn) error {
		var prior Rejection
		err := c.QueryRowContext(context.Background(), "SELECT checksum,epoch FROM activation_rejections WHERE target=? AND revision=?", target, revision).Scan(&prior.Checksum, &prior.Epoch)
		if err == nil {
			if prior.Checksum != checksum || prior.Epoch != strconv.FormatUint(epoch, 10) {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var state string
		if err = c.QueryRowContext(context.Background(), "SELECT state FROM operations WHERE target=? AND revision=?", target, revision).Scan(&state); err != nil {
			return err
		}
		if state != "pending" && state != "uncertain" {
			return ErrConflict
		}
		_, err = c.ExecContext(context.Background(), "INSERT INTO activation_rejections(target,revision,checksum,epoch,recorded_at) VALUES(?,?,?,?,?)", target, revision, checksum, strconv.FormatUint(epoch, 10), time.Now().UTC().Format(time.RFC3339Nano))
		return err
	})
}
func (s *Store) Rejection(target, revision string) (Rejection, error) {
	var r Rejection
	r.Revision = revision
	err := s.db.QueryRow("SELECT checksum,epoch,recorded_at FROM activation_rejections WHERE target=? AND revision=?", target, revision).Scan(&r.Checksum, &r.Epoch, &r.RecordedAt)
	return r, err
}
func (s *Store) Rejections(target string) ([]Rejection, error) {
	rows, err := s.db.Query("SELECT revision,checksum,epoch,recorded_at FROM activation_rejections WHERE target=? ORDER BY rowid LIMIT 1001", target)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rejection
	for rows.Next() {
		var r Rejection
		if err = rows.Scan(&r.Revision, &r.Checksum, &r.Epoch, &r.RecordedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
		if len(out) > 1000 {
			return nil, ErrConflict
		}
	}
	return out, rows.Err()
}
