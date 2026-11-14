package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"pooltune/internal/domain"
	"pooltune/internal/gateway"
	"pooltune/internal/policy"
)

type Preview struct {
	Context gateway.ApprovedContext
	Hash    string
}
type mapping struct {
	preview       Preview
	state         State
	local, opaque map[string]string
}

func decode(raw []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil || d.Decode(&struct{}{}) != io.EOF {
		return ErrInvalid
	}
	return nil
}
func opaque(prefix, value string) string {
	sum := sha256.Sum256([]byte(value))
	return prefix + hex.EncodeToString(sum[:12])
}
func (r *Runner) Preview(ctx context.Context, semantic bool) (Preview, error) {
	state, e := r.authority.View(ctx)
	if e != nil {
		return Preview{}, e
	}
	m, e := prepare(state, semantic)
	return m.preview, e
}
func prepare(s State, semantic bool) (mapping, error) {
	m := mapping{state: s, local: map[string]string{}, opaque: map[string]string{}}
	if len(s.Registry.Workloads) == 0 || len(s.Registry.Workloads) > 128 || len(s.Evidence.Workloads) > 128 || s.Evidence.ID == "" {
		return m, ErrInvalid
	}
	ids := make([]string, 0, len(s.Registry.Workloads))
	for _, w := range s.Registry.Workloads {
		ids = append(ids, w.ID)
	}
	sort.Strings(ids)
	for i, id := range ids {
		if _, ok := m.opaque[id]; ok {
			return m, ErrInvalid
		}
		alias := fmt.Sprintf("w%03d", i+1)
		m.opaque[id] = alias
		m.local[alias] = id
	}
	c := gateway.ApprovedContext{PolicyRevision: opaque("p", string(s.Policy.Revision)), GuardrailVersion: opaque("g", s.Guardrails.Version), SnapshotID: opaque("s", s.Evidence.ID), SemanticApproval: semantic}
	// Copy before translating: neither the installed policy nor caller maps change.
	raw, _ := json.Marshal(s.Policy)
	var p policy.Policy
	if json.Unmarshal(raw, &p) != nil {
		return m, ErrInvalid
	}
	p.Revision = domain.RevisionID(c.PolicyRevision)
	p.ExpectedPredecessor = ""
	p.GuardrailVersion = c.GuardrailVersion
	p.Description = ""
	for i := range p.Rules {
		alias, ok := m.opaque[p.Rules[i].WorkloadID]
		if !ok {
			return m, ErrInvalid
		}
		p.Rules[i].WorkloadID = alias
		p.Rules[i].ID = "rule-" + alias
		p.Rules[i].Description = ""
	}
	for i := range p.Workloads {
		alias, ok := m.opaque[p.Workloads[i].WorkloadID]
		if !ok {
			return m, ErrInvalid
		}
		p.Workloads[i].WorkloadID = alias
	}
	c.Policy, _ = json.Marshal(p)
	seen := map[string]bool{}
	for _, w := range s.Evidence.Workloads {
		alias, ok := m.opaque[w.ID]
		if !ok || seen[alias] {
			return m, ErrInvalid
		}
		seen[alias] = true
		w.ID = alias
		c.Workloads = append(c.Workloads, w)
	}
	// Missing workload coverage is not a valid policy-change context.
	if len(seen) != len(m.local) {
		return m, ErrStale
	}
	if semantic {
		c.Descriptions = map[string]string{}
		for id, text := range s.Descriptions {
			alias, ok := m.opaque[id]
			if !ok || len(text) > 512 {
				return m, ErrInvalid
			}
			c.Descriptions[alias] = text
		}
	}
	c.Bounds = &gateway.PolicyBounds{MaxChangedEntries: s.Guardrails.MaxChangedEntries, MinReserveFraction: s.Guardrails.MinReserveFraction, MaxReserveFraction: s.Guardrails.MaxReserveFraction, MinClassWeight: s.Guardrails.MinClassWeight, MaxClassWeight: s.Guardrails.MaxClassWeight}
	for _, w := range s.Registry.Workloads {
		c.Bounds.Workloads = append(c.Bounds.Workloads, gateway.WorkloadBoundary{ID: m.opaque[w.ID], DefaultClass: w.DefaultClass, AllowedClasses: append([]domain.Class(nil), w.AllowedClasses...), LockedClass: w.LockedClass, IdentityDefault: w.IdentityDefault, AllowBlocked: w.AllowBlocked, MaxQueueWait: w.MaxQueueWait, MaxQueueLimit: w.MaxQueueLimit, MaxConcurrency: w.MaxConcurrency})
	}
	// The approval binds the exact semantic disclosure and authority, but not a
	// moving metrics window. Preview metrics may change before an approved run.
	consent, _ := json.Marshal(struct {
		Policy       json.RawMessage
		Bounds       *gateway.PolicyBounds
		Descriptions map[string]string
		Registry     policy.Registry
	}{c.Policy, c.Bounds, c.Descriptions, s.Registry})
	sum := sha256.Sum256(consent)
	m.preview = Preview{Context: c, Hash: hex.EncodeToString(sum[:])}
	return m, nil
}
