package hostedquery

import (
	"errors"
	"time"
)

// ValidateDiagnosticProposal checks evidence eligibility only. This does not grant
// execution authority or replace each executor's current identity/load/grant checks.
func ValidateDiagnosticProposal(e IndexEvidence, p IndexProposal, now time.Time) error {
	if p.Outcome != "propose" || p.RunID != e.RunID || p.JobID != e.JobID || p.TableOID != e.TableOID || p.Column != e.Column || len(p.Reason) == 0 || len(p.Reason) > 1000 || e.RunID == "" || !ValidDiagnosticTarget(e.TableOID, e.Column, e.Columns, e.JobID) || e.Decision != Investigate || e.At.IsZero() || e.At.After(now) || now.Sub(e.At) > 30*time.Second {
		return errors.New("invalid diagnostic identity or freshness")
	}
	if e.JobID != 0 && (p.Repair != "cron" || e.ManagedCron == nil || e.ManagedCron.JobID != e.JobID) {
		return errors.New("job identity permits only its managed scheduling repair")
	}
	if len(e.Columns) > 0 && p.Repair != "" {
		return errors.New("compound enrollment permits only index repair")
	}
	eligible := false
	switch p.Repair {
	case "":
		return qualifyIndex(e, p, now)
	case "analyze":
		eligible = len(p.Columns) == 0 && supportsAnalyzeEvidence(e)
	case "statistics":
		eligible = e.Statistics.SupportsRepair(e.Existing) && (e.Statistics.ColumnTarget == nil || len(p.Columns) == 0)
	case "vacuum":
		eligible = qualifyVacuum(e, p, e.TableOID, now)
	case "memory":
		if e.ManagedMemory != nil {
			m := e.ManagedMemory
			eligible = len(e.Columns) == 0 && len(p.Columns) == 0 && e.Memory == nil && e.Analyze == nil && e.Statistics == nil && e.Vacuum == nil && e.Cron == nil && e.ManagedCron == nil && e.Plan.Node == "Sort" && e.Plan.ExaminedRows == m.SortRows && e.Plan.ReturnedRows == m.SortRows && m.Valid(now)
		} else {
			eligible = qualifyMemory(e, p, e.TableOID, now)
		}
	case "cron":
		if e.ManagedCron != nil {
			eligible = e.JobID > 0 && e.ManagedCron.JobID == e.JobID && len(p.Columns) == 0 && e.ManagedMemory == nil && e.Cron == nil && e.Analyze == nil && e.Statistics == nil && e.Vacuum == nil && e.Memory == nil && ManagedCronCause(*e.ManagedCron)
		} else {
			eligible = cronCause(e)
		}
	}
	if !eligible {
		return errors.New("diagnostic evidence does not support proposed repair")
	}
	return nil
}

// ValidDiagnosticTarget separates a scheduled job from a table/column enrollment.
func ValidDiagnosticTarget(table uint32, column int16, columns []int16, job int64) bool {
	if job != 0 {
		return job > 0 && job <= 9007199254740991 && table == 0 && column == 0 && len(columns) == 0
	}
	return table != 0 && ValidIndexColumns(column, columns)
}
