package hostedapp

import (
	"bytes"
	"reflect"
	"testing"
)

func TestProjectSecretIsBoundToOwnerAndInstallation(t *testing.T) {
	vault, err := newVault(bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	project := projectRecord{ID: "0123456789abcdef", Ref: "abcdefghijklmnopqrst", Owner: 7}
	secrets := projectSecrets{
		Password: "an-example-monitor-password", CA: "a certificate", LogsToken: "a scoped token",
		Automatic: true, Capabilities: []string{"index", "cron"},
	}
	sealed, err := vault.seal(project, secrets)
	if err != nil || bytes.Contains(sealed, []byte(secrets.Password)) {
		t.Fatalf("could not protect project credentials: %v", err)
	}
	opened, err := vault.open(project, sealed)
	if err != nil || !reflect.DeepEqual(opened, secrets) {
		t.Fatalf("could not restore project credentials: %v", err)
	}
	for _, different := range []projectRecord{
		{ID: project.ID, Ref: project.Ref, Owner: 8},
		{ID: "fedcba9876543210", Ref: project.Ref, Owner: project.Owner},
		{ID: project.ID, Ref: "bcdefghijklmnopqrstu", Owner: project.Owner},
	} {
		if _, err := vault.open(different, sealed); err == nil {
			t.Fatal("credentials opened for a different project or owner")
		}
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := vault.open(project, sealed); err == nil {
		t.Fatal("modified credentials accepted")
	}
}
