package main

import (
	"context"
	"encoding/json"
	"os"
	"pooltune/internal/provider"
	"strings"
	"testing"
)

func TestManagedProfileRequiresExplicitScopeAndPrivatePaths(t *testing.T) {
	base := `{"provider":"azure","target":"db","subscription":"sub","resource_group":"group","instance":"instance","database":"postgres","role":"app","directory":"/tmp/state","secret_root":"/tmp/secrets","password_file":"/tmp/secrets/password","ca_file":"/etc/ssl/cert.pem","exclusive_management":true,"other_reserve":32,"bounds":{"minimum":5,"maximum":8,"cpu_grow":0.7,"cpu_backoff":0.85,"memory_grow":0.8,"memory_backoff":0.9}}`
	if _, e := loadManaged(strings.NewReader(base)); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{strings.Replace(base, `"exclusive_management":true`, `"exclusive_management":false`, 1), strings.Replace(base, `"password_file":"/tmp/secrets/password"`, `"password_file":"relative"`, 1), strings.Replace(base, `"provider":"azure"`, `"provider":"unknown"`, 1)} {
		if _, e := loadManaged(strings.NewReader(bad)); e == nil {
			t.Fatal("unsafe profile accepted")
		}
	}
}

func TestSupabaseResumeIsRefusedBeforeAnyProviderRequest(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	profile := map[string]any{"provider": "supabase", "target": "supabase-test", "project": "project", "database": "postgres", "role": "postgres", "directory": dir + "/state", "secret_root": dir, "password_file": dir + "/password", "token_file": dir + "/token", "ca_file": "/etc/ssl/cert.pem", "exclusive_management": true, "other_reserve": 32, "bounds": map[string]any{"minimum": 5, "maximum": 10, "cpu_grow": .7, "cpu_backoff": .85, "memory_grow": .8, "memory_backoff": .9}}
	raw, _ := json.Marshal(profile)
	path := dir + "/profile.json"
	os.WriteFile(path, raw, 0600)
	// No credentials exist. A request or SQL connection would fail differently.
	if e := runManaged(context.Background(), path, true); e != provider.ErrUnsupported {
		t.Fatalf("unsafe resume path: %v", e)
	}
}
