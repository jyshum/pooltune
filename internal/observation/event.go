// Package observation defines credential-free admission outcome events shared by ingress paths.
package observation

import (
	"pooltune/internal/domain"
	"time"
)

type Kind uint8

const (
	Arrived Kind = iota
	Admitted
	Completed
	AdmissionRejected
	Indeterminate
	Unattributed
)

type Event struct {
	ID                                uint64
	TimedOut                          bool
	Kind                              Kind
	Classification                    domain.Classification
	LimitRevision                     domain.RevisionID
	ArrivedAt, AcquiredAt, FinishedAt time.Time
	Failed                            bool
}
