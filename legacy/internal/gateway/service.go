package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"pooltune/internal/domain"
	"time"
)

type Profile struct {
	ID, Model                                        string
	Verified                                         bool
	MaxInputTokens, MaxOutputTokens, MaxPayloadBytes int
	InputMicrosPerMillion, OutputMicrosPerMillion    int64
	Timeout                                          time.Duration
}
type Usage struct {
	InputTokens, OutputTokens int
	Known                     bool
}
type ToolCall struct {
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}
type ModelResponse struct {
	Done    bool      `json:"done"`
	Summary string    `json:"summary,omitempty"`
	Tool    *ToolCall `json:"tool,omitempty"`
	Usage   Usage     `json:"usage"`
}
type Exchange struct {
	Tool   string          `json:"tool"`
	Result json.RawMessage `json:"result"`
}
type Workload struct {
	ID, Class                             string
	Arrivals, Completed, Rejected, Failed uint64
	QueueP95MS, TotalP95MS                float64
}
type ApprovedContext struct {
	PolicyRevision, GuardrailVersion, SnapshotID string
	Workloads                                    []Workload
	Policy                                       json.RawMessage
	Bounds                                       *PolicyBounds
	SemanticApproval                             bool
	Descriptions                                 map[string]string
}
type ModelRequest struct {
	Reason    string          `json:"reason,omitempty"`
	Operation string          `json:"operation"`
	Context   ApprovedContext `json:"context"`
	History   []Exchange      `json:"history"`
}
type StepRequest struct {
	Run  string `json:"run"`
	Step int    `json:"step"`
	ModelRequest
}
type Model interface {
	Infer(context.Context, ModelRequest) (ModelResponse, error)
}
type Service struct {
	Store   *Store
	Model   Model
	Profile Profile
}

var toolNames = map[string]bool{"ReadState": true, "ReadWorkloadSummary": true, "ReadHistory": true, "SubmitCandidate": true, "StartCanary": true, "ProposeDiscovery": true}

func (p Profile) valid() bool {
	return p.Verified && idPattern.MatchString(p.ID) && idPattern.MatchString(p.Model) && p.MaxInputTokens > 0 && p.MaxInputTokens <= 100000 && p.MaxOutputTokens > 0 && p.MaxOutputTokens <= 8192 && p.MaxPayloadBytes > 0 && p.MaxPayloadBytes <= 128<<10 && p.InputMicrosPerMillion > 0 && p.InputMicrosPerMillion <= 1000000000 && p.OutputMicrosPerMillion > 0 && p.OutputMicrosPerMillion <= 1000000000 && p.Timeout > 0 && p.Timeout <= 2*time.Minute
}
func (p Profile) cost(input, output int) (int64, error) {
	if input < 0 || input > p.MaxInputTokens || output < 0 || output > p.MaxOutputTokens {
		return 0, ErrInvalid
	}
	return (int64(input)*p.InputMicrosPerMillion + int64(output)*p.OutputMicrosPerMillion + 999999) / 1000000, nil
}
func (s *Service) Start(_ context.Context, token string) (string, error) {
	if s.Store == nil || s.Model == nil || !s.Profile.valid() {
		return "", ErrUnavailable
	}
	return s.Store.NewRun(token)
}
func (s *Service) Step(ctx context.Context, token string, req StepRequest) (ModelResponse, error) {
	if s.Store == nil || s.Model == nil || !s.Profile.valid() {
		return ModelResponse{}, ErrUnavailable
	}
	if req.Operation != "priority" && req.Operation != "discovery" {
		return ModelResponse{}, ErrInvalid
	}
	if len(req.History) > 8 || len(req.Context.Workloads) > 128 || len(req.Context.Descriptions) > 128 || len(req.Context.Descriptions) > 0 && !req.Context.SemanticApproval {
		return ModelResponse{}, ErrInvalid
	}
	for _, x := range req.History {
		if !toolNames[x.Tool] || !json.Valid(x.Result) {
			return ModelResponse{}, ErrInvalid
		}
	}
	if validateContext(req.ModelRequest) != nil {
		return ModelResponse{}, ErrInvalid
	}
	data, e := json.Marshal(req.ModelRequest)
	if e != nil || len(data) > s.Profile.MaxPayloadBytes {
		return ModelResponse{}, ErrInvalid
	}
	hash := sha256.Sum256(data)
	maxCost, _ := s.Profile.cost(s.Profile.MaxInputTokens, s.Profile.MaxOutputTokens)
	reservation, e := s.Store.Reserve(token, req.Run, req.Step, hex.EncodeToString(hash[:]), maxCost)
	if e != nil {
		return ModelResponse{}, e
	}
	callCtx, cancel := context.WithTimeout(ctx, s.Profile.Timeout)
	defer cancel()
	out, e := s.Model.Infer(callCtx, req.ModelRequest)
	if e != nil || !out.Usage.Known {
		_ = s.Store.Uncertain(reservation.ID)
		return ModelResponse{}, ErrUnavailable
	}
	cost, e := s.Profile.cost(out.Usage.InputTokens, out.Usage.OutputTokens)
	if e != nil {
		_ = s.Store.Uncertain(reservation.ID)
		return ModelResponse{}, ErrUnavailable
	}
	if e = s.Store.Settle(reservation.ID, cost); e != nil {
		return ModelResponse{}, ErrUnavailable
	}
	if out.Done == (out.Tool != nil) || len(out.Summary) > 4096 {
		return ModelResponse{}, ErrInvalid
	}
	if out.Tool != nil && (!toolNames[out.Tool.Name] || !json.Valid(out.Tool.Input) || len(out.Tool.Input) > 64<<10) {
		return ModelResponse{}, ErrInvalid
	}
	if out.Tool != nil && req.Operation == "discovery" && out.Tool.Name != "ProposeDiscovery" {
		return ModelResponse{}, ErrInvalid
	}
	if out.Tool != nil && req.Operation == "priority" && out.Tool.Name == "ProposeDiscovery" {
		return ModelResponse{}, ErrInvalid
	}
	return out, nil
}

// PolicyBounds carries only generic limits and opaque workload IDs. Registry
// selectors, principals and routes stay inside the customer control process.
type PolicyBounds struct {
	MaxChangedEntries                      int
	MinReserveFraction, MaxReserveFraction float64
	MinClassWeight, MaxClassWeight         int
	Workloads                              []WorkloadBoundary
}
type WorkloadBoundary struct {
	ID                            string
	DefaultClass                  domain.Class
	AllowedClasses                []domain.Class
	LockedClass                   domain.Class
	IdentityDefault, AllowBlocked bool
	MaxQueueWait                  time.Duration
	MaxQueueLimit, MaxConcurrency int
}
