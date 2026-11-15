package supabaseproject

import (
	_ "embed"
	"strings"
)

//go:embed discovered_lookup.sql
var discoveredLookupTemplate string

func (p Project) discoveredLookupSQL() string {
	return strings.ReplaceAll(discoveredLookupTemplate, "pooltune_monitor_role", p.Role())
}
