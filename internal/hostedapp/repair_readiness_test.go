package hostedapp

import (
	"bytes"
	"html/template"
	"slices"
	"strings"
	"testing"
)

func TestRepairReadinessPreservesChoicesWithoutPromisingAllRepairs(t *testing.T) {
	view := ProjectView{ID: "project", Status: "Monitoring", ManagedRepairs: true, RepairsSelected: true, Capabilities: slices.Clone(projectCapabilities)}
	for i := range view.Capabilities {
		view.Capabilities[i].Selected = true
		view.Capabilities[i].Availability = repairAvailability(view.Capabilities[i].Key, true)
	}
	page := renderRepairReadiness(t, view)
	if strings.Count(page, " checked>") != 6 {
		t.Fatal("readiness guidance removed saved repair choices")
	}
	for _, expected := range []string{"one- or two-column integer lookups", "stale statistics", "plain table cleanup", "keeps verified improvements", "chosen function", "simultaneous calls", "chosen job", "permitted schedule window", "measured job history"} {
		if !strings.Contains(page, expected) {
			t.Errorf("missing repair limit %q", expected)
		}
	}
	if strings.Contains(page, "Create worker key") || strings.Contains(page, "Waiting for repair support") {
		t.Fatal("managed setup showed an operator step or vague repair readiness")
	}
}

func TestMonitoringOnlyConnectionExplainsManagedCredentialRoute(t *testing.T) {
	view := ProjectView{ID: "project", Status: "Monitoring", ManagedCredentialNeeded: true, RepairsSelected: true,
		Capabilities: []CapabilityView{{Key: "index", Label: "Add indexes", Selected: true, Availability: repairAvailability("index", false)}}}
	page := renderRepairReadiness(t, view)
	for _, expected := range []string{"Connect repair access", "Disconnect this project, then reconnect", "database password", "Disconnecting stops monitoring", "Pooltune will run the worker on Railway"} {
		if !strings.Contains(page, expected) {
			t.Errorf("missing managed connection guidance %q", expected)
		}
	}
	if strings.Contains(page, "/repair-worker-key") || strings.Contains(page, "Create worker key") {
		t.Fatal("monitoring-only customer was routed to an operator-managed worker")
	}
}

func TestExistingOwnerHostedWorkerKeepsItsControls(t *testing.T) {
	page := renderRepairReadiness(t, ProjectView{ID: "project", Status: "Monitoring", RepairsSelected: true, RepairWorkerLinked: true})
	if !strings.Contains(page, "Replace worker key") || !strings.Contains(page, "Revoke worker key") {
		t.Fatal("existing owner-hosted worker controls were removed")
	}
}

func renderRepairReadiness(t *testing.T, view ProjectView) string {
	t.Helper()
	parsed, err := template.ParseFS(frontend, "ui/*.html")
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	if err := parsed.ExecuteTemplate(&body, "repair-permissions", view); err != nil {
		t.Fatal(err)
	}
	return body.String()
}
