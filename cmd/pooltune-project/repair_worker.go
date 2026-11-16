package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"pooltune/internal/hostedquery"
	"pooltune/internal/supabaseproject"
)

const repairWorkerPollInterval = 15 * time.Second

var (
	errRepairPolicyChanged     = errors.New("repair permissions changed while checking access")
	errRepairWorkerUnavailable = errors.New("the Pooltune repair worker connection is temporarily unavailable")
)

type repairAccessCheck func(context.Context, hostedquery.ProjectRepairAccessScope) (hostedquery.ProjectRepairAccessReport, error)

type repairWorker struct {
	project  string
	endpoint string
	token    string
	client   *http.Client
	read     repairAccessCheck
}

type repairWorkerPolicy struct {
	Project      string   `json:"project"`
	Epoch        int64    `json:"epoch"`
	Capabilities []string `json:"capabilities"`
	Paused       bool     `json:"paused"`
}

type repairWorkerOperation struct {
	Operation string `json:"operation"`
	Status    string `json:"status"`
}

type repairWorkerReport struct {
	Epoch      int64                   `json:"epoch"`
	Operations []repairWorkerOperation `json:"operations"`
}

type repairWorkerState struct {
	Project  string `json:"project"`
	Epoch    int64  `json:"epoch"`
	Paused   bool   `json:"paused"`
	Reported int    `json:"operations_reported"`
	Retrying bool   `json:"retrying"`
}

func runRepairWorker(ctx context.Context, project supabaseproject.Project, scopeFile, origin string, once bool, output io.Writer) error {
	scope, err := readRepairAccessScope(scopeFile)
	if err != nil {
		return err
	}
	if scope.Project != project.ID() {
		return errors.New("repair scope must match the connected project")
	}
	config, err := repairAccessConfig(project, "pooltune-project-repair-worker")
	if err != nil {
		return err
	}
	worker, err := newRepairWorker(project, origin, os.Getenv("POOLTUNE_REPAIR_WORKER_TOKEN"), func(ctx context.Context, approved hostedquery.ProjectRepairAccessScope) (hostedquery.ProjectRepairAccessReport, error) {
		return queryRepairAccess(ctx, project, config, approved)
	})
	if err != nil {
		return err
	}

	encoder := json.NewEncoder(output)
	var previous repairWorkerState
	for {
		state, err := worker.poll(ctx, scope)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if once || (!errors.Is(err, errRepairWorkerUnavailable) && !errors.Is(err, errRepairPolicyChanged)) {
				return err
			}
			state = repairWorkerState{Project: project.ID(), Retrying: true}
		}
		if state != previous {
			if err := encoder.Encode(state); err != nil {
				return err
			}
			previous = state
		}
		if once {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(repairWorkerPollInterval):
		}
	}
}

func newRepairWorker(project supabaseproject.Project, origin, token string, read repairAccessCheck) (*repairWorker, error) {
	endpoint, err := url.Parse(origin)
	if err != nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.Opaque != "" ||
		(endpoint.Path != "" && endpoint.Path != "/") || endpoint.RawPath != "" || endpoint.RawQuery != "" || endpoint.ForceQuery || strings.Contains(origin, "#") {
		return nil, errors.New("repair-worker requires the Pooltune origin without a path, credentials, query, or fragment")
	}
	host := endpoint.Hostname()
	if endpoint.Scheme != "https" && (endpoint.Scheme != "http" || (!strings.EqualFold(host, "localhost") && !net.ParseIP(host).IsLoopback())) {
		return nil, errors.New("the Pooltune repair worker origin must use HTTPS outside loopback")
	}
	if _, err := hex.DecodeString(token); err != nil || len(token) != 64 || token != strings.ToLower(token) {
		return nil, errors.New("set POOLTUNE_REPAIR_WORKER_TOKEN to this project's worker key")
	}
	endpoint.Path = "/api/projects/" + path.Base(project.ID()) + "/repair-worker"
	client := &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &repairWorker{project: project.ID(), endpoint: endpoint.String(), token: token, client: client, read: read}, nil
}

func (w *repairWorker) poll(ctx context.Context, scope hostedquery.ProjectRepairAccessScope) (repairWorkerState, error) {
	if scope.Project != w.project {
		return repairWorkerState{}, errors.New("repair scope must match the connected project")
	}
	policy, err := w.policy(ctx)
	if err != nil {
		return repairWorkerState{}, err
	}
	state := repairWorkerState{Project: w.project, Epoch: policy.Epoch, Paused: policy.Paused}
	if policy.Paused {
		return state, nil
	}
	approved := scope
	approved.Capabilities = nil
	for _, key := range scope.Capabilities {
		if slices.Contains(policy.Capabilities, key) && !slices.Contains(approved.Capabilities, key) {
			approved.Capabilities = append(approved.Capabilities, key)
		}
	}
	report := repairWorkerReport{Epoch: policy.Epoch, Operations: []repairWorkerOperation{}}
	if len(approved.Capabilities) != 0 {
		if w.read == nil {
			return repairWorkerState{}, errors.New("the owner-side repair access check is missing")
		}
		access, err := w.read(ctx, approved)
		if err != nil {
			return repairWorkerState{}, fmt.Errorf("%w: %s", errRepairWorkerUnavailable, err)
		}
		if access.Project != w.project {
			return repairWorkerState{}, errors.New("the database repair access check returned a different project")
		}
		for _, operation := range access.Operations {
			capability, known := hostedquery.RepairAccessCapability(operation.Operation)
			if known && slices.Contains(approved.Capabilities, capability) {
				report.Operations = append(report.Operations, repairWorkerOperation{Operation: operation.Operation, Status: operation.Status})
			}
		}
	}
	body, err := json.Marshal(report)
	if err != nil {
		return repairWorkerState{}, err
	}
	response, err := w.request(ctx, http.MethodPost, body, http.StatusNoContent)
	if err != nil {
		return repairWorkerState{}, err
	}
	response.Body.Close()
	state.Reported = len(report.Operations)
	return state, nil
}

func (w *repairWorker) policy(ctx context.Context) (repairWorkerPolicy, error) {
	var policy repairWorkerPolicy
	response, err := w.request(ctx, http.MethodGet, nil, http.StatusOK)
	if err != nil {
		return policy, err
	}
	defer response.Body.Close()
	const maxPolicyBytes = 16 << 10
	body, err := io.ReadAll(io.LimitReader(response.Body, maxPolicyBytes+1))
	if err != nil {
		return policy, errRepairWorkerUnavailable
	}
	if len(body) > maxPolicyBytes {
		return policy, errors.New("Pooltune returned an invalid repair permissions response")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil || !errors.Is(decoder.Decode(new(any)), io.EOF) || policy.Epoch <= 0 {
		return repairWorkerPolicy{}, errors.New("Pooltune returned an invalid repair permissions response")
	}
	if policy.Project != w.project {
		return repairWorkerPolicy{}, errors.New("Pooltune returned repair permissions for a different project")
	}
	return policy, nil
}

func (w *repairWorker) request(ctx context.Context, method string, body []byte, want int) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, w.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("could not build the Pooltune repair worker request")
	}
	req.Header.Set("Authorization", "Bearer "+w.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := w.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errRepairWorkerUnavailable
	}
	if response.StatusCode == want {
		return response, nil
	}
	response.Body.Close()
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return nil, errors.New("Pooltune rejected this project's repair worker key")
	case response.StatusCode == http.StatusConflict:
		return nil, errRepairPolicyChanged
	case response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError:
		return nil, errRepairWorkerUnavailable
	default:
		return nil, fmt.Errorf("unexpected Pooltune repair worker response (%d)", response.StatusCode)
	}
}
