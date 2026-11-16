package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupPrintsProjectSpecificSQLWithoutCredentials(t *testing.T) {
	t.Setenv("POOLTUNE_PROJECT_PASSWORD", "test-password-must-not-print")
	var output bytes.Buffer
	options := []string{"setup-sql", "-ref", "osdjqwpzgyzwfaxyufze", "-key", "123456789abcdef0"}
	if err := execute(context.Background(), options, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "CREATE ROLE pt_monitor_123456789abcdef0 NOLOGIN") ||
		strings.Contains(output.String(), "test-password-must-not-print") {
		t.Fatal("setup did not produce scoped, credential-free SQL")
	}
}

func TestRepairAccessValidatesScopeBeforeConnecting(t *testing.T) {
	const project = "supabase/abcdefghijklmnopqrst/0123456789abcdef"
	t.Setenv("POOLTUNE_WORKER_STATE_DSN", "must-not-be-read")
	t.Setenv("POOLTUNE_REPAIR_DB_ROLE", "")
	t.Setenv("POOLTUNE_REPAIR_DB_PASSWORD", "")
	for _, test := range []struct {
		name, scope, want string
	}{
		{"unknown field", `{"project":"` + project + `","capabilities":["index"],"sql":"SELECT 1"}`, "valid repair scope"},
		{"extra document", `{"project":"` + project + `","capabilities":["index"]}{}`, "valid repair scope"},
		{"foreign project", `{"project":"supabase/abcdefghijklmnopqrst/123456789abcdef0","capabilities":["index"]}`, "match the connected project"},
		{"missing repair login", `{"project":"` + project + `","capabilities":["index"]}`, "POOLTUNE_REPAIR_DB_ROLE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "scope.json")
			if err := os.WriteFile(path, []byte(test.scope), 0600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			args := []string{"repair-access", "-ref", "abcdefghijklmnopqrst", "-key", "0123456789abcdef", "-scope", path}
			err := execute(context.Background(), args, &output)
			if err == nil || !strings.Contains(err.Error(), test.want) || output.Len() != 0 {
				t.Fatalf("got %q and %v, want error containing %q", output.String(), err, test.want)
			}
		})
	}
}

func TestSetupRefusesInvalidInstallationKey(t *testing.T) {
	var output bytes.Buffer
	options := []string{"setup-sql", "-ref", "osdjqwpzgyzwfaxyufze", "-key", "123456789abcdef;"}
	if err := execute(context.Background(), options, &output); err == nil || output.Len() != 0 {
		t.Fatal("setup accepted an invalid key")
	}
}
