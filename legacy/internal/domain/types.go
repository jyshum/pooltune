package domain

import (
	"encoding/json"
	"time"
)

type PathKind string

const (
	PathPostgres PathKind = "postgres"
	PathREST     PathKind = "rest"
)

type Class string

const (
	ClassCritical   Class = "critical"
	ClassNormal     Class = "normal"
	ClassBackground Class = "background"
)

type RevisionID string

type RequestContext struct {
	Target, Principal, Tenant, Workload, Route, Method string
	Path                                               PathKind
	TrustedTenant                                      bool
	ClientDeadline                                     time.Time
}

type Classification struct {
	ActivationRevision RevisionID
	// Cohort and BufferedBytes are assigned by the trusted ingress adapter.
	Cohort                        string
	BufferedBytes                 int64
	Class                         Class
	Workload, FairnessKey, RuleID string
	Revision                      RevisionID
	QueueDeadline                 time.Time
	ConcurrencyCap                int
	QueueLimit                    int
	Blocked                       bool
}

type LimitSnapshot struct {
	CapacityRevision           string `json:",omitempty"`
	Target                     string
	Path                       PathKind
	Revision                   RevisionID
	Effective, CriticalReserve int
	CohortBudgets              map[string]int
	GuardrailVersion           string
	Epoch                      uint64
}

type ActivationEnvelope struct {
	CandidateFraction                         float64 `json:",omitempty"`
	Revision, ExpectedPredecessor             RevisionID
	Epoch                                     uint64
	SchemaVersion, GuardrailVersion, Checksum string
	PolicyJSON                                json.RawMessage
	Limits                                    []LimitSnapshot
	LeaseExpiresAt                            time.Time
}

type Decision string

const (
	DecisionWait    Decision = "WAIT"
	DecisionAdvance Decision = "ADVANCE"
	DecisionRevert  Decision = "REVERT"
)

// Resources carries freshness and source so an unavailable sample cannot be
// mistaken for zero pressure or a local reading for a managed database metric.
type Resources struct {
	At          time.Time
	CPU, Memory float64
	Source      string
}
