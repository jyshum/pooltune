package store

import (
	"errors"
	"strings"
	"testing"
)

func TestRejectionPersistsWithoutActivatingAndCannotBeConfirmed(t *testing.T) {
	s, path := openTestStore(t)
	i := intent("i", "i", "db", "resume-1", "")
	if err := s.Append(i); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(follow("u", "u", i, KindUncertain)); err != nil {
		t.Fatal(err)
	}
	checksum := strings.Repeat("a", 64)
	if err := s.RecordRejection("db", "resume-1", checksum, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRejection("db", "resume-1", checksum, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRejection("db", "resume-1", checksum, 2); !errors.Is(err, ErrConflict) {
		t.Fatal("changed rejection accepted", err)
	}
	if err := s.Append(follow("c", "c", i, KindConfirmed)); !errors.Is(err, ErrConflict) {
		t.Fatal("rejected intent confirmed", err)
	}
	for _, q := range []string{"DELETE FROM activation_rejections", "UPDATE activation_rejections SET epoch='2'"} {
		if _, err := s.db.Exec(q); err == nil {
			t.Fatal("mutable rejection")
		}
	}
	if err := s.Rebuild(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.ActiveRevision("db"); !errors.Is(err, ErrNoActiveRevision) {
		t.Fatal("rejection advanced active revision", err)
	}
	pending, err := reopened.Pending("db")
	if err != nil || len(pending) != 0 {
		t.Fatal(pending, err)
	}
	if _, err := reopened.Rejection("db", "resume-1"); err != nil {
		t.Fatal(err)
	}
}
