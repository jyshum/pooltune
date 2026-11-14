//go:build managedlive

package provider

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPressureDiagnostic(t *testing.T) {
	root := os.Getenv("POOLTUNE_PRESSURE_DIAGNOSTIC")
	if root == "" {
		t.Skip("explicit diagnostic only")
	}
	project := strings.TrimSpace(os.Getenv("POOLTUNE_TEST_SUPABASE_PROJECT"))
	if project == "" {
		t.Fatal("POOLTUNE_TEST_SUPABASE_PROJECT required")
	}
	token, e := os.ReadFile(root + "/private-cloud-credentials/supabase-management-token.txt")
	if e != nil {
		t.Fatal("secret file")
	}
	a, e := NewDiscovery(Config{Kind: Supabase, Target: Target{ID: "diag"}, Project: project, Timeout: 5 * time.Second, Freshness: time.Minute, Token: func(context.Context) (string, error) { return strings.TrimSpace(string(token)), nil }})
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 6; i++ {
		p, e := a.Pressure(context.Background())
		t.Logf("sample %d age=%s cpu=%.3f memory=%.3f error=%v", i, time.Since(p.At), p.CPU, p.Memory, e)
		if e == nil {
			return
		}
		time.Sleep(15 * time.Second)
	}
	t.Fatal("pressure unavailable")
}
