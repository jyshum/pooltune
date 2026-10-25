package store

import (
	"path/filepath"
	"pooltune/internal/provider"
	"strings"
	"testing"
	"time"
)

func TestProviderJournalSurvivesRestartWithoutLosingIntent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	s, e := OpenStore(path)
	if e != nil {
		t.Fatal(e)
	}
	op := provider.Operation{Target: provider.Target{ID: "db"}, Kind: provider.Google, Revision: "c1", Parameter: "max_pool_size", Value: 15, State: "intent", StartedAt: time.Now().UTC()}
	if e = s.Save(op); e != nil {
		t.Fatal(e)
	}
	op.State = "accepted"
	op.ProviderID = "operation-1"
	if e = s.Save(op); e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = OpenStore(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	loaded, e := s.Load("db")
	if e != nil || *loaded != op {
		t.Fatalf("lost operation: %+v %v", loaded, e)
	}
	other := op
	other.Revision = "c2"
	other.State = "intent"
	other.ProviderID = ""
	if e = s.Save(other); e == nil {
		t.Fatal("compounded pending provider write")
	}
	if e = s.Save(op); e != nil {
		t.Fatal("duplicate acknowledgement not idempotent")
	}
	op.State = "complete"
	if e = s.Save(op); e != nil {
		t.Fatal(e)
	}
	if e = s.Save(other); e != nil {
		t.Fatal(e)
	}
}

// Azure returned a valid 3421-byte asynchronous-operation URL in the live test.
// Rejecting it after an accepted PATCH leaves the actual change unrecorded.
func TestProviderJournalRetainsLongAzureOperationWithoutAllowingUnboundedIDs(t *testing.T) {
	s, e := OpenStore(filepath.Join(t.TempDir(), "control.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	op := provider.Operation{Target: provider.Target{ID: "azure"}, Kind: provider.Azure, Revision: "grow", Parameter: "pgbouncer.default_pool_size", Before: 5, Value: 7, State: "intent", StartedAt: time.Now().UTC()}
	if e = s.Save(op); e != nil {
		t.Fatal(e)
	}
	op.State = "accepted"
	op.ProviderID = "https://management.azure.com/subscriptions/sub/providers/Microsoft.DBforPostgreSQL/locations/region/azureAsyncOperation/op?api-version=v&t=" + strings.Repeat("x", 3400)
	if e = s.Save(op); e != nil {
		t.Fatalf("valid Azure receipt lost: %v", e)
	}
	got, e := s.Load("azure")
	if e != nil || got.ProviderID != op.ProviderID || got.Before != 5 {
		t.Fatal("receipt did not survive storage")
	}
	op.State = "complete"
	op.ProviderID += strings.Repeat("x", 9000)
	if e = s.Save(op); e == nil {
		t.Fatal("unbounded provider identifier accepted")
	}
}
