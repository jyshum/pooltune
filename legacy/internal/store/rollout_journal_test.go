package store

import (
	"encoding/json"
	"testing"
)

func TestRolloutJournalCASAndImmutableHistory(t *testing.T) {
	s, path := openTestStore(t)
	seq, e := s.AppendRollout("db", "event1", 0, json.RawMessage(`{"phase":"baseline"}`))
	if e != nil || seq != 1 {
		t.Fatal(e)
	}
	if _, e = s.AppendRollout("db", "event2", 0, json.RawMessage(`{"phase":"candidate"}`)); e == nil {
		t.Fatal("stale writer accepted")
	}
	if _, e = s.AppendRollout("db", "event1", 0, json.RawMessage(`{"phase":"changed"}`)); e == nil {
		t.Fatal("changed duplicate accepted")
	}
	s.Close()
	restored, e := OpenStore(path)
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	got, raw, e := restored.LatestRollout("db")
	if e != nil || got != 1 || string(raw) != `{"phase":"baseline"}` {
		t.Fatal("journal lost across restart")
	}
	if _, e = restored.db.Exec("DELETE FROM rollout_events"); e == nil {
		t.Fatal("history deletable")
	}
}
