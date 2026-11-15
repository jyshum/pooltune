package main

import (
	"strings"
	"testing"
)

func TestRecordRequiresPrivateRailwayStaging(t *testing.T) {
	t.Setenv("POOLTUNE_STAGING_PRIVATE_STATE", "")
	err := record(strings.NewReader(`{}`))
	if err == nil || !strings.Contains(err.Error(), "private Railway staging database") {
		t.Fatalf("recorder did not reject non-staging execution before reading input: %v", err)
	}
}
