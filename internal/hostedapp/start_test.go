package hostedapp

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
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

func TestAutomaticSetupResumesAfterRepairCredentialFailure(t *testing.T) {
	ctx := context.Background()
	installed, readerInstalled, activated := false, false, false
	installations := 0
	passwordValid := false
	setup := func(context.Context) error {
		installations++
		if installed {
			return errors.New("monitor already exists")
		}
		installed = true
		return nil
	}
	verify := func(_ context.Context, includeRepairEvidence bool) error {
		if !installed {
			return errors.New("monitor absent")
		}
		if includeRepairEvidence && !readerInstalled {
			return errors.New("reader absent")
		}
		return nil
	}
	configure := func(context.Context) error {
		if !passwordValid {
			return errors.New("database credential rejected")
		}
		readerInstalled = true
		return nil
	}
	activate := func(context.Context) error { activated = true; return nil }
	if err := completeAutomaticSetup(ctx, false, setup, verify, configure, activate); err == nil || activated {
		t.Fatal("failed repair setup activated project")
	}
	passwordValid = true
	if err := completeAutomaticSetup(ctx, true, setup, verify, configure, activate); err != nil {
		t.Fatalf("retry did not resume the installed monitor: %v", err)
	}
	if installations != 1 || !readerInstalled || !activated {
		t.Fatalf("retry state: installs=%d reader=%v active=%v", installations, readerInstalled, activated)
	}
}

func TestSetupObjective(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int
		valid bool
	}{
		{"", 0, true}, {"1", 1, true}, {"25", 25, true}, {"1000", 1000, true},
		{"0", 0, false}, {"1001", 0, false}, {"NaN", 0, false}, {"2.5", 0, false}, {"1;SELECT", 0, false},
	} {
		got, err := setupObjective(tc.value)
		if (err == nil) != tc.valid || got != tc.want {
			t.Errorf("goal %q: %d %v", tc.value, got, err)
		}
	}
}

func TestAutomaticSetupRejectsOversizedRepairPassword(t *testing.T) {
	app := &App{}
	row := projectRecord{Ref: "abcdefghijklmnopqrst", ID: "0123456789abcdef"}
	secret := strings.Repeat("x", 513)
	err := app.startAutomaticProject(context.Background(), row, "token", "invalid ca", []string{"index"}, secret, 25)
	if err == nil || err.Error() != "enter a valid database credential" || strings.Contains(err.Error(), secret) {
		t.Fatalf("oversized repair credential was not rejected safely: %v", err)
	}
}
