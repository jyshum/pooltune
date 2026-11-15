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
