// Command pooltune-diagnostic-eval evaluates the real investigator against retained
// synthetic measurements. It has no database connection or repair executor.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"pooltune/internal/graphrunner"
	"pooltune/internal/hostedquery"
	"time"
)

type testCase struct {
	Name         string                    `json:"name"`
	Source       string                    `json:"source"`
	Kind         string                    `json:"kind"`
	Evidence     hostedquery.IndexEvidence `json:"evidence"`
	InitialTools []string                  `json:"initial_tools"`
	Expected     string                    `json:"expected"`
	MinReads     int                       `json:"min_reads"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Diagnostic evaluation failed.")
		os.Exit(1)
	}
}
func run() error {
	var cases []testCase
	d := json.NewDecoder(io.LimitReader(os.Stdin, 2<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&cases); err != nil {
		return err
	}
	if len(cases) == 0 || len(cases) > 20 {
		return fmt.Errorf("invalid case count")
	}
	investigate, err := graphrunner.DiagnosticReplayProcess()
	if err != nil {
		return err
	}
	failed := false
	for i, c := range cases {
		original := c.Evidence.At
		c.Evidence.RunID = fmt.Sprintf("diagnostic-eval-%d-%d", time.Now().UnixMilli(), i)
		c.Evidence.At = time.Now().UTC()
		observations := graphrunner.SnapshotObservations(c.Evidence)
		id := graphrunner.DiagnosticIdentity{RunID: c.Evidence.RunID, TableOID: c.Evidence.TableOID, Column: c.Evidence.Column, Columns: c.Evidence.Columns}
		input := graphrunner.DiagnosticInput{DiagnosticIdentity: id, Decision: c.Evidence.Decision, Initial: []graphrunner.DiagnosticObservation{}}
		for _, o := range observations {
			for _, tool := range c.InitialTools {
				if o.Tool == tool {
					input.Initial = append(input.Initial, o)
				}
			}
		}
		requested := []string{}
		result, runErr := investigate(context.Background(), input, func(ctx context.Context, tool string) (graphrunner.DiagnosticObservation, error) {
			requested = append(requested, tool)
			for _, o := range observations {
				if o.Tool == tool {
					return o, nil
				}
			}
			return graphrunner.DiagnosticObservation{DiagnosticIdentity: id, Tool: tool, At: time.Now().UTC(), Status: "unavailable"}, nil
		})
		actual := result.Proposal.Outcome
		if actual == "propose" {
			actual = result.Proposal.Repair
			if actual == "" {
				actual = "index"
			}
		}
		modelChoice := "no_change"
		if len(result.History) > 0 {
			var last struct {
				Action string  `json:"action"`
				Repair *string `json:"repair"`
			}
			if json.Unmarshal(result.History[len(result.History)-1], &last) != nil {
				modelChoice = "invalid"
			} else {
				modelChoice = last.Action
				if last.Action == "propose" && last.Repair != nil {
					modelChoice = *last.Repair
				}
			}
		}
		passed := runErr == nil && actual == c.Expected && modelChoice == c.Expected && len(requested) >= c.MinReads && (result.StopReason == "" || result.StopReason == "no_change")
		if !passed {
			failed = true
		}
		record := map[string]any{"model_choice": modelChoice, "guard_veto": actual != modelChoice, "name": c.Name, "kind": c.Kind, "source": c.Source, "source_observed_at": original, "replay_clock_rebased": true, "no_database_mutation": true, "expected": c.Expected, "actual": actual, "passed": passed, "requested_tools": requested, "result": result}
		if runErr != nil {
			record["error"] = "diagnostic process or proposal validation failed"
		}
		if err = json.NewEncoder(os.Stdout).Encode(record); err != nil {
			return err
		}
	}
	if failed {
		return fmt.Errorf("qualification cases failed")
	}
	return nil
}
