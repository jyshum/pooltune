package hostedquery

import (
	"math"
	"time"
)

type VacuumEvidence struct {
	HeapFetches     float64 `json:"heap_fetches"`
	ScanRows        int64   `json:"scan_rows"`
	ConditionsClear bool    `json:"conditions_clear"`
}
type VacuumSample struct {
	AnalyzeSample
	HeapFetches float64 `json:"heap_fetches"`
	ScanRows    int64   `json:"scan_rows"`
}
type VacuumReport struct {
	CleanupTrigger *CleanupTriggerReport `json:"cleanup_trigger,omitempty"`
	JobID          int64                 `json:"job_id"`
	Fixture        string                `json:"fixture"`
	IndexOID       uint32                `json:"index_oid"`
	Before         VacuumSample          `json:"before"`
	After          VacuumSample          `json:"after"`
	Outcome        string                `json:"outcome"`
	Reason         string                `json:"reason"`
	Evidence       IndexEvidence         `json:"evidence"`
	Proposal       IndexProposal         `json:"proposal"`
	BaselineMS     float64               `json:"baseline_wall_ms"`
	GraphMS        float64               `json:"investigation_wall_ms"`
	VacuumMS       float64               `json:"vacuum_wall_ms"`
	VerifyMS       float64               `json:"verify_wall_ms"`
}

func vacuumCause(e IndexEvidence) bool {
	v := e.Vacuum
	return e.Decision == Investigate && e.Existing && e.Plan.Node == "Index Only Scan" && v != nil && v.ConditionsClear &&
		nonnegativeFinite(v.HeapFetches) && v.HeapFetches >= 1000 && v.ScanRows > 0 && v.HeapFetches/float64(v.ScanRows) >= .5
}
func qualifyVacuum(e IndexEvidence, p IndexProposal, table uint32, now time.Time) bool {
	return vacuumCause(e) && e.Analyze == nil && e.Statistics == nil && table != 0 && e.TableOID == table && e.Column == 2 && e.RunID != "" &&
		p.Outcome == "propose" && p.Repair == "vacuum" && p.RunID == e.RunID && p.TableOID == table && p.Column == 2 && len(p.Reason) > 0 && len(p.Reason) <= 1000 &&
		!e.At.IsZero() && !e.At.After(now) && now.Sub(e.At) <= 30*time.Second
}
func verifyVacuum(before, after VacuumSample) (string, string) {
	for _, s := range []VacuumSample{before, after} {
		if s.Reads != 10 || s.HealthyReads != 10 || s.Writes != 10 || s.ScanRows <= 0 ||
			!nonnegativeFinite(s.HeapFetches) || !nonnegativeFinite(s.ReadMS) || !nonnegativeFinite(s.ReadMAD) ||
			!nonnegativeFinite(s.HealthyMS) || !nonnegativeFinite(s.HealthyMAD) || !nonnegativeFinite(s.WriteMS) || !nonnegativeFinite(s.WriteMAD) {
			return "inconclusive", "missing matched read, write or visibility evidence"
		}
	}
	for _, m := range []struct{ before, after, noise, relative, absolute float64 }{
		{before.ReadMS, after.ReadMS, math.Max(before.ReadMAD, after.ReadMAD), .2, 5},
		{before.HealthyMS, after.HealthyMS, math.Max(before.HealthyMAD, after.HealthyMAD), .2, 5},
		{before.WriteMS, after.WriteMS, math.Max(before.WriteMAD, after.WriteMAD), .1, 2},
	} {
		d := m.after - m.before
		if d >= m.absolute && d >= m.relative*m.before && d > 3*m.noise {
			return "regressed", "a measured read or write regressed"
		}
	}
	gain := before.ReadMS - after.ReadMS
	mechanism := before.ScanRows == after.ScanRows && before.HeapFetches >= 1000 && before.HeapFetches/float64(before.ScanRows) >= .5 && after.HeapFetches <= before.HeapFetches*.5
	if mechanism && gain >= 5 && gain >= .2*before.ReadMS && gain > 3*math.Max(before.ReadMAD, after.ReadMAD) {
		return "improved_tested_inputs", "visibility work and measured query latency improved with no sampled regression"
	}
	return "inconclusive", "measured visibility and latency did not meet all prespecified benefit gates"
}
