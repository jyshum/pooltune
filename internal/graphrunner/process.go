package graphrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"pooltune/internal/hostedquery"
)

func Process(mode, stateDSN string) (hostedquery.IndexInvestigator, error) {
	if mode == "anthropic" && (os.Getenv("ANTHROPIC_API_KEY") == "" || os.Getenv("POOLTUNE_ANTHROPIC_MODEL") == "") {
		return nil, errors.New("anthropic mode requires ANTHROPIC_API_KEY and POOLTUNE_ANTHROPIC_MODEL")
	}
	path, err := filepath.Abs("investigator/run.ts")
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		return nil, errors.New("run from the Pooltune repo with investigator dependencies installed")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return nil, errors.New("Node.js is required for the investigator")
	}
	return func(ctx context.Context, e hostedquery.IndexEvidence) (hostedquery.IndexProposal, error) {
		ctx, cancel := context.WithTimeout(ctx, 22*time.Second)
		defer cancel()
		payload, err := json.Marshal(e)
		if err != nil {
			return hostedquery.IndexProposal{}, err
		}
		call := exec.CommandContext(ctx, node, path, mode)
		call.Stdin = bytes.NewReader(payload)
		call.Env = []string{"POOLTUNE_LAB_STATE_DSN=" + stateDSN, "LANGSMITH_TRACING=false", "POOLTUNE_STAGING_PRIVATE_STATE=" + os.Getenv("POOLTUNE_STAGING_PRIVATE_STATE")}
		if mode == "anthropic" {
			call.Env = append(call.Env, "ANTHROPIC_API_KEY="+os.Getenv("ANTHROPIC_API_KEY"),
				"POOLTUNE_ANTHROPIC_MODEL="+os.Getenv("POOLTUNE_ANTHROPIC_MODEL"))
		}
		response, err := call.Output()
		if err != nil {
			return hostedquery.IndexProposal{}, errors.New("investigator process failed")
		}
		if len(response) > 4096 {
			return hostedquery.IndexProposal{}, errors.New("oversized investigator response")
		}
		var proposal hostedquery.IndexProposal
		decoder := json.NewDecoder(bytes.NewReader(response))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&proposal); err != nil || !json.Valid(response) {
			return hostedquery.IndexProposal{}, errors.New("invalid investigator response")
		}
		return proposal, nil
	}, nil
}
