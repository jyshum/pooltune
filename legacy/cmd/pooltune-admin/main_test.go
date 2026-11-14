package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApprovalWritesPrivateTokenWithoutPrintingIt(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	db := filepath.Join(dir, "gateway.db")
	token := filepath.Join(dir, "installation-token")
	var output bytes.Buffer
	args := []string{"-db", db, "-action", "approve", "-account", "test-account", "-global-micros", "5000000", "-allowance-micros", "1000000", "-token-file", token}
	if err := run(args, &output); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(token)
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.TrimSpace(string(raw))) != 43 || strings.Contains(output.String(), strings.TrimSpace(string(raw))) {
		t.Fatal("token missing or printed")
	}
	info, _ := os.Stat(token)
	if info.Mode().Perm() != 0600 {
		t.Fatal("token permissions")
	}
	if err = run(args, &output); err == nil {
		t.Fatal("token overwritten")
	}
}
