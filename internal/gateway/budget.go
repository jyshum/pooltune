// Package gateway lives only in the Pooltune-owned service, never customer traffic.
package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	_ "modernc.org/sqlite"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

var (
	ErrDenied      = errors.New("gateway access denied")
	ErrBudget      = errors.New("gateway budget unavailable")
	ErrDuplicate   = errors.New("gateway step already reserved")
	ErrInvalid     = errors.New("invalid gateway request")
	ErrUnavailable = errors.New("gateway unavailable")
)
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type Limits struct {
	GlobalMicros                                                        int64
	MaxConcurrent, MaxAccountConcurrent, RequestsPerMinute, MaxRunSteps int
	RunTTL                                                              time.Duration
}
type Store struct {
	db     *sql.DB
	mu     sync.Mutex
	limits Limits
}
type Account struct {
	ID              string
	AllowanceMicros int64
}
type Reservation struct {
	ID, Account, Run string
	Step             int
	MaxMicros        int64
}
type Balance struct{ SpentMicros, ReservedMicros int64 }

func Open(path string, limits Limits) (*Store, error) {
	if !filepath.IsAbs(path) || limits.GlobalMicros <= 0 || limits.GlobalMicros > 1000000000000 || limits.MaxConcurrent < 1 || limits.MaxConcurrent > 64 || limits.MaxAccountConcurrent < 1 || limits.MaxAccountConcurrent > limits.MaxConcurrent || limits.RequestsPerMinute < 1 || limits.RequestsPerMinute > 1000 || limits.MaxRunSteps < 1 || limits.MaxRunSteps > 8 || limits.RunTTL <= 0 || limits.RunTTL > time.Hour {
		return nil, ErrInvalid
	}
	parent, e := os.Stat(filepath.Dir(path))
	if e != nil || !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return nil, ErrInvalid
	}
	if info, e := os.Lstat(path); e == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return nil, ErrInvalid
		}
	} else if errors.Is(e, os.ErrNotExist) {
		f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if e != nil {
			return nil, e
		}
		f.Close()
	} else {
		return nil, e
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	for _, v := range []string{"journal_mode(WAL)", "synchronous(FULL)", "foreign_keys(1)", "busy_timeout(5000)"} {
		q.Add("_pragma", v)
	}
	u.RawQuery = q.Encode()
	db, e := sql.Open("sqlite", u.String())
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	_, e = db.Exec(`CREATE TABLE IF NOT EXISTS gateway_profile(id INTEGER PRIMARY KEY CHECK(id=1),limits_json TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS accounts(id TEXT PRIMARY KEY,token_hash BLOB NOT NULL UNIQUE,enabled INTEGER NOT NULL,allowance INTEGER NOT NULL,expires INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS runs(id TEXT PRIMARY KEY,account TEXT NOT NULL REFERENCES accounts(id),created INTEGER NOT NULL,expires INTEGER NOT NULL,next_step INTEGER NOT NULL DEFAULT 0);
 CREATE INDEX IF NOT EXISTS runs_account_created ON runs(account,created);
 CREATE TABLE IF NOT EXISTS reservations(id TEXT PRIMARY KEY,account TEXT NOT NULL REFERENCES accounts(id),run TEXT NOT NULL REFERENCES runs(id),step INTEGER NOT NULL,payload_hash TEXT NOT NULL,max_cost INTEGER NOT NULL,cost INTEGER,state TEXT NOT NULL,created INTEGER NOT NULL,UNIQUE(account,run,step));
 CREATE INDEX IF NOT EXISTS reservations_account ON reservations(account,state);
 CREATE TABLE IF NOT EXISTS audit(seq INTEGER PRIMARY KEY AUTOINCREMENT,account TEXT NOT NULL,kind TEXT NOT NULL,reference TEXT NOT NULL,at INTEGER NOT NULL);
 CREATE TRIGGER IF NOT EXISTS audit_no_update BEFORE UPDATE ON audit BEGIN SELECT RAISE(ABORT,'append-only'); END;
 CREATE TRIGGER IF NOT EXISTS audit_no_delete BEFORE DELETE ON audit BEGIN SELECT RAISE(ABORT,'append-only'); END;`)
	if e != nil {
		db.Close()
		return nil, ErrUnavailable
	}
	raw, _ := json.Marshal(limits)
	if _, e = db.Exec("INSERT OR IGNORE INTO gateway_profile(id,limits_json) VALUES(1,?)", string(raw)); e != nil {
		db.Close()
		return nil, ErrUnavailable
	}
	var saved string
	if e = db.QueryRow("SELECT limits_json FROM gateway_profile WHERE id=1").Scan(&saved); e != nil || saved != string(raw) {
		db.Close()
		return nil, ErrInvalid
	}
	return &Store{db: db, limits: limits}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func randomID() (string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func (s *Store) transaction(fn func(*sql.Conn) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, e := s.db.Conn(context.Background())
	if e != nil {
		return ErrUnavailable
	}
	defer c.Close()
	if _, e = c.ExecContext(context.Background(), "BEGIN IMMEDIATE"); e != nil {
		return ErrUnavailable
	}
	defer c.ExecContext(context.Background(), "ROLLBACK")
	if e = fn(c); e != nil {
		return e
	}
	if _, e = c.ExecContext(context.Background(), "COMMIT"); e != nil {
		return ErrUnavailable
	}
	return nil
}
func auth(c *sql.Conn, token string) (Account, error) {
	if len(token) != 43 {
		return Account{}, ErrDenied
	}
	hash := sha256.Sum256([]byte(token))
	var a Account
	err := c.QueryRowContext(context.Background(), "SELECT id,allowance FROM accounts WHERE token_hash=? AND enabled=1 AND expires>?", hash[:], time.Now().UnixNano()).Scan(&a.ID, &a.AllowanceMicros)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrDenied
	}
	if err != nil {
		return Account{}, ErrUnavailable
	}
	return a, nil
}
func (s *Store) Authorize(token string) (Account, error) {
	var a Account
	err := s.transaction(func(c *sql.Conn) error { var e error; a, e = auth(c, token); return e })
	return a, err
}
func (s *Store) NewRun(token string) (string, error) {
	id, err := randomID()
	if err != nil {
		return "", ErrUnavailable
	}
	err = s.transaction(func(c *sql.Conn) error {
		a, e := auth(c, token)
		if e != nil {
			return e
		}
		var count int
		if e = c.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM runs WHERE account=? AND created>?", a.ID, time.Now().Add(-time.Minute).UnixNano()).Scan(&count); e != nil {
			return ErrUnavailable
		}
		if count >= s.limits.RequestsPerMinute {
			return ErrDenied
		}
		now := time.Now()
		_, e = c.ExecContext(context.Background(), "INSERT INTO runs(id,account,created,expires) VALUES(?,?,?,?)", id, a.ID, now.UnixNano(), now.Add(s.limits.RunTTL).UnixNano())
		if e != nil {
			return ErrUnavailable
		}
		return nil
	})
	return id, err
}
func (s *Store) Reserve(token, run string, step int, payloadHash string, maxCost int64) (Reservation, error) {
	if step < 0 || step >= s.limits.MaxRunSteps || maxCost <= 0 || maxCost > s.limits.GlobalMicros || len(run) != 43 || len(payloadHash) < 1 || len(payloadHash) > 128 {
		return Reservation{}, ErrBudget
	}
	id, err := randomID()
	if err != nil {
		return Reservation{}, ErrUnavailable
	}
	var out Reservation
	err = s.transaction(func(c *sql.Conn) error {
		a, e := auth(c, token)
		if e != nil {
			return e
		}
		var next int
		if e = c.QueryRowContext(context.Background(), "SELECT next_step FROM runs WHERE id=? AND account=? AND expires>?", run, a.ID, time.Now().UnixNano()).Scan(&next); e != nil {
			return ErrDenied
		}
		var duplicate int
		if e = c.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM reservations WHERE account=? AND run=? AND step=?", a.ID, run, step).Scan(&duplicate); e != nil {
			return ErrUnavailable
		}
		if duplicate != 0 {
			return ErrDuplicate
		}
		if step != next {
			return ErrDenied
		}
		var global, account int64
		var globalActive, accountActive, recent int
		if e = c.QueryRowContext(context.Background(), "SELECT COALESCE(SUM(CASE WHEN state='settled' THEN cost ELSE max_cost END),0),COALESCE(SUM(CASE WHEN state!='settled' THEN 1 ELSE 0 END),0) FROM reservations").Scan(&global, &globalActive); e != nil {
			return ErrUnavailable
		}
		if e = c.QueryRowContext(context.Background(), "SELECT COALESCE(SUM(CASE WHEN state='settled' THEN cost ELSE max_cost END),0),COALESCE(SUM(CASE WHEN state!='settled' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN created>? THEN 1 ELSE 0 END),0) FROM reservations WHERE account=?", time.Now().Add(-time.Minute).UnixNano(), a.ID).Scan(&account, &accountActive, &recent); e != nil {
			return ErrUnavailable
		}
		if maxCost > s.limits.GlobalMicros-global || maxCost > a.AllowanceMicros-account {
			return ErrBudget
		}
		if globalActive >= s.limits.MaxConcurrent || accountActive >= s.limits.MaxAccountConcurrent || recent >= s.limits.RequestsPerMinute {
			return ErrDenied
		}
		if _, e = c.ExecContext(context.Background(), "INSERT INTO reservations(id,account,run,step,payload_hash,max_cost,state,created) VALUES(?,?,?,?,?,?,'pending',?)", id, a.ID, run, step, payloadHash, maxCost, time.Now().UnixNano()); e != nil {
			return ErrUnavailable
		}
		if _, e = c.ExecContext(context.Background(), "UPDATE runs SET next_step=next_step+1 WHERE id=?", run); e != nil {
			return ErrUnavailable
		}
		out = Reservation{ID: id, Account: a.ID, Run: run, Step: step, MaxMicros: maxCost}
		return nil
	})
	return out, err
}
func (s *Store) Uncertain(id string) error {
	return s.transaction(func(c *sql.Conn) error {
		r, e := c.ExecContext(context.Background(), "UPDATE reservations SET state='uncertain' WHERE id=? AND state IN ('pending','uncertain')", id)
		if e != nil {
			return ErrUnavailable
		}
		n, _ := r.RowsAffected()
		if n != 1 {
			return ErrInvalid
		}
		return nil
	})
}
func (s *Store) Settle(id string, cost int64) error {
	return s.transaction(func(c *sql.Conn) error {
		var maxCost int64
		var prior sql.NullInt64
		var state string
		if e := c.QueryRowContext(context.Background(), "SELECT max_cost,cost,state FROM reservations WHERE id=?", id).Scan(&maxCost, &prior, &state); e != nil {
			return ErrInvalid
		}
		if cost < 0 || cost > maxCost {
			return ErrInvalid
		}
		if state == "settled" {
			if prior.Valid && prior.Int64 == cost {
				return nil
			}
			return ErrDuplicate
		}
		if _, e := c.ExecContext(context.Background(), "UPDATE reservations SET cost=?,state='settled' WHERE id=?", cost, id); e != nil {
			return ErrUnavailable
		}
		return nil
	})
}
func (s *Store) Usage(account string) (Balance, error) {
	var b Balance
	e := s.db.QueryRow("SELECT COALESCE(SUM(CASE WHEN state='settled' THEN cost ELSE 0 END),0),COALESCE(SUM(CASE WHEN state!='settled' THEN max_cost ELSE 0 END),0) FROM reservations WHERE account=?", account).Scan(&b.SpentMicros, &b.ReservedMicros)
	if e != nil {
		return Balance{}, ErrUnavailable
	}
	return b, nil
}
