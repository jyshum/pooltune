package agent

import (
	"encoding/json"
	"pooltune/internal/domain"
)

// Suggestions are explicitly hypotheses. Only the local owner-confirmation
// workflow may turn them into registry defaults or enlarge an allowed class set.
type Suggestion struct {
	WorkloadID     string       `json:"workload_id"`
	Class          domain.Class `json:"class"`
	Reason         string       `json:"reason"`
	Provenance     string       `json:"provenance"`
	ReviewRequired bool         `json:"review_required"`
}

func discovery(raw json.RawMessage, m mapping) ([]Suggestion, error) {
	var req struct {
		Groups []struct {
			WorkloadID string       `json:"workload_id"`
			Class      domain.Class `json:"class"`
			Reason     string       `json:"reason"`
		} `json:"groups"`
	}
	if decode(raw, &req) != nil || len(req.Groups) == 0 || len(req.Groups) > 32 {
		return nil, ErrInvalid
	}
	seen := map[string]bool{}
	out := []Suggestion{}
	for _, g := range req.Groups {
		local, ok := m.local[g.WorkloadID]
		if !ok || seen[local] || len(g.Reason) > 512 || (g.Class != domain.ClassCritical && g.Class != domain.ClassNormal && g.Class != domain.ClassBackground) {
			return nil, ErrInvalid
		}
		seen[local] = true
		out = append(out, Suggestion{WorkloadID: local, Class: g.Class, Reason: g.Reason, Provenance: "model_hypothesis", ReviewRequired: true})
	}
	return out, nil
}
