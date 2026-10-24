package store

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

type EventKind string

const (
	KindIntent    EventKind = "intent"
	KindUncertain EventKind = "uncertain"
	KindConfirmed EventKind = "confirmed"
)

var (
	ErrConflict         = errors.New("store: conflicting mutation")
	ErrNoActiveRevision = errors.New("store: no active revision")
	identifier          = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

// Event contains only bounded identifiers. It cannot carry a config or secret payload.
// Callers must still avoid placing credential material in identifiers.
type Event struct {
	ID             string
	IdempotencyKey string
	Target         string
	Actor          string
	Kind           EventKind
	Revision       string
	Predecessor    string // Empty means no previously active revision.
}

type EventRecord struct {
	Event
	Sequence   int64
	RecordedAt time.Time
}

type Revision struct {
	Target      string
	ID          string
	Predecessor string
	Actor       string
	State       string
}

func (e Event) validate() error {
	for name, value := range map[string]string{"id": e.ID, "idempotency key": e.IdempotencyKey, "target": e.Target, "actor": e.Actor, "revision": e.Revision} {
		if !identifier.MatchString(value) {
			return fmt.Errorf("store: invalid %s", name)
		}
	}
	if e.Predecessor != "" && !identifier.MatchString(e.Predecessor) {
		return errors.New("store: invalid predecessor")
	}
	if e.Revision == e.Predecessor {
		return errors.New("store: revision equals predecessor")
	}
	switch e.Kind {
	case KindIntent, KindUncertain, KindConfirmed:
		return nil
	}
	return errors.New("store: unknown event kind")
}

func sameEvent(a, b Event) bool {
	return a == b
}
