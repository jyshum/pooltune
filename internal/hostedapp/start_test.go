package hostedapp

import (
	"net/url"
	"slices"
	"testing"
)

func TestCapabilityChoices(t *testing.T) {
	keys := []string{"index", "analyze", "statistics", "vacuum", "memory", "cron"}
	for _, tc := range []struct {
		name   string
		values url.Values
		want   []string
	}{
		{name: "monitor only"},
		{name: "explicit subset", values: url.Values{"capability": {"cron", "index", "index"}}, want: []string{"index", "cron"}},
		{name: "select all", values: url.Values{"all_capabilities": {"1"}}, want: keys},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := capabilityChoices(tc.values)
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("capabilityChoices = %v, %v, expected %v", got, err, tc.want)
			}
		})
	}
	if _, err := capabilityChoices(url.Values{"capability": {"unknown"}, "all_capabilities": {"1"}}); err == nil {
		t.Fatal("unexpected operation accepted")
	}
}
