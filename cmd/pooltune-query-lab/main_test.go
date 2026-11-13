package main

import (
	"strings"
	"testing"
)

func TestConnectionRoutes(t *testing.T) {
	local := "postgres://lab:test@127.0.0.1:5432/state?sslmode=disable"
	remote := "postgres://postgres.osdjqwpzgyzwfaxyufze:test@aws-0-us-west-2.pooler.supabase.com:5432/postgres?sslmode=verify-full"
	if config, err := labConfig(local, false); err != nil || config.Host != "127.0.0.1" ||
		config.Database != "state" || config.Password != "test" || len(config.Fallbacks) != 0 {
		t.Fatal("local fixture route is unavailable")
	}

	for _, test := range []struct {
		name, dsn string
		rookery   bool
	}{
		{"remote state", remote, false},
		{"unselected remote", remote, false},
		{"other project", strings.Replace(remote, "osdjqwpzgyzwfaxyufze", "otherproject", 1), true},
		{"other user", strings.Replace(remote, "postgres.", "service.", 1), true},
		{"other host", strings.Replace(remote, "aws-0-us-west-2", "aws-0-us-east-1", 1), true},
		{"other port", strings.Replace(remote, ":5432", ":6543", 1), true},
		{"other database", strings.Replace(remote, "/postgres?", "/other?", 1), true},
		{"downgrade", strings.Replace(remote, "verify-full", "require", 1), true},
		{"ambiguous ssl", local + "&sslmode=verify-full", false},
		{"fallback", strings.Replace(local, "127.0.0.1", "127.0.0.1,elsewhere", 1), false},
		{"query redirect", local + "&host=elsewhere", false},
		{"query options", local + "&options=x", false},
		{"missing password", strings.Replace(local, "lab:test", "lab", 1), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := labConfig(test.dsn, test.rookery); err == nil {
				t.Fatal("accepted invalid lab route")
			} else if strings.Contains(err.Error(), "lab:test") || strings.Contains(err.Error(), ":test@") {
				t.Fatal("error included a DSN")
			}
		})
	}
}
