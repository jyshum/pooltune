package supabaseproject

import (
	"context"
	"strings"
	"testing"
)

func TestManagedRepairRejectsInvalidCredentialsWithoutExposingThem(t *testing.T) {
	project, _ := New("abcdefghijklmnopqrst", "0123456789abcdef")
	const secret = "private-database-password"
	for _, password := range []string{"", secret, strings.Repeat(secret, 30)} {
		err := project.ValidateManagedRepair(context.Background(), password, []byte("invalid certificate"))
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("unexpected credential validation result: %v", err)
		}
	}
	if err := project.InstallManagedIndexReader(context.Background(), "invalid\ntoken"); err != ErrSetupPermission {
		t.Fatalf("invalid setup token was accepted: %v", err)
	}
}
