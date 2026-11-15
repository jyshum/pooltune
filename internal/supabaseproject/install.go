package supabaseproject

import (
	_ "embed"
	"strings"
)

//go:embed install.sql
var installTemplate string

//go:embed snapshot.sql
var snapshotTemplate string

//go:embed manual_snapshot.sql
var manualSnapshot string

//go:embed automatic_discovery.sql
var automaticDiscovery string

//go:embed automatic_snapshot.sql
var automaticSnapshot string

// InstallSQL is run by the project owner. The derived identifier contains only
// lowercase ASCII letters, numbers and underscores. The script contains no secret.
func (p Project) InstallSQL() string {
	return p.snapshotSQL(installTemplate, manualSnapshot, "")
}

// AutomaticDiscoverySQL opts an existing monitor into bounded workload discovery.
// The project owner runs it after InstallSQL. It preserves the private reader's grants.
func (p Project) AutomaticDiscoverySQL() string {
	return p.snapshotSQL(automaticDiscovery, automaticSnapshot, "OR current_setting('transaction_read_only') <> 'on'")
}

func (p Project) snapshotSQL(installation, reader, extraGuard string) string {
	snapshot := strings.NewReplacer("-- POOLTUNE_READER", reader, "-- POOLTUNE_EXTRA_GUARD", extraGuard).Replace(snapshotTemplate)
	installation = strings.Replace(installation, "-- POOLTUNE_SNAPSHOT", snapshot, 1)
	return strings.ReplaceAll(installation, "pooltune_monitor_role", p.Role())
}

//go:embed allow_lookup.sql
var allowLookupSQL string

//go:embed lookup_evidence.sql
var lookupEvidenceTemplate string

// LookupEvidenceSQL adds optional diagnosis permissions to an existing monitor.
func (p Project) LookupEvidenceSQL() string {
	return p.lookupSQL(lookupEvidenceTemplate)
}

//go:embed lookup_plan.sql
var lookupPlanTemplate string

// LookupPlanSQL adds the owner-operated measured sample to an installed lookup reader.
func (p Project) LookupPlanSQL() string {
	return p.lookupSQL(lookupPlanTemplate)
}

func (p Project) lookupSQL(template string) string {
	template = strings.Replace(template, "-- POOLTUNE_ALLOW_LOOKUP", allowLookupSQL, 1)
	return strings.ReplaceAll(template, "pooltune_monitor_role", p.Role())
}

//go:embed traffic_plan.sql
var trafficPlanTemplate string

// TrafficPlanSQL adds an optional authorization for reading actual traffic logs.
func (p Project) TrafficPlanSQL() string {
	return strings.ReplaceAll(trafficPlanTemplate, "pooltune_monitor_role", p.Role())
}
