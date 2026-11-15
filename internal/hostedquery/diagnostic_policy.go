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
	eligible := false
	switch p.Repair {
	case "":
		return qualifyIndex(e, p, now)
	case "analyze":
		eligible = qualifyAnalyze(AnalyzeReport{Evidence: e, Proposal: p}, e.TableOID, now)
		if eligible {
			a := e.Analyze
			eligible = e.Plan.Node == "Seq Scan" && e.Plan.ExaminedRows > 0 && a.ActualRows == e.Plan.ReturnedRows && a.SampledValueFraction != nil && *a.SampledValueFraction/(float64(a.ActualRows)/float64(e.Plan.ExaminedRows)) >= 10
		}
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
