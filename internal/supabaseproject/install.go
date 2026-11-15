package supabaseproject

import (
	_ "embed"
	"strings"
)

//go:embed install.sql
var installTemplate string

// InstallSQL is run by the project owner. The derived identifier contains only
// lowercase ASCII letters, numbers and underscores. The script contains no secret.
func (p Project) InstallSQL() string {
	return strings.ReplaceAll(installTemplate, "pooltune_monitor_role", p.Role())
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
