package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoveryDrillRequiresScriptedRookeryAndPrivateState(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "pooltune-query-lab")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build lab: %v\n%s", err, output)
	}
	for _, tc := range []struct {
		target, mode, private string
		valid                 bool
	}{
		{target: "local", mode: "scripted", private: "1"},
		{target: "rookery", mode: "anthropic", private: "1"},
		{target: "rookery", mode: "scripted", private: "0"},
		{target: "rookery", private: "1"},
		{target: "rookery", mode: "scripted", private: "1", valid: true},
	} {
		t.Run(tc.target+"/"+tc.mode+"/"+tc.private, func(t *testing.T) {
			cmd := exec.Command(binary, "-target", tc.target, "-index-trial", tc.mode, "-pause-after-index-build")
			cmd.Env = append(os.Environ(), "POOLTUNE_STAGING_PRIVATE_STATE="+tc.private, "POOLTUNE_LAB_STATE_DSN=")
			out, err := cmd.CombinedOutput()
			exit, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("expected exit error, got %v: %s", err, out)
			}
			if tc.valid {
				if exit.ExitCode() != 1 || !strings.Contains(string(out), "state configuration") {
					t.Fatalf("expected the private state check, exit %d: %s", exit.ExitCode(), out)
				}
			} else if exit.ExitCode() != 2 || !strings.Contains(string(out), "recovery drill requires") {
				t.Fatalf("expected the drill guard, exit %d: %s", exit.ExitCode(), out)
			}
		})
	}
}
