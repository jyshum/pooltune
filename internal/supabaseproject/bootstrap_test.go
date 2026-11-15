package supabaseproject

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type setupTransport func(*http.Request) (*http.Response, error)

func (f setupTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSetupDefaultScope(t *testing.T) {
	project, err := New("abcdefghijklmnopqrst", "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	password := "an example generated password for provisioning"
	script, err := project.bootstrapSQL(password)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(script, password) || strings.Count(script, "COMMIT;") != 1 ||
		!strings.Contains(script, "ALTER ROLE "+project.Role()+" LOGIN PASSWORD 'SCRAM-SHA-256$4096:") {
		t.Fatal("setup script should use one transaction and a SCRAM verifier")
	}

	client := &http.Client{Transport: setupTransport(func(r *http.Request) (*http.Response, error) {
		want := "https://api.supabase.com/v1/projects/" + project.Ref() + "/database/query"
		if r.URL.String() != want || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatal("setup did not use the exact project and API")
		}
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Query != script {
			t.Fatal("unexpected setup payload")
		}
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader("sensitive provider payload"))}, nil
	})}

	if err := project.postSetup(context.Background(), client, "test-token", script); !errors.Is(err, ErrSetupPermission) {
		t.Fatalf("provider permission error = %v", err)
	}
}

func TestOwnedIndexProbeDiscoveryOnlyChangesFixtureRole(t *testing.T) {
	project, err := New(OwnedIndexProbeRef, "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	script, err := project.bootstrapSQLWithDiscovery("test-password", true)
	if err != nil {
		t.Fatal(err)
	}
	const scope = "CASE WHEN a.userid = to_regrole('pt_hosted_ix_0123456789abcdef')::oid THEN 2.0 ELSE 1000.0 END"
	if !strings.Contains(script, scope) || strings.Count(script, "COMMIT;") != 1 {
		t.Fatal("owned probe did not bind the discovery objective to its test role")
	}
	other, _ := New("abcdefghijklmnopqrst", "0123456789abcdef")
	if _, err := other.bootstrapSQLWithDiscovery("test-password", true); !errors.Is(err, ErrSetupPermission) {
		t.Fatalf("unsupported project admitted owned test objective: %v", err)
	}
	normal, err := project.bootstrapSQL("test-password")
	if err != nil || strings.Contains(normal, "pt_hosted_ix_") {
		t.Fatalf("ordinary project setup received the test objective: %v", err)
	}
}
