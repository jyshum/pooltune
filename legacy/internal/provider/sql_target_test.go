package provider

import (
	"context"
	"testing"
	"time"
)

func TestSQLTargetRejectsDifferentResourceDespiteValidHostname(t *testing.T) {
	for _, c := range []Config{{Kind: Azure, Target: Target{ID: "a"}, Subscription: "sub", ResourceGroup: "rg", Instance: "server-a"}, {Kind: Supabase, Target: Target{ID: "s"}, Project: "project-a"}} {
		c.Timeout = time.Second
		c.Freshness = time.Minute
		c.Token = func(context.Context) (string, error) { return "unused", nil }
		a, e := NewDiscovery(c)
		if e != nil {
			t.Fatal(e)
		}
		for _, host := range []string{"server-b.postgres.database.azure.com", "db.project-b.supabase.co"} {
			if e = a.VerifySQLTarget(context.Background(), host, host, "postgres.project-b"); e == nil {
				t.Fatal("mismatched provider accepted")
			}
		}
	}
}
