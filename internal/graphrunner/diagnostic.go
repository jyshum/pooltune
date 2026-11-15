package graphrunner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"pooltune/internal/hostedquery"
	"pooltune/internal/labdb"
	"slices"
	"time"
)

type DiagnosticIdentity struct {
	RunID    string  `json:"run_id"`
	TableOID uint32  `json:"table_oid"`
	Column   int16   `json:"column_number"`
	Columns  []int16 `json:"column_numbers,omitempty"`
}
type DiagnosticObservation struct {
	DiagnosticIdentity
	Tool   string          `json:"tool"`
	At     time.Time       `json:"observed_at"`
	Status string          `json:"status"`
	Data   json.RawMessage `json:"data,omitempty"`
}
type DiagnosticInput struct {
	DiagnosticIdentity
	Mode       string                      `json:"mode,omitempty"`
	AdmitModel func(context.Context) error `json:"-"`
	Decision   hostedquery.Decision        `json:"decision"`
	Initial    []DiagnosticObservation     `json:"initial"`
}
type DiagnosticResult struct {
	Proposal      hostedquery.IndexProposal `json:"proposal"`
	ModelAttempts int                       `json:"model_attempts"`
	ModelCalls    int                       `json:"model_calls"`
	Reads         int                       `json:"reads"`
	ModelMS       float64                   `json:"model_ms"`
	ToolMS        float64                   `json:"tool_ms"`
	TotalMS       float64                   `json:"total_ms"`
	StopReason    string                    `json:"stop_reason"`
	History       []json.RawMessage         `json:"history"`
}
type DiagnosticReader func(context.Context, string) (DiagnosticObservation, error)
type DiagnosticInvestigator func(context.Context, DiagnosticInput, DiagnosticReader) (DiagnosticResult, error)

var diagnosticTools = map[string]bool{"plan": true, "indexes": true, "analyze": true, "statistics": true, "vacuum": true, "memory": true, "cron": true}

// DiagnosticProcess persists graph state. The supplied readers own project grants,
// exact target binding and query bounds. The model can select only a tool name.
func DiagnosticProcess(stateDSN string) (DiagnosticInvestigator, error) {
	if stateDSN == "" {
		return nil, errors.New("diagnostic process requires durable state")
	}
	return diagnosticProcess(stateDSN, false)
}

// DiagnosticReplayProcess has no durable job or action adapter. It is explicitly
// for model evaluation against retained synthetic evidence, never customer repair.
func DiagnosticReplayProcess() (DiagnosticInvestigator, error) { return diagnosticProcess("", true) }
func diagnosticProcess(stateDSN string, replay bool) (DiagnosticInvestigator, error) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" || os.Getenv("POOLTUNE_ANTHROPIC_MODEL") == "" {
		return nil, errors.New("diagnostic model configuration required")
	}
	path, err := filepath.Abs("investigator/diagnostic-run.ts")
	if err != nil {
		return nil, err
	}
	if _, err = os.Stat(path); err != nil {
		return nil, errors.New("run from the Pooltune repository")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, input DiagnosticInput, read DiagnosticReader) (result DiagnosticResult, err error) {
		started := time.Now()
		defer func() { result.TotalMS = float64(time.Since(started)) / float64(time.Millisecond) }()
		ctx, cancel := context.WithTimeout(ctx, 22*time.Second)
		defer cancel()
		var budget diagnosticBudget
		if !replay {
			config, err := labdb.StateConfig(stateDSN)
			if err != nil {
				return DiagnosticResult{}, errors.New("invalid diagnostic state route")
			}
			config.RuntimeParams["statement_timeout"] = "3s"
			journal, err := openDiagnosticJournal(ctx, config, input)
			if err != nil {
				return DiagnosticResult{}, err
			}
			defer journal.close()
			read = journal.reader(read)
			budget = diagnosticBudget{journal.attempts, journal.reserveModel}
		}
		return runDiagnostic(ctx, input, read, func(ctx context.Context) *exec.Cmd {
			c := exec.CommandContext(ctx, node, path)
			c.Env = []string{"ANTHROPIC_API_KEY=" + os.Getenv("ANTHROPIC_API_KEY"), "POOLTUNE_ANTHROPIC_MODEL=" + os.Getenv("POOLTUNE_ANTHROPIC_MODEL"), "LANGSMITH_TRACING=false", "POOLTUNE_LAB_STATE_DSN=" + stateDSN, "POOLTUNE_STAGING_PRIVATE_STATE=" + os.Getenv("POOLTUNE_STAGING_PRIVATE_STATE")}
			if replay {
				c.Env = append(c.Env, "POOLTUNE_DIAGNOSTIC_REPLAY=1")
			}
			return c
		}, budget)
	}, nil
}
func validObservation(o DiagnosticObservation, input DiagnosticInput, tool string) bool {
	age := time.Since(o.At)
	return SameDiagnosticIdentity(o.DiagnosticIdentity, input.DiagnosticIdentity) && o.Tool == tool && (diagnosticTools[tool] || (input.Mode == "enrolled_lookup" && tool == "lookup")) && !o.At.IsZero() && age >= 0 && age <= 30*time.Second &&
		((o.Status == "available" && json.Valid(o.Data) && !bytes.Equal(o.Data, []byte("null"))) || (o.Status == "unavailable" && (len(o.Data) == 0 || bytes.Equal(o.Data, []byte("null")))))
}
func strictJSON(data []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

type diagnosticBudget struct {
	initial int
	reserve func(context.Context) (int, error)
}

func runDiagnostic(parent context.Context, input DiagnosticInput, read DiagnosticReader, command func(context.Context) *exec.Cmd, budget ...diagnosticBudget) (result DiagnosticResult, err error) {
	started := time.Now()
	attempts := 0
	if len(budget) > 0 {
		attempts = budget[0].initial
	}
	defer func() {
		result.TotalMS = float64(time.Since(started)) / float64(time.Millisecond)
		result.ModelAttempts = attempts
	}()
	ctx, cancel := context.WithTimeout(parent, 22*time.Second)
	defer cancel()
	if input.RunID == "" || len(input.RunID) > 64 || input.TableOID == 0 || !hostedquery.ValidIndexColumns(input.Column, input.Columns) || len(input.Initial) > 7 {
		return result, errors.New("invalid diagnostic input")
	}
	lookup := input.Mode == "enrolled_lookup"
	if (input.Mode != "" && !lookup) || (lookup && (input.AdmitModel == nil || len(input.Initial) != 1 ||
		input.Initial[0].Tool != "lookup" || input.Initial[0].Status != "available")) {
		return result, errors.New("invalid enrolled lookup input")
	}
	maxAttempts := 3
	if lookup {
		maxAttempts = 1
	}
	observations := map[string]DiagnosticObservation{}
	for _, o := range input.Initial {
		if !validObservation(o, input, o.Tool) || observations[o.Tool].Tool != "" {
			return result, errors.New("invalid initial evidence")
		}
		observations[o.Tool] = o
	}
	c := command(ctx)
	stdin, err := c.StdinPipe()
	if err != nil {
		return result, err
	}
	stdout, err := c.StdoutPipe()
	if err != nil {
		return result, err
	}
	c.Stderr = io.Discard
	if err = c.Start(); err != nil {
		return result, errors.New("diagnostic process failed to start")
	}
	defer func() {
		stdin.Close()
		cancel()
		if c.ProcessState == nil {
			c.Process.Kill()
			c.Wait()
		}
	}()
	payload, marshalErr := json.Marshal(input)
	if marshalErr != nil || len(payload) > 60000 {
		return result, errors.New("oversized diagnostic input")
	}
	encoder := json.NewEncoder(stdin)
	if err = encoder.Encode(input); err != nil {
		return result, errors.New("diagnostic input failed")
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 65536)
	reads := 0
	for scanner.Scan() {
		var frame struct {
			Type   string            `json:"type"`
			Tool   string            `json:"tool,omitempty"`
			Result *DiagnosticResult `json:"result,omitempty"`
		}
		if strictJSON(scanner.Bytes(), &frame) != nil {
			return result, errors.New("invalid diagnostic frame")
		}
		switch frame.Type {
		case "model_start":
			if frame.Tool != "" || frame.Result != nil {
				return result, errors.New("invalid model admission frame")
			}
			allowed := attempts < maxAttempts
			if allowed && input.AdmitModel != nil {
				if err := input.AdmitModel(ctx); err != nil {
					return result, err
				}
			}
			if allowed && len(budget) > 0 && budget[0].reserve != nil {
				var reserveErr error
				attempts, reserveErr = budget[0].reserve(ctx)
				allowed = reserveErr == nil
			} else if allowed {
				attempts++
			}
			if encoder.Encode(struct {
				Permit bool `json:"permit"`
			}{allowed}) != nil {
				return result, errors.New("model admission handoff failed")
			}
		case "read":
			if lookup || frame.Result != nil || !diagnosticTools[frame.Tool] || observations[frame.Tool].Tool != "" || reads >= 6 {
				return result, errors.New("invalid diagnostic evidence request")
			}
			reads++
			o := DiagnosticObservation{DiagnosticIdentity: input.DiagnosticIdentity, Tool: frame.Tool, At: time.Now().UTC(), Status: "unavailable"}
			if read != nil {
				callCtx, stop := context.WithTimeout(ctx, 3*time.Second)
				type reply struct {
					observation DiagnosticObservation
					err         error
				}
				ready := make(chan reply, 1)
				go func() { observation, callErr := read(callCtx, frame.Tool); ready <- reply{observation, callErr} }()
				select {
				case response := <-ready:
					o, err = response.observation, response.err
				case <-callCtx.Done():
					err = callCtx.Err()
				}
				stop()
				if err != nil {
					return result, errors.New("diagnostic evidence collection failed")
				}
			}
			if !validObservation(o, input, frame.Tool) {
				return result, errors.New("invalid diagnostic evidence")
			}
			encoded, e := json.Marshal(o)
			if e != nil || len(encoded) > 60000 {
				return result, errors.New("oversized diagnostic evidence")
			}
			observations[o.Tool] = o
			if encoder.Encode(struct {
				Observation DiagnosticObservation `json:"observation"`
			}{o}) != nil {
				return result, errors.New("diagnostic evidence handoff failed")
			}
		case "result":
			if frame.Tool != "" || frame.Result == nil {
				return result, errors.New("invalid diagnostic result")
			}
			result = *frame.Result
			result.ModelAttempts = attempts
			if result.Reads != reads || result.ModelCalls < 0 || result.ModelCalls > maxAttempts || result.ModelMS < 0 || result.ToolMS < 0 || len(result.History) > 3 || validateDiagnosticResult(input, observations, result) != nil {
				return result, errors.New("unsupported diagnostic result")
			}
			stdin.Close()
			if scanner.Scan() {
				return DiagnosticResult{}, errors.New("trailing diagnostic frame")
			}
			if scanner.Err() != nil || c.Wait() != nil {
				return DiagnosticResult{}, errors.New("diagnostic process failed")
			}
			result.TotalMS = float64(time.Since(started)) / float64(time.Millisecond)
			return result, nil
		default:
			return result, errors.New("unexpected diagnostic frame")
		}
	}
	return result, errors.New("diagnostic process ended without a result")
}
func validateDiagnosticResult(input DiagnosticInput, observations map[string]DiagnosticObservation, result DiagnosticResult) error {
	p := result.Proposal
	if p.RunID != input.RunID || p.TableOID != input.TableOID || p.Column != input.Column || !slices.Equal(p.Columns, input.Columns) || len(p.Reason) == 0 || len(p.Reason) > 1000 || (p.Outcome != "no_change" && p.Outcome != "propose") {
		return errors.New("identity")
	}
	if input.Mode == "enrolled_lookup" && p.Outcome != "no_change" {
		return errors.New("enrolled diagnosis does not authorize repair")
	}
	if p.Outcome == "no_change" {
		if p.Repair != "" {
			return errors.New("unexpected repair")
		}
		return nil
	}
	if len(result.History) == 0 {
		return errors.New("missing cause assessment")
	}
	var decision struct {
		Action      string   `json:"action"`
		Repair      *string  `json:"repair"`
		Tools       []string `json:"tools"`
		Reason      string   `json:"reason"`
		Assessments []struct {
			Cause      string `json:"cause"`
			Assessment string `json:"assessment"`
			Blocks     *bool  `json:"blocks_decision"`
			Reason     string `json:"reason"`
		} `json:"assessments"`
	}
	if strictJSON(result.History[len(result.History)-1], &decision) != nil || decision.Action != "propose" || decision.Repair == nil || len(decision.Tools) != 0 || len(decision.Assessments) < 2 || len(decision.Assessments) > 7 {
		return errors.New("invalid cause assessment")
	}
	chosen := p.Repair
	if chosen == "" {
		chosen = "index"
	}
	if *decision.Repair != chosen {
		return errors.New("changed selected repair")
	}
	supported := 0
	seen := map[string]bool{}
	for _, a := range decision.Assessments {
		if a.Blocks == nil || *a.Blocks || seen[a.Cause] || a.Reason == "" || len(a.Reason) > 200 {
			return errors.New("unresolved cause assessment")
		}
		seen[a.Cause] = true
		if a.Assessment == "supported" {
			supported++
			if a.Cause != chosen {
				return errors.New("competing cause")
			}
		} else if a.Assessment != "rejected" && a.Assessment != "uncertain" {
			return errors.New("invalid assessment")
		}
	}
	if supported != 1 {
		return errors.New("unsupported cause")
	}
	if input.Decision != hostedquery.Investigate {
		return errors.New("not actionable")
	}
	// The statistics detail reader's contract includes fresh ordinary ANALYZE. Strong
	// stale-distribution evidence in the same investigation contradicts it.
	for _, o := range observations {
		if !validObservation(o, input, o.Tool) {
			return errors.New("expired evidence")
		}
	}
	if a, ok := observations["analyze"]; ok && a.Status == "available" {
		if st, ok := observations["statistics"]; ok && st.Status == "available" {
			var av hostedquery.AnalyzeEvidence
			var sv hostedquery.StatisticsEvidence
			if strictJSON(a.Data, &av) != nil || strictJSON(st.Data, &sv) != nil {
				return errors.New("invalid statistics evidence")
			}
			var i struct {
				Covering bool `json:"covering_index"`
			}
			if o, ok := observations["indexes"]; ok && o.Status == "available" && strictJSON(o.Data, &i) != nil {
				return errors.New("invalid index evidence")
			}
			if sv.SupportsRepair(i.Covering) && av.SampledValueFraction != nil && *av.SampledValueFraction >= .9 && av.ActualRows > 0 && float64(av.EstimatedRows)/float64(av.ActualRows) > 10 {
				return errors.New("contradictory statistics evidence")
			}
		}
	}
	required := []string{"plan", "indexes"}
	if p.Repair != "" {
		required = append(required, p.Repair)
	}
	e := hostedquery.IndexEvidence{RunID: input.RunID, TableOID: input.TableOID, Column: input.Column, Columns: input.Columns, Decision: input.Decision, Label: "lookup", At: time.Now()}
	for _, tool := range required {
		o, ok := observations[tool]
		if !ok || !validObservation(o, input, tool) || o.Status != "available" {
			return errors.New("missing evidence")
		}
		if o.At.Before(e.At) {
			e.At = o.At
		}
		var target any
		switch tool {
		case "plan":
			target = &e.Plan
		case "indexes":
			var index struct {
				Covering bool `json:"covering_index"`
			}
			if strictJSON(o.Data, &index) != nil {
				return errors.New("index evidence")
			}
			e.Existing = index.Covering
			continue
		case "analyze":
			e.Analyze = &hostedquery.AnalyzeEvidence{}
			target = e.Analyze
		case "statistics":
			e.Statistics = &hostedquery.StatisticsEvidence{}
			target = e.Statistics
		case "vacuum":
			e.Vacuum = &hostedquery.VacuumEvidence{}
			target = e.Vacuum
		case "memory":
			e.Memory = &hostedquery.MemoryEvidence{}
			target = e.Memory
		case "cron":
			e.Cron = &hostedquery.CronEvidence{}
			target = e.Cron
		default:
			return errors.New("repair")
		}
		if strictJSON(o.Data, target) != nil {
			return errors.New("malformed evidence")
		}
	}
	return hostedquery.ValidateDiagnosticProposal(e, p, time.Now())
}

// SnapshotObservations preserves measurement time; callers may expose several
// independently collected cause fields. Missing fields are never fabricated.
func SnapshotObservations(e hostedquery.IndexEvidence) []DiagnosticObservation {
	id := DiagnosticIdentity{RunID: e.RunID, TableOID: e.TableOID, Column: e.Column, Columns: e.Columns}
	out := []DiagnosticObservation{}
	add := func(tool string, value any) {
		data, _ := json.Marshal(value)
		out = append(out, DiagnosticObservation{id, tool, e.At, "available", data})
	}
	add("plan", e.Plan)
	add("indexes", struct {
		Covering bool `json:"covering_index"`
	}{e.Existing})
	if e.Analyze != nil {
		add("analyze", e.Analyze)
	}
	if e.Statistics != nil {
		add("statistics", e.Statistics)
	}
	if e.Vacuum != nil {
		add("vacuum", e.Vacuum)
	}
	if e.Memory != nil {
		add("memory", e.Memory)
	}
	if e.Cron != nil {
		add("cron", e.Cron)
	}
	return out
}

// SameDiagnosticIdentity includes ordered index keys in evidence and replay binding.
func SameDiagnosticIdentity(a, b DiagnosticIdentity) bool {
	return a.RunID == b.RunID && a.TableOID == b.TableOID && a.Column == b.Column && slices.Equal(a.Columns, b.Columns)
}
