package gateway

import (
	"encoding/json"
	"math"
	"pooltune/internal/domain"
	"pooltune/internal/policy"
	"strings"
	"time"
)

func validateContext(input ModelRequest) error {
	switch input.Reason {
	case "", "slo", "starvation", "mix", "queue_expiry", "operation", "manual", "discovery":
	default:
		return ErrInvalid
	}
	c := input.Context
	for _, id := range []string{c.PolicyRevision, c.GuardrailVersion, c.SnapshotID} {
		if !idPattern.MatchString(id) {
			return ErrInvalid
		}
	}
	ids := map[string]bool{}
	for _, w := range c.Workloads {
		if !idPattern.MatchString(w.ID) || ids[w.ID] || (w.Class != "critical" && w.Class != "normal" && w.Class != "background") || math.IsNaN(w.QueueP95MS) || math.IsInf(w.QueueP95MS, 0) || w.QueueP95MS < 0 || math.IsNaN(w.TotalP95MS) || math.IsInf(w.TotalP95MS, 0) || w.TotalP95MS < 0 {
			return ErrInvalid
		}
		ids[w.ID] = true
	}
	for id, label := range c.Descriptions {
		if !c.SemanticApproval || !ids[id] || len(label) > 512 {
			return ErrInvalid
		}
	}
	if len(c.Policy) > 0 {
		var p policy.Policy
		if strictJSON(c.Policy, &p) != nil || (!c.SemanticApproval && p.Description != "") {
			return ErrInvalid
		}
		for _, setting := range p.Workloads {
			if !ids[setting.WorkloadID] {
				return ErrInvalid
			}
		}
		for _, rule := range p.Rules {
			if !idPattern.MatchString(rule.ID) || !ids[rule.WorkloadID] || (!c.SemanticApproval && rule.Description != "") {
				return ErrInvalid
			}
		}
	}
	if b := c.Bounds; b != nil {
		if b.MaxChangedEntries < 0 || b.MaxChangedEntries > 2048 || b.MinReserveFraction < 0 || b.MaxReserveFraction >= 1 || b.MinReserveFraction > b.MaxReserveFraction || math.IsNaN(b.MinReserveFraction) || math.IsNaN(b.MaxReserveFraction) || b.MinClassWeight < 1 || b.MaxClassWeight < b.MinClassWeight || b.MaxClassWeight > 256 || len(b.Workloads) > 128 {
			return ErrInvalid
		}
		seen := map[string]bool{}
		for _, w := range b.Workloads {
			if !ids[w.ID] || seen[w.ID] || !knownClass(w.DefaultClass) || len(w.AllowedClasses) == 0 || len(w.AllowedClasses) > 3 || (w.LockedClass != "" && !knownClass(w.LockedClass)) || w.MaxQueueWait <= 0 || w.MaxQueueWait > time.Hour || w.MaxQueueLimit < 1 || w.MaxQueueLimit > 100000 || w.MaxConcurrency < 1 || w.MaxConcurrency > 1000000 {
				return ErrInvalid
			}
			seen[w.ID] = true
			for _, class := range w.AllowedClasses {
				if !knownClass(class) {
					return ErrInvalid
				}
			}
		}
	}
	for _, step := range input.History {
		var result any
		if json.Unmarshal(step.Result, &result) != nil || !safeResult(result, 0, c.SemanticApproval) {
			return ErrInvalid
		}
	}
	return nil
}

// Structural guardrails supplement local minimization. They do not establish that
// a customer supplied truthful telemetry or that every string is non-sensitive.
func safeResult(v any, depth int, semantic bool) bool {
	if depth > 12 {
		return false
	}
	switch x := v.(type) {
	case map[string]any:
		if len(x) > 128 {
			return false
		}
		for key, item := range x {
			normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
			switch normalized {
			case "sql", "rawsql", "querytext", "rows", "body", "authorization", "apikey", "secret", "password", "credentials", "accesstoken":
				return false
			case "description", "descriptions", "route", "tenantname":
				if !semantic {
					return false
				}
			}
			if !safeResult(item, depth+1, semantic) {
				return false
			}
		}
	case []any:
		if len(x) > 128 {
			return false
		}
		for _, item := range x {
			if !safeResult(item, depth+1, semantic) {
				return false
			}
		}
	case string:
		return len(x) <= 4096
	}
	return true
}

func knownClass(c domain.Class) bool {
	return c == domain.ClassCritical || c == domain.ClassNormal || c == domain.ClassBackground
}
