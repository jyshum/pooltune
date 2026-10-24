package config

import (
	"strings"
	"testing"
)

// Synthetic configuration. These bounds are test data, not calibrated defaults.
const validJSON = `{
  "schema_version":"v1", "automation_enabled":true,
  "targets":[{"id":"db1","owner":"node1",
    "postgres":{"admission_floor_transactions":1,"admission_start_transactions":2,"admission_ceiling_transactions":4,"physical_ceiling_connections":8,"critical_reserve_transactions":1},
    "rest":{"max_in_flight_requests":4,"physical_allocation_connections":2,"critical_reserve_requests":1},
    "guardrails":{"version":"v1","cpu":{"grow":0.5,"backoff":0.7,"stop":0.9},"memory":{"grow":0.5,"backoff":0.7,"stop":0.9},"metric_max_age":{"cpu":"10s","memory":"10s","capacity":"10s","connections":"10s"},"max_occupancy":{"critical":"2s","normal":"3s","background":"4s"}}
  }],
  "evaluation_profile":{"version":"v1","min_window":"30s","max_window":"5m","min_samples_per_workload":2000,"max_failure_rate_increase":0.01,"max_latency_ratio":1.05},
  "model_profile":{"gateway_profile_id":"approved-test-profile"},
  "secret_refs":{"postgres_credential_file":"/secrets/pg","installation_token_file":"/secrets/installation-token"}
}`

const gatewayJSON = `{"schema_version":"v1","gateway":{"provider":"anthropic","model":"test-model","provider_api_key_file":"/secrets/provider-key"}}`

func TestLoadConfigAcceptsSyntheticBounds(t *testing.T) {
	c, err := LoadConfig(strings.NewReader(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	if !c.AutomationEnabled || len(c.Targets) != 1 {
		t.Fatal("config fields lost")
	}
}

func TestLoadConfigRejectsInvalidInput(t *testing.T) {
	tests := []struct{ name, input string }{
		{"postgres field in rest", strings.Replace(validJSON, `"max_in_flight_requests":4`, `"admission_ceiling_transactions":4`, 1)},
		{"missing safety threshold", strings.Replace(validJSON, `"stop":0.9`, `"stop":0`, 1)},
		{"missing freshness", strings.Replace(validJSON, `"capacity":"10s",`, ``, 1)},
		{"unknown nested field", strings.Replace(validJSON, `"grow":0.5`, `"grow":0.5,"other":1`, 1)},
		{"duplicate JSON key", strings.Replace(validJSON, `"schema_version":"v1"`, `"schema_version":"v1","schema_version":"v1"`, 1)},
		{"case variant key", strings.Replace(validJSON, `"schema_version":"v1"`, `"Schema_Version":"v1"`, 1)},
		{"trailing JSON", validJSON + `{}`},
		{"invalid duration unit", strings.Replace(validJSON, `"min_window":"30s"`, `"min_window":30`, 1)},
		{"missing profile", strings.Replace(validJSON, `"min_samples_per_workload":2000`, `"min_samples_per_workload":0`, 1)},
		{"missing model", strings.Replace(validJSON, `"gateway_profile_id":"approved-test-profile"`, `"gateway_profile_id":""`, 1)},
		{"missing secret ref", strings.Replace(validJSON, `"installation_token_file":"/secrets/installation-token"`, `"installation_token_file":""`, 1)},
		{"provider key in customer config", strings.Replace(validJSON, `"installation_token_file":"/secrets/installation-token"`, `"installation_token_file":"/secrets/installation-token","provider_api_key_file":"/secrets/provider-key"`, 1)},
		{"oversized", strings.Repeat(" ", 1<<20) + validJSON},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := LoadConfig(strings.NewReader(tc.input)); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestModeConfigurationBoundary(t *testing.T) {
	customer, err := LoadConfig(strings.NewReader(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"traffic", "control"} {
		if err := ValidateForMode(customer, mode); err != nil {
			t.Fatalf("%s rejected customer config: %v", mode, err)
		}
	}
	if err := ValidateForMode(customer, "gateway"); err == nil {
		t.Fatal("gateway accepted customer config")
	}
	gateway, err := LoadConfig(strings.NewReader(gatewayJSON))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateForMode(gateway, "gateway"); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"traffic", "control"} {
		if err := ValidateForMode(gateway, mode); err == nil {
			t.Fatalf("%s accepted provider key config", mode)
		}
	}
	if _, err := LoadConfig(strings.NewReader(strings.Replace(gatewayJSON, `"/secrets/provider-key"`, `""`, 1))); err == nil {
		t.Fatal("gateway without provider key accepted")
	}
}

func TestValidateRejectsDuplicateTargetOwnership(t *testing.T) {
	c, err := LoadConfig(strings.NewReader(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	c.Targets = append(c.Targets, c.Targets[0])
	c.Targets[1].Owner = "node2"
	if err := Validate(c); err == nil {
		t.Fatal("duplicate target accepted")
	}
}
