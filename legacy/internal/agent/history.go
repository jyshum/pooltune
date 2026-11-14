package agent

import (
	"errors"
	"os"
	"time"
)

// History reads the durable sanitized projection without waiting for a model call.
func (r *Runner) History() ([]map[string]string, error) {
	raw, err := r.files.Load("agent.json")
	if errors.Is(err, os.ErrNotExist) {
		return []map[string]string{}, nil
	}
	if err != nil {
		return nil, ErrStorage
	}
	var j journal
	if decode(raw, &j) != nil || j.Target != r.config.Target || j.Path != r.config.Path {
		return nil, ErrStorage
	}
	out := []map[string]string{}
	for _, record := range j.Records {
		out = append(out, map[string]string{"id": record.RunID, "actor": "agent", "status": record.Outcome, "at": record.Started.Format(time.RFC3339), "reason": record.Trigger})
	}
	return out, nil
}
