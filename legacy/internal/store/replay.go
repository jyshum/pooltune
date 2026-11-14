package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s *Store) ActiveRevision(target string) (Revision, error) {
	if !identifier.MatchString(target) {
		return Revision{}, errors.New("store: invalid target")
	}
	var r Revision
	err := s.db.QueryRow("SELECT r.target,r.id,r.predecessor,r.actor,r.state FROM targets t JOIN revisions r ON r.target=t.target AND r.id=t.revision WHERE t.target=?", target).
		Scan(&r.Target, &r.ID, &r.Predecessor, &r.Actor, &r.State)
	if errors.Is(err, sql.ErrNoRows) {
		return Revision{}, ErrNoActiveRevision
	}
	return r, err
}

func (s *Store) Pending(target string) ([]Revision, error) {
	if !identifier.MatchString(target) {
		return nil, errors.New("store: invalid target")
	}
	rows, err := s.db.Query("SELECT r.target,r.id,r.predecessor,r.actor,r.state FROM revisions r JOIN operations o ON o.target=r.target AND o.revision=r.id WHERE r.target=? AND o.state IN ('pending','uncertain') AND NOT EXISTS (SELECT 1 FROM activation_rejections a WHERE a.target=r.target AND a.revision=r.id) ORDER BY r.rowid LIMIT 1001", target)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Revision
	for rows.Next() {
		var r Revision
		if err := rows.Scan(&r.Target, &r.ID, &r.Predecessor, &r.Actor, &r.State); err != nil {
			return nil, err
		}
		result = append(result, r)
		if len(result) > 1000 {
			return nil, errors.New("store: pending read exceeds 1000 rows")
		}
	}
	return result, rows.Err()
}

// Events returns at most 1000 typed journal records. A larger export needs a
// future paginated API, rather than silently truncating history.
func (s *Store) Events(target string) ([]EventRecord, error) {
	if !identifier.MatchString(target) {
		return nil, errors.New("store: invalid target")
	}
	rows, err := s.db.Query("SELECT seq,recorded_at,id,idempotency_key,target,actor,kind,revision,predecessor FROM events WHERE target=? ORDER BY seq LIMIT 1001", target)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []EventRecord
	for rows.Next() {
		var e EventRecord
		var stamp string
		if err := rows.Scan(&e.Sequence, &stamp, &e.ID, &e.IdempotencyKey, &e.Target, &e.Actor, &e.Kind, &e.Revision, &e.Predecessor); err != nil {
			return nil, err
		}
		e.RecordedAt, err = time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			return nil, err
		}
		result = append(result, e)
		if len(result) > 1000 {
			return nil, errors.New("store: event read exceeds 1000 rows")
		}
	}
	return result, rows.Err()
}

// Rebuild discards only derived state, then replays committed events in order.
func (s *Store) Rebuild() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.immediate(func(conn *sql.Conn) error {
		ctx := context.Background()
		for _, table := range []string{"policies", "guardrails", "experiments", "agent_runs", "setup", "operations", "revisions", "targets"} {
			if _, err := conn.ExecContext(ctx, "DELETE FROM "+table); err != nil {
				return err
			}
		}
		rows, err := conn.QueryContext(ctx, "SELECT id,idempotency_key,target,actor,kind,revision,predecessor FROM events ORDER BY seq")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e Event
			if err := rows.Scan(&e.ID, &e.IdempotencyKey, &e.Target, &e.Actor, &e.Kind, &e.Revision, &e.Predecessor); err != nil {
				return err
			}
			if err := applyEvent(conn, e); err != nil {
				return err
			}
		}
		return rows.Err()
	})
}
