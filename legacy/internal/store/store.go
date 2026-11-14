package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema embed.FS

type Store struct {
	db *sql.DB
	mu sync.Mutex // One writer, including rebuild and backup.
}

func OpenStore(path string) (*Store, error) { return openStore(path, true) }

// OpenExistingControlStore performs no schema initialization or migration. Offline
// recovery must never turn an unrelated SQLite file into a control installation.
func OpenExistingControlStore(path string) (*Store, error) { return openStore(path, false) }
func openStore(path string, initialize bool) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("store: database path must be absolute")
	}
	if !initialize {
		info, e := os.Lstat(path)
		if e != nil || !info.Mode().IsRegular() {
			return nil, errors.New("store: existing control database required")
		}
	}
	if err := secureDatabasePath(path); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	if !initialize {
		q.Set("mode", "rw")
	}
	for _, p := range []string{"journal_mode(WAL)", "foreign_keys(1)", "synchronous(FULL)", "busy_timeout(5000)"} {
		q.Add("_pragma", p)
	}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if initialize {
		data, err := schema.ReadFile("schema.sql")
		if err != nil {
			db.Close()
			return nil, err
		}
		if _, err := db.Exec(string(data)); err != nil {
			db.Close()
			return nil, err
		}
	}
	var version int
	if err := db.QueryRow("SELECT version FROM schema_version WHERE id=1").Scan(&version); err != nil {
		db.Close()
		return nil, err
	}
	if version != 1 {
		db.Close()
		return nil, fmt.Errorf("store: unsupported schema version %d", version)
	}
	if !initialize {
		var owners int
		if e := db.QueryRow("SELECT count(*) FROM admin_owner WHERE id=1").Scan(&owners); e != nil || owners != 1 {
			db.Close()
			return nil, errors.New("store: initialized owner required")
		}
	}
	return &Store{db: db}, nil
}

func secureDatabasePath(path string) error {
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return errors.New("store: database directory must not be group/world writable")
	}
	info, err = os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if err != nil {
			return err
		}
		return f.Close()
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("store: database must be a restricted regular file")
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Append(e Event) error {
	if err := e.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.immediate(func(conn *sql.Conn) error {
		var existing Event
		var kind string
		err := conn.QueryRowContext(context.Background(), "SELECT id, idempotency_key, target, actor, kind, revision, predecessor FROM events WHERE id=? OR idempotency_key=?", e.ID, e.IdempotencyKey).
			Scan(&existing.ID, &existing.IdempotencyKey, &existing.Target, &existing.Actor, &kind, &existing.Revision, &existing.Predecessor)
		if err == nil {
			existing.Kind = EventKind(kind)
			if sameEvent(existing, e) {
				return nil
			}
			return ErrConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var rejected int
		if err := conn.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM activation_rejections WHERE target=? AND revision=?", e.Target, e.Revision).Scan(&rejected); err != nil {
			return err
		}
		if rejected != 0 {
			return ErrConflict
		}
		if _, err := conn.ExecContext(context.Background(), "INSERT INTO events(id,idempotency_key,target,actor,kind,revision,predecessor,recorded_at) VALUES(?,?,?,?,?,?,?,?)", e.ID, e.IdempotencyKey, e.Target, e.Actor, e.Kind, e.Revision, e.Predecessor, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		return applyEvent(conn, e)
	})
}

func (s *Store) immediate(fn func(*sql.Conn) error) error {
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	if err := fn(conn); err != nil {
		return err
	}
	_, err = conn.ExecContext(context.Background(), "COMMIT")
	return err
}

func applyEvent(conn *sql.Conn, e Event) error {
	ctx := context.Background()
	switch e.Kind {
	case KindIntent:
		if _, err := conn.ExecContext(ctx, "INSERT OR IGNORE INTO targets(target,revision) VALUES(?, '')", e.Target); err != nil {
			return err
		}
		var current string
		if err := conn.QueryRowContext(ctx, "SELECT revision FROM targets WHERE target=?", e.Target).Scan(&current); err != nil {
			return err
		}
		if current != e.Predecessor {
			return ErrConflict
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO revisions(target,id,predecessor,actor,state) VALUES(?,?,?,?, 'pending')", e.Target, e.Revision, e.Predecessor, e.Actor); err != nil {
			return ErrConflict
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO operations(target,revision,state) VALUES(?,?,'pending')", e.Target, e.Revision); err != nil {
			return err
		}
		return nil
	case KindUncertain, KindConfirmed:
		var predecessor, state string
		err := conn.QueryRowContext(ctx, "SELECT r.predecessor,o.state FROM revisions r JOIN operations o ON o.target=r.target AND o.revision=r.id WHERE r.target=? AND r.id=?", e.Target, e.Revision).Scan(&predecessor, &state)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		if predecessor != e.Predecessor || (state != "pending" && state != "uncertain") {
			return ErrConflict
		}
		if e.Kind == KindUncertain {
			if _, err := conn.ExecContext(ctx, "UPDATE operations SET state='uncertain' WHERE target=? AND revision=?", e.Target, e.Revision); err != nil {
				return err
			}
			_, err = conn.ExecContext(ctx, "UPDATE revisions SET state='uncertain' WHERE target=? AND id=?", e.Target, e.Revision)
			return err
		}
		result, err := conn.ExecContext(ctx, "UPDATE targets SET revision=? WHERE target=? AND revision=?", e.Revision, e.Target, e.Predecessor)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrConflict
		}
		if e.Predecessor != "" {
			if _, err := conn.ExecContext(ctx, "UPDATE revisions SET state='superseded' WHERE target=? AND id=? AND state='active'", e.Target, e.Predecessor); err != nil {
				return err
			}
		}
		if _, err := conn.ExecContext(ctx, "UPDATE revisions SET state='active' WHERE target=? AND id=?", e.Target, e.Revision); err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, "UPDATE operations SET state='confirmed' WHERE target=? AND revision=?", e.Target, e.Revision)
		return err
	}
	return errors.New("store: unreachable event kind")
}
