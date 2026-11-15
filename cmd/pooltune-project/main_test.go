package main

import (
	"bytes"
	"context"
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

func TestSetupRefusesInvalidInstallationKey(t *testing.T) {
	var output bytes.Buffer
	options := []string{"setup-sql", "-ref", "osdjqwpzgyzwfaxyufze", "-key", "123456789abcdef;"}
	if err := execute(context.Background(), options, &output); err == nil || output.Len() != 0 {
		t.Fatal("setup accepted an invalid key")
	}
}
