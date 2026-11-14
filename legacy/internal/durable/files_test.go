package durable

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicStateAndExclusiveOwner(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if other, e := Open(dir); e == nil {
		other.Close()
		t.Fatal("duplicate owner")
	}
	if e = s.Save("state.json", []byte(`{"revision":1}`)); e != nil {
		t.Fatal(e)
	}
	data, e := s.Load("state.json")
	if e != nil || string(data) != `{"revision":1}` {
		t.Fatal("durable state")
	}
	if e = s.Save("../outside", []byte("bad")); e == nil {
		t.Fatal("escaped root")
	}
	os.Symlink(filepath.Join(dir, "state.json"), filepath.Join(dir, "link.json"))
	if _, e = s.Load("link.json"); e == nil {
		t.Fatal("followed symlink")
	}
	if _, e = s.Load("missing.json"); !errors.Is(e, os.ErrNotExist) {
		t.Fatal(e)
	}
}
