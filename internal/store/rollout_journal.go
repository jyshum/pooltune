package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Rollout records are bounded, local control state with no request/secret data.
func (s *Store) LatestRollout(target string) (int64, json.RawMessage, error) {
	if !identifier.MatchString(target) {
		return 0, nil, ErrConflict
	}
	var seq int64
	var raw string
	e := s.db.QueryRow("SELECT sequence,payload FROM rollout_events WHERE target=? ORDER BY sequence DESC LIMIT 1", target).Scan(&seq, &raw)
	if errors.Is(e, sql.ErrNoRows) {
		return 0, nil, nil
	}
	return seq, json.RawMessage(raw), e
}
func (s *Store) AppendRollout(target, eventID string, expected int64, payload json.RawMessage) (int64, error) {
	if !identifier.MatchString(target) || !identifier.MatchString(eventID) || expected < 0 || expected >= 1<<52 || len(payload) > 1<<20 || !json.Valid(payload) {
		return 0, ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var next int64
	e := s.immediate(func(c *sql.Conn) error {
		var existing string
		var seq int64
		err := c.QueryRowContext(context.Background(), "SELECT sequence,payload FROM rollout_events WHERE target=? AND event_id=?", target, eventID).Scan(&seq, &existing)
		if err == nil {
			if seq != expected+1 || existing != string(payload) {
				return ErrConflict
			}
			next = seq
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err = c.QueryRowContext(context.Background(), "SELECT COALESCE(MAX(sequence),0) FROM rollout_events WHERE target=?", target).Scan(&seq); err != nil {
			return err
		}
		if seq != expected {
			return ErrConflict
		}
		next = seq + 1
		_, err = c.ExecContext(context.Background(), "INSERT INTO rollout_events(target,sequence,event_id,payload,recorded_at) VALUES(?,?,?,?,?)", target, next, eventID, string(payload), time.Now().UnixNano())
		return err
	})
	return next, e
}

func (s *Store) RolloutCandidate(target, candidate string) (json.RawMessage, error) {
	if !identifier.MatchString(target) || !identifier.MatchString(candidate) {
		return nil, ErrConflict
	}
	var raw string
	e := s.db.QueryRow("SELECT payload FROM rollout_events WHERE target=? AND json_extract(payload,'$.Experiment.CandidateRevision')=? ORDER BY sequence DESC LIMIT 1", target, candidate).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	return json.RawMessage(raw), e
}
