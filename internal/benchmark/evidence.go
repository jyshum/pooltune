package benchmark

import (
	"encoding/json"
	"os"
	"strconv"
)

// WriteEvidence checkpoints an exploratory run, including invalid trials. It is
// not an atomic backup/recovery primitive and makes no power-loss guarantee.
func WriteEvidence(f *os.File, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err = f.Seek(0, 0); err != nil {
		return err
	}
	if err = f.Truncate(0); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

type PoolInventory struct {
	Connections, Idle, Connecting, CancellationSockets int
	Valid                                              bool
}

// CountPoolInventory describes established PostgreSQL identities in SHOW SERVERS.
// active_cancel entries are cancellation transport, not reusable SQL backends.
// New connections without a backend PID are reported separately, never readiness.
func CountPoolInventory(rows []map[string]string) PoolInventory {
	out := PoolInventory{Valid: true}
	seen := map[int]bool{}
	for _, r := range rows {
		state := r["state"]
		if state == "active_cancel" {
			out.CancellationSockets++
			continue
		}
		switch state {
		case "new":
			out.Connecting++
		case "active", "idle", "used", "tested", "being_canceled":
		default:
			out.Valid = false
			continue
		}
		pid, err := strconv.Atoi(r["remote_pid"])
		if state == "new" && (r["remote_pid"] == "" || r["remote_pid"] == "0") {
			continue
		}
		if err != nil || pid <= 0 || seen[pid] {
			out.Valid = false
			continue
		}
		seen[pid] = true
		out.Connections++
		if state == "idle" {
			out.Idle++
		}
	}
	return out
}
