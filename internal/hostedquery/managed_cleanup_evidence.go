package hostedquery

import (
	"strconv"
	"time"
)

// ManagedCleanupEvidence distinguishes a bounded trigger change from plain VACUUM.
// The observations come from the native table catalog, not the model.
type ManagedCleanupEvidence struct {
	Earlier         ProjectCleanupSettingsObservation `json:"earlier"`
	Later           ProjectCleanupSettingsObservation `json:"later"`
	NextScaleFactor float64                           `json:"next_scale_factor"`
}

func (c ManagedCleanupEvidence) Valid(table uint32, runID string, now time.Time) bool {
	a, b := c.Earlier, c.Later
	if table == 0 || b.Target.TableOID != table || b.Target.DatabaseOID == 0 || runID == "" || b.At.IsZero() || b.At.After(now) || now.Sub(b.At) > 30*time.Second ||
		!a.OnlyDeadTupleTrigger || !b.OnlyDeadTupleTrigger || !a.AutovacuumEnabled || !b.AutovacuumEnabled ||
		a.AutovacuumNaptimeSeconds != b.AutovacuumNaptimeSeconds || b.AutovacuumNaptimeSeconds <= 0 || b.AutovacuumNaptimeSeconds > maximumCleanupNaptime ||
		a.AutomaticCleanups != b.AutomaticCleanups || !a.LastAutovacuum.Equal(b.LastAutovacuum) || !grewOwnedCleanupBacklog(a, b) {
		return false
	}
	intent, err := planProjectCleanupSettings(ProjectCleanupSettingsRequest{RunID: runID, Earlier: a, Later: b}, b)
	if err != nil {
		return false
	}
	next, err := strconv.ParseFloat(intent.AfterOption, 64)
	return err == nil && c.NextScaleFactor == next
}
