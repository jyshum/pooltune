package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"pooltune/internal/provider"
	"strings"
	"time"
)

// Load and Save implement provider.Journal on the control service's SQLite store.
func (s *Store) Load(target string) (*provider.Operation, error) {
	if !identifier.MatchString(target) {
		return nil, ErrConflict
	}
	var raw string
	err := s.db.QueryRow("SELECT payload FROM provider_events WHERE target=? ORDER BY seq DESC LIMIT 1", target).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var op provider.Operation
	if json.Unmarshal([]byte(raw), &op) != nil || op.Target.ID != target {
		return nil, ErrConflict
	}
	return &op, nil
}
func (s *Store) Save(op provider.Operation) error {
	if !identifier.MatchString(op.Target.ID) || !identifier.MatchString(op.Revision) || op.Value < 0 || op.Value > 1000000 || op.StartedAt.IsZero() || len(op.ProviderID) > 8192 {
		return ErrConflict
	}
	switch op.Kind {
	case provider.LocalPgBouncer:
		if op.Parameter != "pool_size" || op.Value < 1 || op.ProviderID != "" {
			return ErrConflict
		}
	case provider.Azure:
		if op.Parameter != "pgbouncer.default_pool_size" && op.Parameter != "pgbouncer.min_pool_size" {
			return ErrConflict
		}
		if op.ProviderID != "" {
			u, e := url.Parse(op.ProviderID)
			if e != nil || u.User != nil || u.Fragment != "" || !strings.HasPrefix(u.Path, "/subscriptions/") {
				return ErrConflict
			}
		}
	case provider.Google:
		if op.Parameter != "max_pool_size" && op.Parameter != "min_pool_size" {
			return ErrConflict
		}
		if op.ProviderID != "" && !identifier.MatchString(op.ProviderID) {
			return ErrConflict
		}
	default:
		return ErrConflict
	}
	switch op.State {
	case "intent", "accepted", "uncertain", "failed", "complete":
	default:
		return ErrConflict
	}
	raw, err := json.Marshal(op)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.immediate(func(conn *sql.Conn) error {
		var previous string
		err := conn.QueryRowContext(context.Background(), "SELECT payload FROM provider_events WHERE target=? ORDER BY seq DESC LIMIT 1", op.Target.ID).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if previous == string(raw) {
			return nil
		}
		if err == nil {
			var prior provider.Operation
			if json.Unmarshal([]byte(previous), &prior) != nil {
				return ErrConflict
			}
			if prior.Revision != op.Revision {
				if prior.State != "complete" || op.State != "intent" {
					return ErrConflict
				}
				var count int
				if err = conn.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM provider_events WHERE target=? AND revision=?", op.Target.ID, op.Revision).Scan(&count); err != nil {
					return err
				}
				if count != 0 {
					return ErrConflict
				}
			} else {
				if prior.State == "complete" || prior.State == "failed" || op.State == "intent" {
					return ErrConflict
				}
				identity := prior
				identity.State = op.State
				identity.ProviderID = op.ProviderID
				if identity != op {
					return ErrConflict
				}
				if prior.ProviderID != "" && prior.ProviderID != op.ProviderID {
					return ErrConflict
				}
			}
		} else if op.State != "intent" {
			return ErrConflict
		}
		_, err = conn.ExecContext(context.Background(), "INSERT INTO provider_events(target,revision,state,payload,recorded_at) VALUES(?,?,?,?,?)", op.Target.ID, op.Revision, op.State, string(raw), time.Now().UTC().Format(time.RFC3339Nano))
		return err
	})
}
