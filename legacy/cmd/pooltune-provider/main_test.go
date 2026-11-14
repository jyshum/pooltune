package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestDiscoveryRejectsWriteAuthorityAndInvalidCredentialPaths(t *testing.T) {
	for _, raw := range []string{`{"provider":"azure","target":"db","changes_enabled":true}`, `{"provider":"supabase","target":"db"}`, `{"provider":"google","token_file":"relative"}`, `{} {}`} {
		if _, e := load(strings.NewReader(raw)); e == nil {
			t.Fatal("unsafe discovery profile accepted")
		}
	}
}
func TestDiscoveryRejectsMissingResourceBeforeFetchingCredentials(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, e := discover(ctx, profile{}); e == nil {
		t.Fatal("empty profile accepted")
	}
}
