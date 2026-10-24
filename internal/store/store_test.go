package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func intent(id, key, target, revision, predecessor string) Event {
	return Event{ID: id, IdempotencyKey: key, Target: target, Actor: "operator", Kind: KindIntent, Revision: revision, Predecessor: predecessor}
}

func follow(id, key string, from Event, kind EventKind) Event {
	return Event{ID: id, IdempotencyKey: key, Target: from.Target, Actor: "operator", Kind: kind, Revision: from.Revision, Predecessor: from.Predecessor}
}

func TestIntentSurvivesReopenAndUncertaintyDoesNotActivate(t *testing.T) {
	s, path := openTestStore(t)
	i := intent("e1", "k1", "target-a", "r1", "")
	if err := s.Append(i); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(follow("e2", "k2", i, KindUncertain)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.ActiveRevision("target-a"); !errors.Is(err, ErrNoActiveRevision) {
		t.Fatalf("active before confirmation: %v", err)
	}
	pending, err := s.Pending("target-a")
	if err != nil || len(pending) != 1 || pending[0].ID != "r1" || pending[0].State != "uncertain" {
		t.Fatalf("pending: %+v, %v", pending, err)
	}
	if err := s.Append(follow("e3", "k3", i, KindConfirmed)); err != nil {
		t.Fatal(err)
	}
	active, err := s.ActiveRevision("target-a")
	if err != nil || active.ID != "r1" {
		t.Fatalf("active: %+v, %v", active, err)
	}
}

func TestIdempotencyAndPredecessorConflict(t *testing.T) {
	s, _ := openTestStore(t)
	i := intent("e1", "key", "target", "r1", "")
	if err := s.Append(i); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(i); err != nil {
		t.Fatalf("identical retry: %v", err)
	}
	changed := i
	changed.ID = "e2"
	changed.Revision = "r2"
	if err := s.Append(changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed retry: %v", err)
	}
	if err := s.Append(follow("e3", "confirm", i, KindConfirmed)); err != nil {
		t.Fatal(err)
	}
	stale := intent("e4", "stale", "target", "r3", "")
	if err := s.Append(stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale predecessor: %v", err)
	}
	events, err := s.Events("target")
	if err != nil || len(events) != 2 {
		t.Fatalf("journal count %d: %v", len(events), err)
	}
	if err := s.Append(follow("e5", "unknown", stale, KindConfirmed)); !errors.Is(err, ErrConflict) {
		t.Fatalf("unknown acknowledgment: %v", err)
	}
}

func TestConcurrentConfirmOnlyOnePredecessorWins(t *testing.T) {
	s, _ := openTestStore(t)
	a := intent("a", "a", "target", "r-a", "")
	b := intent("b", "b", "target", "r-b", "")
	for _, e := range []Event{a, b} {
		if err := s.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, e := range []Event{follow("ac", "ac", a, KindConfirmed), follow("bc", "bc", b, KindConfirmed)} {
		wg.Add(1)
		go func(e Event) { defer wg.Done(); errs <- s.Append(e) }(e)
	}
	wg.Wait()
	close(errs)
	success, conflict := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	events, err := s.Events("target")
	if err != nil || len(events) != 3 {
		t.Fatalf("events=%d err=%v", len(events), err)
	}
}

func TestConcurrentIdenticalRetryCreatesOneEvent(t *testing.T) {
	s, _ := openTestStore(t)
	e := intent("same", "same-key", "target", "r1", "")
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- s.Append(e) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	events, err := s.Events("target")
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%d err=%v", len(events), err)
	}
}

func TestRebuildRestoresProjectionAndPending(t *testing.T) {
	s, _ := openTestStore(t)
	a := intent("a", "a", "target", "r1", "")
	b := intent("b", "b", "target", "r2", "r1")
	for _, e := range []Event{a, follow("ac", "ac", a, KindConfirmed), b, follow("bu", "bu", b, KindUncertain)} {
		if err := s.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec("DELETE FROM operations; DELETE FROM revisions; DELETE FROM targets"); err != nil {
		t.Fatal(err)
	}
	if err := s.Rebuild(); err != nil {
		t.Fatal(err)
	}
	active, err := s.ActiveRevision("target")
	if err != nil || active.ID != "r1" {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	pending, err := s.Pending("target")
	if err != nil || len(pending) != 1 || pending[0].ID != "r2" || pending[0].State != "uncertain" {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
}

func TestReadonlyWriteFailureRollsBackAndBackupRestores(t *testing.T) {
	s, _ := openTestStore(t)
	a := intent("a", "a", "target", "r1", "")
	if err := s.Append(a); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(follow("ac", "ac", a, KindConfirmed)); err != nil {
		t.Fatal(err)
	}
	b := intent("b", "b", "target", "r2", "r1")
	if err := s.Append(b); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}
	err := s.Append(follow("bu", "bu", b, KindUncertain))
	if err == nil {
		t.Fatal("readonly injection did not fail")
	}
	if _, err := s.db.Exec("PRAGMA query_only=OFF"); err != nil {
		t.Fatal(err)
	}
	events, err := s.Events("target")
	if err != nil || len(events) != 3 {
		t.Fatalf("partial journal: %d %v", len(events), err)
	}
	active, err := s.ActiveRevision("target")
	if err != nil || active.ID != "r1" {
		t.Fatalf("partial activation: %+v %v", active, err)
	}
	pending, err := s.Pending("target")
	if err != nil || len(pending) != 1 || pending[0].State != "pending" {
		t.Fatalf("partial pending: %+v %v", pending, err)
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	if err := s.Backup(backup); err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(backup); err == nil {
		t.Fatal("backup overwrote destination")
	}
	if info, err := os.Stat(backup); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("backup mode: %v %v", info, err)
	}
	restored, err := OpenStore(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	active, err = restored.ActiveRevision("target")
	if err != nil || active.ID != "r1" {
		t.Fatalf("restored active: %+v %v", active, err)
	}
	pending, err = restored.Pending("target")
	if err != nil || len(pending) != 1 || pending[0].ID != "r2" {
		t.Fatalf("restored pending: %+v %v", pending, err)
	}
}

func TestEventMetadataValidationAndImmutableHistory(t *testing.T) {
	s, _ := openTestStore(t)
	i := intent("e1", "k1", "target", "r1", "")
	i.Actor = "token=secret"
	if err := s.Append(i); err == nil {
		t.Fatal("invalid identifier accepted")
	}
	i.Actor = "operator"
	if err := s.Append(i); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE events SET actor='other' WHERE id='e1'"); err == nil {
		t.Fatal("history update allowed")
	}
	if _, err := s.db.Exec("DELETE FROM events WHERE id='e1'"); err == nil {
		t.Fatal("history deletion allowed")
	}
	if err := s.Append(Event{ID: "bad", IdempotencyKey: "bad", Target: "target", Actor: "operator", Kind: "arbitrary", Revision: "r2", Predecessor: ""}); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

func TestExportIsTypedBoundedAndOrdered(t *testing.T) {
	s, _ := openTestStore(t)
	secret := "fixture-private-token-123456"
	secretFile := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secretFile, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	i := intent("e1", "k1", "target", "r1", "")
	if err := s.Append(i); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(follow("e2", "k2", i, KindUncertain)); err != nil {
		t.Fatal(err)
	}
	events, err := s.Events("target")
	if err != nil || len(events) != 2 {
		t.Fatalf("events=%d err=%v", len(events), err)
	}
	if events[0].Sequence >= events[1].Sequence || events[0].RecordedAt.IsZero() || events[1].RecordedAt.IsZero() {
		t.Fatalf("missing sequence/time: %+v", events)
	}
	data, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatal("secret bytes exported")
	}
	var fields []map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"ID": true, "IdempotencyKey": true, "Target": true, "Actor": true, "Kind": true, "Revision": true, "Predecessor": true, "Sequence": true, "RecordedAt": true}
	for key := range fields[0] {
		if !allowed[key] {
			t.Fatalf("unexpected export field %q", key)
		}
	}
	if len(fields[0]) != len(allowed) {
		t.Fatalf("export fields %v", fields[0])
	}
}

func TestSQLitePragmasSurviveConnectionReplacement(t *testing.T) {
	s, _ := openTestStore(t)
	s.db.SetConnMaxLifetime(time.Millisecond)
	time.Sleep(2 * time.Millisecond)
	checks := []struct {
		pragma string
		want   any
	}{
		{"foreign_keys", int64(1)},
		{"journal_mode", "wal"},
		{"synchronous", int64(2)},
		{"busy_timeout", int64(5000)},
	}
	for _, check := range checks {
		var got any
		if err := s.db.QueryRow("PRAGMA " + check.pragma).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != check.want {
			t.Errorf("%s: got %v want %v", check.pragma, got, check.want)
		}
	}
}

func TestOpenStoreRejectsUnsafePaths(t *testing.T) {
	if s, err := OpenStore("relative.db"); err == nil {
		s.Close()
		t.Fatal("relative database path accepted")
	}
	root := t.TempDir()
	loose := filepath.Join(root, "loose.db")
	if err := os.WriteFile(loose, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if s, err := OpenStore(loose); err == nil {
		s.Close()
		t.Fatal("loose database file accepted")
	}
	link := filepath.Join(root, "link.db")
	if err := os.Symlink(loose, link); err != nil {
		t.Fatal(err)
	}
	if s, err := OpenStore(link); err == nil {
		s.Close()
		t.Fatal("database symlink accepted")
	}
	looseDir := filepath.Join(root, "loose-dir")
	if err := os.Mkdir(looseDir, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(looseDir, 0777); err != nil {
		t.Fatal(err)
	}
	if s, err := OpenStore(filepath.Join(looseDir, "db")); err == nil {
		s.Close()
		t.Fatal("loose directory accepted")
	}
}

func TestDatabaseAndWALFilesAreRestricted(t *testing.T) {
	s, path := openTestStore(t)
	if err := s.Append(intent("e1", "k1", "target", "r1", "")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0077 != 0 {
			t.Errorf("%s mode %o", name, info.Mode().Perm())
		}
	}
}
