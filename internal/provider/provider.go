// Package provider performs slow, serialized management work. It never runs in admission.
package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sync"
	"time"
)

type Kind string

const (
	Azure    Kind = "azure"
	Google   Kind = "google"
	Supabase Kind = "supabase"
)

var (
	ErrInvalid     = errors.New("invalid provider profile")
	ErrUnsupported = errors.New("unsupported provider operation")
	ErrPending     = errors.New("provider operation pending reconciliation")
	ErrUncertain   = errors.New("provider outcome uncertain")
	ErrStorage     = errors.New("provider journal unavailable")
	ErrStale       = errors.New("provider configuration changed")
	ErrAPI         = errors.New("provider request failed")
)

type Target struct{ ID string }
type Scope struct{ ID, Database, Role, Replica string }
type Evidence struct {
	ScopeID, Version string
	ObservedAt       time.Time
	Working, Idle    int
	Complete         bool
}
type Observation struct {
	Target         Target
	ObservedAt     time.Time
	Version        string
	Parameters     map[string]int
	Scopes         []Scope
	Working, Idle  int
	Ready          bool
	Status         string
	Gaps           []string
	WorkingByScope map[string]int
}
type TargetChange struct {
	Target                               Target
	Revision, ExpectedVersion, Parameter string
	Value                                int
}
type Operation struct {
	Before                               int `json:",omitempty"`
	Target                               Target
	Kind                                 Kind
	Revision, ExpectedVersion, Parameter string
	Value                                int
	ProviderID, State                    string
	StartedAt                            time.Time
}

// Journal must durably persist before returning. New loads any incomplete write.
// It stores identifiers and pool values only, never credentials or response bodies.
type Journal interface {
	Load(target string) (*Operation, error)
	Save(Operation) error
}
type Config struct {
	Kind                                                               Kind
	Target                                                             Target
	Subscription, ResourceGroup, Project, Instance, Endpoint           string
	LocalTest, ChangesEnabled, SingleWriterVerified, InventoryVerified bool
	PhysicalAllocation                                                 int
	Scopes                                                             []Scope
	Timeout, Freshness                                                 time.Duration
	Token                                                              func(context.Context) (string, error)
	Readiness                                                          func(context.Context, Target, string) ([]Evidence, error)
	Journal                                                            Journal
}
type Provider interface {
	Observe(context.Context, Target) (Observation, error)
	Apply(context.Context, TargetChange) (Operation, error)
	Reconcile(context.Context, Operation) (Observation, error)
}
type Adapter struct {
	cfg       Config
	client    *http.Client
	mu        sync.Mutex
	operation *Operation
	pressure  pressureHistory
	discovery bool
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func New(cfg Config) (*Adapter, error) {
	return newAdapter(cfg, false)
}

// NewDiscovery can read provider configuration before a database/role/replica
// inventory exists. It never claims working capacity and cannot perform writes.
func NewDiscovery(cfg Config) (*Adapter, error) {
	if cfg.ChangesEnabled || cfg.InventoryVerified || cfg.SingleWriterVerified || cfg.Readiness != nil || cfg.Journal != nil || len(cfg.Scopes) != 0 || cfg.PhysicalAllocation != 0 {
		return nil, ErrInvalid
	}
	return newAdapter(cfg, true)
}

func newAdapter(cfg Config, discovery bool) (*Adapter, error) {
	if !identifier.MatchString(cfg.Target.ID) || cfg.Token == nil || cfg.Timeout <= 0 || cfg.Timeout > 2*time.Minute || cfg.Freshness <= 0 || cfg.Freshness > 5*time.Minute || (!discovery && (cfg.PhysicalAllocation < 1 || cfg.PhysicalAllocation > 1000000 || len(cfg.Scopes) == 0)) || len(cfg.Scopes) > 128 {
		return nil, ErrInvalid
	}
	seen := map[string]bool{}
	for _, scope := range cfg.Scopes {
		if seen[scope.ID] || !identifier.MatchString(scope.ID) || !identifier.MatchString(scope.Database) || !identifier.MatchString(scope.Role) || !identifier.MatchString(scope.Replica) {
			return nil, ErrInvalid
		}
		seen[scope.ID] = true
	}
	cfg.Scopes = append([]Scope(nil), cfg.Scopes...)
	expected := ""
	switch cfg.Kind {
	case Azure:
		expected = "https://management.azure.com"
		if !identifier.MatchString(cfg.Subscription) || !identifier.MatchString(cfg.ResourceGroup) || !identifier.MatchString(cfg.Instance) {
			return nil, ErrInvalid
		}
	case Google:
		expected = "https://sqladmin.googleapis.com"
		if !identifier.MatchString(cfg.Project) || !identifier.MatchString(cfg.Instance) {
			return nil, ErrInvalid
		}
	case Supabase:
		expected = "https://api.supabase.com"
		if !identifier.MatchString(cfg.Project) {
			return nil, ErrInvalid
		}
	default:
		return nil, ErrInvalid
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = expected
	}
	u, e := url.Parse(cfg.Endpoint)
	if e != nil || u.Host == "" || u.Path != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, ErrInvalid
	}
	if cfg.Endpoint != expected {
		ip := net.ParseIP(u.Hostname())
		if !cfg.LocalTest || u.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return nil, ErrInvalid
		}
	}
	if cfg.ChangesEnabled && (!cfg.SingleWriterVerified || !cfg.InventoryVerified || cfg.Journal == nil) {
		return nil, ErrInvalid
	}
	// No redirects, environment proxy, automatic retry or unbounded response bodies.
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: (&net.Dialer{Timeout: cfg.Timeout}).DialContext, TLSHandshakeTimeout: cfg.Timeout, ResponseHeaderTimeout: cfg.Timeout, MaxResponseHeaderBytes: 16384}
	a := &Adapter{cfg: cfg, discovery: discovery, client: &http.Client{Transport: transport, Timeout: cfg.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if cfg.Journal != nil {
		op, e := cfg.Journal.Load(cfg.Target.ID)
		if e != nil {
			return nil, ErrStorage
		}
		if op != nil {
			if op.Target != cfg.Target || op.Kind != cfg.Kind || !identifier.MatchString(op.Revision) || !a.parameterAllowed(op.Parameter) || op.Value < 0 || op.Value > cfg.PhysicalAllocation || op.StartedAt.IsZero() {
				return nil, ErrStorage
			}
			switch op.State {
			case "intent", "uncertain", "accepted", "failed", "complete":
			default:
				return nil, ErrStorage
			}
			copy := *op
			a.operation = &copy
		}
	}
	return a, nil
}
func (a *Adapter) request(ctx context.Context, method, endpoint string, body any) ([]byte, http.Header, error) {
	u, e := url.Parse(endpoint)
	base, _ := url.Parse(a.cfg.Endpoint)
	if e != nil || u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil || u.Fragment != "" {
		return nil, nil, ErrAPI
	}
	var data []byte
	if body != nil {
		data, e = json.Marshal(body)
		if e != nil {
			return nil, nil, ErrInvalid
		}
	}
	token, e := a.cfg.Token(ctx)
	if e != nil || token == "" {
		return nil, nil, ErrAPI
	}
	req, e := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(data))
	if e != nil {
		return nil, nil, ErrAPI
	}
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "pooltune/0.1")
	resp, e := a.client.Do(req)
	if e != nil {
		return nil, nil, ErrAPI
	}
	defer resp.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if e != nil || len(raw) > 1<<20 || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, ErrAPI
	}
	return raw, resp.Header, nil
}
func (a *Adapter) Observe(ctx context.Context, t Target) (Observation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t != a.cfg.Target {
		return Observation{}, ErrInvalid
	}
	obs, _, e := a.observe(ctx)
	return obs, e
}
func (a *Adapter) observe(ctx context.Context) (Observation, map[string]any, error) {
	var params map[string]int
	var state map[string]any
	var e error
	switch a.cfg.Kind {
	case Azure:
		params, state, e = a.azureObserve(ctx)
	case Google:
		params, state, e = a.googleObserve(ctx)
	case Supabase:
		params, state, e = a.supabaseObserve(ctx)
	}
	if e != nil {
		return Observation{}, nil, e
	}
	raw, _ := json.Marshal(state)
	hash := sha256.Sum256(raw)
	obs := Observation{Target: a.cfg.Target, ObservedAt: time.Now(), Version: hex.EncodeToString(hash[:]), Parameters: params, Scopes: append([]Scope(nil), a.cfg.Scopes...), Status: "configured"}
	if a.discovery && a.cfg.Kind == Google {
		for _, key := range []string{"max_pool_size", "min_pool_size"} {
			if _, ok := params[key]; !ok {
				obs.Gaps = append(obs.Gaps, key+" is not explicitly configured")
			}
		}
		if len(obs.Gaps) > 0 {
			obs.Status = "discovery_incomplete"
		}
	}
	if a.cfg.Readiness != nil && a.cfg.InventoryVerified {
		samples, err := a.cfg.Readiness(ctx, a.cfg.Target, obs.Version)
		if err == nil && len(samples) == len(a.cfg.Scopes) {
			found := map[string]bool{}
			obs.WorkingByScope = make(map[string]int)
			valid := true
			now := time.Now()
			for _, sample := range samples {
				known := false
				for _, scope := range a.cfg.Scopes {
					if sample.ScopeID == scope.ID {
						known = true
						break
					}
				}
				if !known || found[sample.ScopeID] || sample.Version != obs.Version || !sample.Complete || sample.ObservedAt.After(now) || now.Sub(sample.ObservedAt) > a.cfg.Freshness || sample.Working < 0 || sample.Working > a.cfg.PhysicalAllocation-obs.Working || sample.Idle < 0 || sample.Idle > sample.Working {
					valid = false
					break
				}
				found[sample.ScopeID] = true
				obs.WorkingByScope[sample.ScopeID] = sample.Working
				obs.Working += sample.Working
				obs.Idle += sample.Idle
			}
			if valid {
				obs.Ready = true
				obs.Status = "capacity_verified"
			} else {
				obs.Working = 0
				obs.Idle = 0
				obs.WorkingByScope = nil
			}
		}
	}
	return obs, state, nil
}
func (a *Adapter) parameterAllowed(p string) bool {
	switch a.cfg.Kind {
	case Azure:
		return p == "pgbouncer.default_pool_size" || p == "pgbouncer.min_pool_size"
	case Google:
		return p == "max_pool_size" || p == "min_pool_size"
	case Supabase:
		return p == "default_pool_size"
	}
	return false
}
func (a *Adapter) Apply(ctx context.Context, c TargetChange) (Operation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.operation != nil {
		if a.operation.Revision == c.Revision && a.operation.Target == c.Target && a.operation.Parameter == c.Parameter && a.operation.Value == c.Value && a.operation.ExpectedVersion == c.ExpectedVersion {
			return *a.operation, ErrPending
		}
		if a.operation.State != "complete" {
			return *a.operation, ErrPending
		}
	}
	if a.cfg.Kind == Supabase {
		return Operation{}, ErrUnsupported
	}
	if !a.cfg.ChangesEnabled || !a.cfg.SingleWriterVerified || !a.cfg.InventoryVerified || a.cfg.Journal == nil {
		return Operation{}, ErrUnsupported
	}
	if c.Target != a.cfg.Target || !identifier.MatchString(c.Revision) || !a.parameterAllowed(c.Parameter) || c.ExpectedVersion == "" || c.Value < 0 || c.Value > a.cfg.PhysicalAllocation/len(a.cfg.Scopes) {
		return Operation{}, ErrInvalid
	}
	before, state, e := a.observe(ctx)
	if e != nil {
		return Operation{}, e
	}
	if before.Version != c.ExpectedVersion {
		return Operation{}, ErrStale
	}
	maxName, minName := "max_pool_size", "min_pool_size"
	if a.cfg.Kind == Azure {
		maxName, minName = "pgbouncer.default_pool_size", "pgbouncer.min_pool_size"
	}
	maxValue, minValue := before.Parameters[maxName], before.Parameters[minName]
	if c.Parameter == maxName {
		maxValue = c.Value
	} else {
		minValue = c.Value
	}
	if maxValue < 1 || minValue > maxValue || maxValue > a.cfg.PhysicalAllocation/len(a.cfg.Scopes) {
		return Operation{}, ErrInvalid
	}
	op := Operation{Target: c.Target, Kind: a.cfg.Kind, Revision: c.Revision, ExpectedVersion: c.ExpectedVersion, Parameter: c.Parameter, Value: c.Value, State: "intent", StartedAt: time.Now().UTC()}
	a.operation = &op
	if a.cfg.Journal.Save(op) != nil {
		return op, ErrStorage
	}
	switch a.cfg.Kind {
	case Azure:
		op.ProviderID, e = a.azureApply(ctx, c)
	case Google:
		op.ProviderID, e = a.googleApply(ctx, c, state)
	}
	if e != nil || op.ProviderID == "" {
		op.State = "uncertain"
		_ = a.cfg.Journal.Save(op)
		return op, ErrUncertain
	}
	op.State = "accepted"
	if a.cfg.Journal.Save(op) != nil {
		op.State = "uncertain"
		return op, ErrStorage
	}
	return op, nil
}
func (a *Adapter) Reconcile(ctx context.Context, requested Operation) (Observation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.operation == nil || requested.Target != a.cfg.Target || requested.Revision != a.operation.Revision {
		return Observation{}, ErrInvalid
	}
	op := a.operation
	if op.State == "complete" {
		obs, _, e := a.observe(ctx)
		return obs, e
	}
	if op.ProviderID == "" || op.State == "failed" {
		return Observation{}, ErrUncertain
	}
	endpoint := op.ProviderID
	if a.cfg.Kind == Google {
		if !identifier.MatchString(op.ProviderID) {
			return Observation{}, ErrInvalid
		}
		endpoint = a.googleBase() + "/operations/" + url.PathEscape(op.ProviderID)
	}
	if a.cfg.Kind == Azure && !a.validAzureOperation(endpoint) {
		return Observation{}, ErrInvalid
	}
	raw, _, err := a.request(ctx, "GET", endpoint, nil)
	if err != nil {
		return Observation{}, err
	}
	var result struct {
		Status string          `json:"status"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return Observation{}, ErrAPI
	}
	if len(result.Error) > 0 && string(result.Error) != "null" || result.Status == "Failed" || result.Status == "Canceled" {
		op.State = "failed"
		if a.cfg.Journal.Save(*op) != nil {
			return Observation{}, ErrStorage
		}
		return Observation{}, ErrUncertain
	}
	terminal := result.Status == "Succeeded" && a.cfg.Kind == Azure || result.Status == "DONE" && a.cfg.Kind == Google
	obs, _, err := a.observe(ctx)
	if err != nil {
		return Observation{}, err
	}
	if !terminal {
		obs.Ready = false
		obs.Working = 0
		obs.Idle = 0
		obs.Status = "operation_pending"
		return obs, nil
	}
	desired := op.Value * len(a.cfg.Scopes)
	scopesReady := true
	for _, scope := range a.cfg.Scopes {
		count, ok := obs.WorkingByScope[scope.ID]
		if !ok || count < op.Value {
			scopesReady = false
		}
		if (op.Parameter == "max_pool_size" || op.Parameter == "pgbouncer.default_pool_size") && count != op.Value {
			scopesReady = false
		}
	}
	if obs.Parameters[op.Parameter] != op.Value || !obs.Ready || !scopesReady || obs.Working < desired {
		obs.Ready = false
		obs.Status = "awaiting_capacity"
		return obs, nil
	}
	completed := *op
	completed.State = "complete"
	if a.cfg.Journal.Save(completed) != nil {
		return Observation{}, ErrStorage
	}
	*op = completed
	return obs, nil
}
func number(v any) (int, error) {
	switch x := v.(type) {
	case string:
		var n int
		if _, e := fmt.Sscanf(x, "%d", &n); e == nil && fmt.Sprint(n) == x && n >= 0 && n <= 1000000 {
			return n, nil
		}
	case float64:
		if x >= 0 && x <= 1000000 && x == float64(int(x)) {
			return int(x), nil
		}
	}
	return 0, ErrAPI
}
func object(v any) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, ErrAPI
	}
	return m, nil
}
func decode(raw []byte) (map[string]any, error) {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return nil, ErrAPI
	}
	return m, nil
}
