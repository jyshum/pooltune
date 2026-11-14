// Package policy compiles bounded operator-approved workload mappings into
// read-only lookup tables. It does not authenticate callers or admit traffic.
package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"pooltune/internal/domain"
)

var (
	ErrStale     = errors.New("stale policy")
	ErrBoundary  = errors.New("policy outside approved boundaries")
	ErrAmbiguous = errors.New("ambiguous policy selector")
)

const (
	SchemaVersion       = "1"
	MaxPolicyBytes      = 1 << 20
	maxIdentifierBytes  = 128
	maxDescriptionBytes = 4096
	maxWorkloads        = 1024
	maxRules            = 2048
)

// Selector values are supplied by the trusted registry. RequestContext values
// must be supplied by an ingress adapter after authentication and route parsing.
type Selector struct {
	Target    string          `json:"target"`
	Path      domain.PathKind `json:"path"`
	Principal string          `json:"principal"`
	Method    string          `json:"method,omitempty"`
	Route     string          `json:"route,omitempty"`
}

type Workload struct {
	ID              string         `json:"id"`
	Selector        Selector       `json:"selector"`
	IdentityDefault bool           `json:"identity_default,omitempty"`
	DefaultClass    domain.Class   `json:"default_class"`
	AllowedClasses  []domain.Class `json:"allowed_classes"`
	LockedClass     domain.Class   `json:"locked_class,omitempty"`
	LockedRuleID    string         `json:"locked_rule_id,omitempty"`
	TenantTrust     bool           `json:"tenant_trust,omitempty"`
	MaxQueueWait    time.Duration  `json:"max_queue_wait"`
	MaxQueueLimit   int            `json:"max_queue_limit"`
	MaxConcurrency  int            `json:"max_concurrency"`
	AllowBlocked    bool           `json:"allow_blocked,omitempty"`
}

type Registry struct {
	Workloads []Workload `json:"workloads"`
	Fallback  Fallback   `json:"fallback"`
}

type Fallback struct {
	QueueWait      time.Duration `json:"queue_wait"`
	QueueLimit     int           `json:"queue_limit"`
	ConcurrencyCap int           `json:"concurrency_cap"`
}

type Guardrails struct {
	Version            string            `json:"version"`
	CurrentRevision    domain.RevisionID `json:"current_revision"`
	CurrentPolicy      *CompiledPolicy   `json:"-"`
	MaxChangedEntries  int               `json:"max_changed_entries"`
	MinReserveFraction float64           `json:"min_reserve_fraction"`
	MaxReserveFraction float64           `json:"max_reserve_fraction"`
	MinClassWeight     int               `json:"min_class_weight"`
	MaxClassWeight     int               `json:"max_class_weight"`
}

type Rule struct {
	ID          string       `json:"id"`
	WorkloadID  string       `json:"workload_id"`
	Class       domain.Class `json:"class"`
	Description string       `json:"description,omitempty"`
}

type WorkloadSettings struct {
	WorkloadID     string        `json:"workload_id"`
	QueueWait      time.Duration `json:"queue_wait"`
	QueueLimit     int           `json:"queue_limit"`
	ConcurrencyCap int           `json:"concurrency_cap"`
	Blocked        bool          `json:"blocked,omitempty"`
}

type ClassWeights struct {
	Critical   int `json:"critical"`
	Normal     int `json:"normal"`
	Background int `json:"background"`
}

type Policy struct {
	SchemaVersion       string             `json:"schema_version"`
	Revision            domain.RevisionID  `json:"revision"`
	ExpectedPredecessor domain.RevisionID  `json:"expected_predecessor"`
	GuardrailVersion    string             `json:"guardrail_version"`
	Description         string             `json:"description,omitempty"`
	ReserveFraction     float64            `json:"reserve_fraction"`
	ClassWeights        ClassWeights       `json:"class_weights"`
	Rules               []Rule             `json:"rules"`
	Workloads           []WorkloadSettings `json:"workloads"`
}

type compiledWorkload struct {
	id             string
	class          domain.Class
	ruleID         string
	queueWait      time.Duration
	queueLimit     int
	concurrencyCap int
	blocked        bool
	tenantTrust    bool
}

// CompiledPolicy contains no exported mutable fields. Compile copies each
// stored value; callers must never mutate it after publication.
type CompiledPolicy struct {
	revision        domain.RevisionID
	reserveFraction float64
	classWeights    ClassWeights
	fallback        Fallback
	exact           map[Selector]compiledWorkload
	defaults        map[Selector]compiledWorkload
	byID            map[string]compiledWorkload
}

func (c *CompiledPolicy) Revision() domain.RevisionID {
	if c == nil {
		return ""
	}
	return c.revision
}
func (c *CompiledPolicy) ReserveFraction() float64 {
	if c == nil {
		return 0
	}
	return c.reserveFraction
}
func (c *CompiledPolicy) ClassWeights() ClassWeights {
	if c == nil {
		return ClassWeights{}
	}
	return c.classWeights
}

func Compile(p Policy, bounds Guardrails, registry Registry) (*CompiledPolicy, error) {
	if p.GuardrailVersion != bounds.Version || p.ExpectedPredecessor != bounds.CurrentRevision || bounds.Version == "" || p.Revision == p.ExpectedPredecessor {
		return nil, ErrStale
	}
	if bounds.CurrentRevision != "" && (bounds.CurrentPolicy == nil || bounds.CurrentPolicy.Revision() != bounds.CurrentRevision) {
		return nil, ErrStale
	}
	if p.SchemaVersion != SchemaVersion || !identifier(string(p.Revision)) || (p.ExpectedPredecessor != "" && !identifier(string(p.ExpectedPredecessor))) ||
		!identifier(p.GuardrailVersion) || len(p.Description) > maxDescriptionBytes ||
		len(p.Rules) > maxRules || len(p.Workloads) > maxWorkloads || len(registry.Workloads) > maxWorkloads ||
		!finite(bounds.MinReserveFraction) || !finite(bounds.MaxReserveFraction) || bounds.MinReserveFraction < 0 || bounds.MaxReserveFraction >= 1 || bounds.MinReserveFraction > bounds.MaxReserveFraction ||
		!finite(p.ReserveFraction) || p.ReserveFraction < bounds.MinReserveFraction || p.ReserveFraction > bounds.MaxReserveFraction ||
		bounds.MaxChangedEntries < 0 || bounds.MinClassWeight <= 0 || bounds.MaxClassWeight < bounds.MinClassWeight || bounds.MaxClassWeight > 256 ||
		!validClassWeights(p.ClassWeights, bounds) || !validFallback(registry.Fallback) {
		return nil, ErrBoundary
	}
	for _, r := range p.Rules {
		if !identifier(r.ID) || !identifier(r.WorkloadID) || len(r.Description) > maxDescriptionBytes {
			return nil, ErrBoundary
		}
	}
	for _, s := range p.Workloads {
		if !identifier(s.WorkloadID) {
			return nil, ErrBoundary
		}
	}
	encoded, err := json.Marshal(p)
	if err != nil || len(encoded) > MaxPolicyBytes {
		return nil, ErrBoundary
	}

	byID := make(map[string]Workload, len(registry.Workloads))
	exactIDs := make(map[Selector]string, len(registry.Workloads))
	defaultIDs := make(map[Selector]string, len(registry.Workloads))
	lockedIDs := make(map[string]struct{})
	for _, w := range registry.Workloads {
		if !validWorkload(w) {
			return nil, fmt.Errorf("%w: invalid registry workload", ErrBoundary)
		}
		if _, exists := byID[w.ID]; exists {
			return nil, ErrAmbiguous
		}
		byID[w.ID] = w
		if w.LockedRuleID != "" {
			if _, exists := lockedIDs[w.LockedRuleID]; exists {
				return nil, ErrAmbiguous
			}
			lockedIDs[w.LockedRuleID] = struct{}{}
		}
		key := w.Selector
		if w.IdentityDefault {
			if _, exists := defaultIDs[key]; exists {
				return nil, ErrAmbiguous
			}
			defaultIDs[key] = w.ID
		} else {
			if _, exists := exactIDs[key]; exists {
				return nil, ErrAmbiguous
			}
			exactIDs[key] = w.ID
		}
	}
	settings := make(map[string]WorkloadSettings, len(p.Workloads))
	for _, s := range p.Workloads {
		w, ok := byID[s.WorkloadID]
		if !ok || !validSettings(s, w) {
			return nil, ErrBoundary
		}
		if _, exists := settings[s.WorkloadID]; exists {
			return nil, ErrAmbiguous
		}
		settings[s.WorkloadID] = s
	}
	if len(settings) != len(byID) {
		return nil, ErrBoundary
	}
	rules := make(map[string]Rule, len(p.Rules))
	ruleIDs := make(map[string]struct{}, len(p.Rules))
	for _, r := range p.Rules {
		w, ok := byID[r.WorkloadID]
		if !ok || w.IdentityDefault || !identifier(r.ID) || len(r.Description) > maxDescriptionBytes || !allowed(r.Class, w.AllowedClasses) ||
			(w.LockedClass != "" && r.Class != w.LockedClass) {
			return nil, ErrBoundary
		}
		if _, exists := rules[r.WorkloadID]; exists {
			return nil, ErrAmbiguous
		}
		if _, exists := ruleIDs[r.ID]; exists {
			return nil, ErrAmbiguous
		}
		if _, exists := lockedIDs[r.ID]; exists {
			return nil, ErrAmbiguous
		}
		rules[r.WorkloadID] = r
		ruleIDs[r.ID] = struct{}{}
	}
	c := &CompiledPolicy{revision: p.Revision, reserveFraction: p.ReserveFraction, classWeights: p.ClassWeights, fallback: registry.Fallback, exact: make(map[Selector]compiledWorkload, len(exactIDs)), defaults: make(map[Selector]compiledWorkload, len(defaultIDs)), byID: make(map[string]compiledWorkload, len(byID))}
	for id, w := range byID {
		s := settings[id]
		v := compiledWorkload{id: id, class: w.DefaultClass, queueWait: s.QueueWait, queueLimit: s.QueueLimit, concurrencyCap: s.ConcurrencyCap, blocked: s.Blocked, tenantTrust: w.TenantTrust}
		if w.LockedClass != "" {
			v.class = w.LockedClass
			v.ruleID = w.LockedRuleID
		} else if r, ok := rules[id]; ok {
			v.class = r.Class
			v.ruleID = r.ID
		}
		if w.IdentityDefault {
			c.defaults[w.Selector] = v
		} else {
			c.exact[w.Selector] = v
		}
		c.byID[id] = v
	}
	if bounds.CurrentRevision != "" && changedEntries(bounds.CurrentPolicy, c) > bounds.MaxChangedEntries {
		return nil, ErrBoundary
	}
	return c, nil
}

func changedEntries(old, next *CompiledPolicy) int {
	n := 0
	if old.reserveFraction != next.reserveFraction {
		n++
	}
	if old.classWeights.Critical != next.classWeights.Critical {
		n++
	}
	if old.classWeights.Normal != next.classWeights.Normal {
		n++
	}
	if old.classWeights.Background != next.classWeights.Background {
		n++
	}
	for id, v := range next.byID {
		prior, ok := old.byID[id]
		if !ok || prior.class != v.class || prior.queueWait != v.queueWait || prior.queueLimit != v.queueLimit || prior.concurrencyCap != v.concurrencyCap || prior.blocked != v.blocked {
			n++
		}
	}
	for id := range old.byID {
		if _, ok := next.byID[id]; !ok {
			n++
		}
	}
	return n
}

func validWorkload(w Workload) bool {
	s := w.Selector
	if len(w.AllowedClasses) > 3 {
		return false
	}
	seenClasses := map[domain.Class]bool{}
	for _, class := range w.AllowedClasses {
		if seenClasses[class] || (class != domain.ClassCritical && class != domain.ClassNormal && class != domain.ClassBackground) {
			return false
		}
		seenClasses[class] = true
	}
	if w.ID == "overflow" || !identifier(w.ID) || !identifier(s.Target) || !identifier(s.Principal) || (s.Path != domain.PathREST && s.Path != domain.PathPostgres) || !allowed(w.DefaultClass, w.AllowedClasses) ||
		len(w.AllowedClasses) == 0 || w.MaxQueueWait <= 0 || w.MaxQueueWait > time.Hour || w.MaxQueueLimit <= 0 || w.MaxQueueLimit > 100_000 || w.MaxConcurrency <= 0 || w.MaxConcurrency > 1_000_000 {
		return false
	}
	if (w.LockedClass == "") != (w.LockedRuleID == "") || (w.LockedClass != "" && (!allowed(w.LockedClass, w.AllowedClasses) || !identifier(w.LockedRuleID))) {
		return false
	}
	if w.IdentityDefault {
		return s.Method == "" && s.Route == ""
	}
	if s.Path == domain.PathPostgres {
		return s.Method == "" && s.Route == ""
	}
	return s.Method != "" && s.Route != "" && len(s.Method) <= maxIdentifierBytes && len(s.Route) <= maxIdentifierBytes && !strings.ContainsAny(s.Method, "\r\n") && !strings.ContainsAny(s.Route, "\r\n")
}

func validSettings(s WorkloadSettings, w Workload) bool {
	if s.QueueWait <= 0 || s.QueueWait > w.MaxQueueWait || s.QueueLimit < 0 || s.QueueLimit > w.MaxQueueLimit || s.ConcurrencyCap < 0 || s.ConcurrencyCap > w.MaxConcurrency {
		return false
	}
	if s.Blocked {
		return w.AllowBlocked && s.ConcurrencyCap == 0 && s.QueueLimit == 0
	}
	return s.ConcurrencyCap > 0 && s.QueueLimit > 0
}

func validFallback(f Fallback) bool {
	return f.QueueWait > 0 && f.QueueWait <= time.Hour && f.QueueLimit > 0 && f.QueueLimit <= 100_000 && f.ConcurrencyCap > 0 && f.ConcurrencyCap <= 1_000_000
}
func validClassWeights(w ClassWeights, g Guardrails) bool {
	for _, v := range []int{w.Critical, w.Normal, w.Background} {
		if v < g.MinClassWeight || v > g.MaxClassWeight {
			return false
		}
	}
	return w.Critical+w.Normal+w.Background <= 256
}

func identifier(s string) bool {
	return s != "" && len(s) <= maxIdentifierBytes && !strings.ContainsAny(s, "\x00\r\n")
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func allowed(c domain.Class, choices []domain.Class) bool {
	if c != domain.ClassCritical && c != domain.ClassNormal && c != domain.ClassBackground {
		return false
	}
	for _, v := range choices {
		if v == c {
			return true
		}
	}
	return false
}
