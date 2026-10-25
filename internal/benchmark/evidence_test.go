package benchmark

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEvidenceKeepsFailedTrial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	first := map[string]any{"Complete": false, "Trials": []string{"passed"}}
	if err := WriteEvidence(f, first); err != nil {
		t.Fatal(err)
	}
	failed := map[string]any{"Complete": false, "Trials": []string{"passed", "failed"}, "Failure": "connections=21 at=85000"}
	if err := WriteEvidence(f, failed); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Complete bool
		Trials   []string
		Failure  string
	}
	if json.Unmarshal(b, &out) != nil || out.Complete || len(out.Trials) != 2 || out.Failure != "connections=21 at=85000" {
		t.Fatalf("failure evidence lost: %q", b)
	}
}
func TestPoolInventorySeparatesCancellationConnections(t *testing.T) {
	rows := []map[string]string{{"state": "active", "remote_pid": "100"}, {"state": "idle", "remote_pid": "101"}, {"state": "being_canceled", "remote_pid": "102"}, {"state": "active_cancel", "remote_pid": "0"}, {"state": "new", "remote_pid": "0"}}
	got := CountPoolInventory(rows)
	if !got.Valid || got.Connections != 3 || got.Idle != 1 || got.CancellationSockets != 1 || got.Connecting != 1 {
		t.Fatalf("cancellation socket counted as SQL capacity: %+v", got)
	}
}
