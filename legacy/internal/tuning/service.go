package tuning

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"pooltune/internal/admin"
	"pooltune/internal/domain"
	"pooltune/internal/durable"
	"pooltune/internal/provider"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Pool interface {
	Observe(context.Context) (provider.LocalPoolStatus, error)
	Apply(context.Context, string, string, int) error
}
type Record struct {
	ID              string    `json:"id"`
	Actor           string    `json:"actor"`
	Status          string    `json:"status"`
	At              time.Time `json:"at"`
	Reason          string    `json:"reason"`
	Before          int       `json:"before"`
	After           int       `json:"after"`
	Key             string    `json:"key,omitempty"`
	Action          string    `json:"action,omitempty"`
	RequestRevision string    `json:"request_revision,omitempty"`
}
type savedState struct {
	Schema      string   `json:"schema"`
	Identity    string   `json:"identity"`
	Fingerprint string   `json:"fingerprint"`
	Revision    string   `json:"revision"`
	Expected    string   `json:"expected"`
	Frozen      bool     `json:"frozen"`
	Pending     *Record  `json:"pending"`
	History     []Record `json:"history"`
}
type View struct {
	Mode        string   `json:"mode"`
	Target      string   `json:"target"`
	Revision    string   `json:"revision"`
	Frozen      bool     `json:"frozen"`
	Ready       bool     `json:"ready"`
	Status      string   `json:"status"`
	Bounds      Bounds   `json:"bounds"`
	Observation *Sample  `json:"observation"`
	Pending     *Record  `json:"pending"`
	Samples     []Sample `json:"samples"`
	Gates       []string `json:"gates"`
}
type Service struct {
	pressureSource string
	pressureAge    time.Duration
	mu             sync.Mutex
	target         string
	bounds         Bounds
	pool           Pool
	journal        provider.Journal
	files          *durable.Files
	state          savedState
	decision       DecisionState
	sample         *Sample
	samples        []Sample
	status         string
	healthy        bool
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func newID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic("secure randomness unavailable")
	}
	return "tune-" + hex.EncodeToString(b[:])
}
func New(target string, b Bounds, p Pool, j provider.Journal, f *durable.Files) (*Service, error) {
	return newService(target, b, p, j, f, "local-host", 0)
}

// NewManaged binds delayed cloud pressure to one explicit resource. Startup is
// frozen and a local profile's persisted authority cannot be reused.
func NewManaged(target string, b Bounds, p Pool, j provider.Journal, f *durable.Files, source string) (*Service, error) {
	age := time.Duration(0)
	if strings.HasPrefix(source, "azure:") && len(source) > 6 {
		age = 120 * time.Second
	}
	if strings.HasPrefix(source, "supabase:") && len(source) > 9 {
		age = 75 * time.Second
	}
	if age == 0 {
		return nil, ErrUnsafe
	}
	return newService(target, b, p, j, f, source, age)
}
func newService(target string, b Bounds, p Pool, j provider.Journal, f *durable.Files, source string, age time.Duration) (*Service, error) {
	if !identifier.MatchString(target) || b.Valid() != nil || p == nil || j == nil || f == nil {
		return nil, ErrUnsafe
	}
	raw, _ := json.Marshal(struct {
		Target string
		Bounds Bounds
		Source string `json:",omitempty"`
	}{Target: target, Bounds: b, Source: func() string {
		if age > 0 {
			return source
		}
		return ""
	}()})
	sum := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(sum[:])
	s := &Service{pressureSource: source, pressureAge: age, target: target, bounds: b, pool: p, journal: j, files: f, status: "Waiting for verified observations"}
	data, e := f.Load("tuner.json")
	if e == nil {
		if json.Unmarshal(data, &s.state) != nil || s.state.Schema != "local-tuner-1" || s.state.Fingerprint != fingerprint || !identifier.MatchString(s.state.Revision) || len(s.state.History) > 1000 {
			return nil, ErrUnsafe
		}
	} else if !os.IsNotExist(e) {
		return nil, ErrUnsafe
	} else {
		s.state = savedState{Schema: "local-tuner-1", Fingerprint: fingerprint}
	}
	next := s.state
	next.Frozen = true
	next.Revision = newID()
	next.History = append(append([]Record(nil), next.History...), Record{ID: next.Revision, Actor: "system", Status: "confirmed", At: time.Now().UTC(), Reason: "Startup freezes elective automation; verify and resume explicitly"})
	if s.persist(next) != nil {
		return nil, ErrUnsafe
	}
	return s, nil
}
func (s *Service) persist(next savedState) error {
	if len(next.History) > 1000 {
		return ErrUnsafe
	}
	raw, e := json.Marshal(next)
	if e != nil || s.files.Save("tuner.json", raw) != nil {
		return ErrUnsafe
	}
	s.state = next
	return nil
}
func (s *Service) fail(reason string) error {
	s.healthy = false
	s.status = reason
	s.decision = DecisionState{}
	return ErrUnsafe
}
func (s *Service) Tick(ctx context.Context, now time.Time, r domain.Resources) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, e := s.pool.Observe(ctx)
	if e != nil {
		return s.fail("Pool observation unavailable; changes held")
	}
	if o.Identity == "" || (s.state.Identity != "" && s.state.Identity != o.Identity) || (s.state.Expected != "" && s.state.Identity == "") {
		return s.fail("Resource identity changed or missing; retained authority refused")
	}
	if o.At.After(now) && o.At.Sub(now) <= 2*time.Second {
		now = o.At
	}
	at := o.At
	if s.pressureAge == 0 && r.At.Before(at) {
		at = r.At
	}
	m := Sample{Transactions: o.Transactions, At: at, Version: o.Version, Target: o.Target, Working: o.Working, Idle: o.Idle, Waiting: o.Waiting, CPU: r.CPU, Memory: r.Memory, Pending: o.Pending}
	if s.pressureAge > 0 {
		m.PressureAt = r.At
		m.PressureMaxAge = s.pressureAge
	}
	s.sample = &m
	if r.Source != s.pressureSource || r.At.After(now) || !m.Valid(now, s.bounds) {
		return s.fail("Fresh resource-bound pressure and verified pool inventory required")
	}
	if s.state.Pending == nil && s.state.Expected != "" && m.Version != s.state.Expected {
		return s.fail("Pool configuration drift; operator reconciliation required")
	}
	if pending := s.state.Pending; pending != nil {
		op, e := s.journal.Load(s.target)
		if e != nil || op == nil || op.Revision != pending.ID {
			return s.fail("Intent outcome unresolved; no automatic replay")
		}
		if !m.Pending && op.State == "complete" && m.Target == pending.After && (m.Working == pending.After || (s.pressureAge > 0 && pending.After < pending.Before && m.Working < pending.After && m.Waiting == 0)) {
			next := s.state
			next.Pending = nil
			next.Expected = m.Version
			next.Revision = newID()
			record := *pending
			record.Status = "confirmed"
			record.At = now.UTC()
			next.History = append(append([]Record(nil), next.History...), record)
			if s.persist(next) != nil {
				return s.fail("Confirmation storage unavailable; changes held")
			}
			s.decision = DecisionState{LastChange: now}
		} else {
			s.healthy = false
			s.status = "Waiting for physical readback; no compounding changes"
			s.decision = DecisionState{}
			return nil
		}
	}
	if s.state.Expected == "" {
		next := s.state
		next.Expected = m.Version
		next.Identity = o.Identity
		if s.persist(next) != nil {
			return s.fail("Initial observation storage unavailable")
		}
	}
	s.healthy = true
	s.samples = append(s.samples, m)
	if len(s.samples) > 120 {
		s.samples = append([]Sample(nil), s.samples[len(s.samples)-120:]...)
	}
	d := Decide(now, s.decision, m, s.bounds, s.state.Frozen)
	if s.pressureAge > 0 && d.Target < m.Target && (m.Waiting > 0 || m.Working != m.Idle) {
		d.Target = m.Target
		d.Reason = "Managed pressure: growth held; lowering limits cannot stop active work"
	}
	s.decision = d.State
	s.status = d.Reason
	if d.Target != m.Target {
		return s.apply(ctx, now, d.Target, d.Reason, "controller", "", "", "")
	}
	return nil
}
func (s *Service) apply(ctx context.Context, now time.Time, target int, reason, actor, key, action, requestRevision string) error {
	if s.sample == nil || s.state.Pending != nil || !s.healthy || !s.sample.Valid(now, s.bounds) || target < s.bounds.Minimum || target > s.bounds.Maximum {
		return ErrUnsafe
	}
	if target == s.sample.Target {
		return ErrUnsafe
	}
	if target > s.sample.Target && (s.sample.CPU >= s.bounds.CPUGrow || s.sample.Memory >= s.bounds.MemoryGrow) {
		return ErrUnsafe
	}
	record := Record{ID: newID(), Actor: actor, Status: "intent", At: now.UTC(), Reason: reason, Before: s.sample.Target, After: target, Key: key, Action: action, RequestRevision: requestRevision}
	next := s.state
	next.Revision = record.ID
	next.Pending = &record
	next.History = append(append([]Record(nil), next.History...), record)
	if actor == "owner" {
		next.Frozen = true
	}
	if s.persist(next) != nil {
		return s.fail("Intent storage unavailable; pool unchanged")
	}
	s.decision = DecisionState{LastChange: now}
	s.healthy = false
	s.status = "Pool operation pending physical readback"
	if s.pool.Apply(ctx, record.ID, s.sample.Version, target) != nil {
		s.status = "Pool operation uncertain; inspect retained intent"
		return ErrUnsafe
	}
	return nil
}
func (s *Service) view(now time.Time) View {
	v := View{Mode: "pool-tuning", Target: s.target, Revision: s.state.Revision, Frozen: s.state.Frozen, Ready: s.healthy && s.sample != nil && s.sample.Valid(now, s.bounds) && s.state.Pending == nil, Status: s.status, Bounds: s.bounds, Samples: append([]Sample(nil), s.samples...), Gates: []string{
		"Experimental local PgBouncer profile. Azure, Google Cloud and hosted Supabase automation are not verified.",
		"Applications connect directly to the existing pooler. No proxy backup routing or pooler high availability is provided.",
		"One local database, role, pooler and OS owner. Exclusive management and no external database consumers are required.",
		"Lower limits drain existing transactions. They cannot immediately stop database overload or undo completed writes.",
		"No demonstrated performance or cost advantage. AI and business-priority scheduling are deferred.",
	}}
	if s.pressureAge > 0 {
		v.Gates[0] = "Experimental managed profile. Pressure is delayed provider telemetry, not an immediate overload stop."
		v.Gates[2] = "Explicit scope inventory, database connection budget and exclusive management are required. Provider-specific mutation restrictions apply."
	}
	if s.sample != nil {
		m := *s.sample
		v.Observation = &m
	}
	if s.state.Pending != nil {
		p := *s.state.Pending
		v.Pending = &p
	}
	return v
}
func (s *Service) View(now time.Time) View { s.mu.Lock(); defer s.mu.Unlock(); return s.view(now) }
func (s *Service) Read(_ context.Context, resource string) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch resource {
	case "state":
		return s.view(time.Now()), nil
	case "revisions":
		return map[string]any{"items": append([]Record(nil), s.state.History...)}, nil
	case "runs":
		return map[string]any{"items": []Record{}}, nil
	}
	return nil, admin.ErrDenied
}
func (s *Service) Execute(ctx context.Context, action string, c admin.Command) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !identifier.MatchString(c.IdempotencyKey) {
		return nil, admin.ErrDenied
	}
	for i := len(s.state.History) - 1; i >= 0; i-- {
		r := s.state.History[i]
		if r.Key == c.IdempotencyKey {
			if r.Action != action || r.RequestRevision != c.Revision {
				return nil, admin.ErrConflict
			}
			return map[string]string{"revision": r.ID, "status": r.Status}, nil
		}
	}
	if c.ExpectedRevision != s.state.Revision {
		return nil, admin.ErrConflict
	}
	now := time.Now()
	next := s.state
	reason := ""
	switch action {
	case "freeze":
		next.Frozen = true
		reason = "Owner froze elective automation"
	case "resume":
		if !s.view(now).Ready || s.sample.CPU >= s.bounds.CPUGrow || s.sample.Memory >= s.bounds.MemoryGrow {
			return nil, ErrUnsafe
		}
		next.Frozen = false
		reason = "Owner resumed bounded automation"
	case "revert":
		for _, r := range s.state.History {
			if r.ID == c.Revision && r.Status == "confirmed" && r.After >= s.bounds.Minimum && r.After <= s.bounds.Maximum {
				if e := s.apply(ctx, now, r.After, "Owner restored a reviewed capacity as a new revision", "owner", c.IdempotencyKey, action, c.Revision); e != nil {
					return nil, e
				}
				return map[string]string{"revision": s.state.Revision, "status": "intent"}, nil
			}
		}
		return nil, admin.ErrDenied
	default:
		return nil, admin.ErrDenied
	}
	next.Revision = newID()
	record := Record{ID: next.Revision, Actor: "owner", Status: "confirmed", At: now.UTC(), Reason: reason, Key: c.IdempotencyKey, Action: action}
	next.History = append(append([]Record(nil), next.History...), record)
	if s.persist(next) != nil {
		return nil, ErrUnsafe
	}
	s.decision = DecisionState{}
	return map[string]string{"revision": next.Revision, "status": "confirmed"}, nil
}
