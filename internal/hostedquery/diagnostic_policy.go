package hostedquery

import (
	"errors"
	"time"
)

// ValidateDiagnosticProposal checks evidence eligibility only. This does not grant
// execution authority or replace each executor's current identity/load/grant checks.
func ValidateDiagnosticProposal(e IndexEvidence, p IndexProposal, now time.Time) error {
	if p.Outcome != "propose" || p.RunID != e.RunID || p.TableOID != e.TableOID || p.Column != e.Column || len(p.Reason) == 0 || len(p.Reason) > 1000 || e.RunID == "" || e.TableOID == 0 || e.Column <= 0 || e.Decision != Investigate || e.At.IsZero() || e.At.After(now) || now.Sub(e.At) > 30*time.Second {
		return errors.New("invalid diagnostic identity or freshness")
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
		s := e.Statistics
		eligible = s != nil && s.Correlated && len(s.Cases) == 2
		if eligible {
			for _, c := range s.Cases {
				if c.ActualRows <= 0 || c.EstimatedRows <= 0 || rowEstimateError(c.EstimatedRows, c.ActualRows) < 10 {
					eligible = false
				}
			}
		}
	case "vacuum":
		eligible = qualifyVacuum(e, p, e.TableOID, now)
	case "memory":
		eligible = qualifyMemory(e, p, e.TableOID, now)
	case "cron":
		eligible = cronCause(e)
	}
	if !eligible {
		return errors.New("diagnostic evidence does not support proposed repair")
	}
	return nil
}
