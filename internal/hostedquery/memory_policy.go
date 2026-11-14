package hostedquery

import (
	"math"
	"time"
)

const memoryFunction = "pooltune_memory_sum"
const memoryOriginal = "4MB"
const memoryCandidate = "32MB"
const memoryMinSpill = 8 * 1024 * 1024

type MemoryEvidence struct {
	FunctionOID        uint32  `json:"function_oid"`
	TempBytes          float64 `json:"temp_bytes"`
	InternalTempBytes  float64 `json:"internal_temp_bytes"`
	SortNodes          int     `json:"sort_nodes"`
	MaxConcurrentCalls int     `json:"max_concurrent_calls"`
	CandidateBytes     int64   `json:"candidate_bytes"`
	AggregateBytes     int64   `json:"aggregate_bytes"`
	CurrentWorkMem     string  `json:"current_work_mem"`
	CandidateWorkMem   string  `json:"candidate_work_mem"`
	IdentityVerified   bool    `json:"identity_verified"`
}
type MemorySample struct {
	AnalyzeSample
	TempBytes         float64 `json:"temp_bytes"`
	InternalTempBytes float64 `json:"internal_temp_bytes"`
	SortRows          int64   `json:"sort_rows"`
	ResultsCorrect    bool    `json:"results_correct"`
}
type MemoryReport struct {
	JobID           int64          `json:"job_id"`
	Fixture         string         `json:"fixture"`
	Function        MemoryIdentity `json:"function"`
	Before          MemorySample   `json:"before"`
	After           MemorySample   `json:"after"`
	Outcome         string         `json:"outcome"`
	Reason          string         `json:"reason"`
	Evidence        IndexEvidence  `json:"evidence"`
	Proposal        IndexProposal  `json:"proposal"`
	OriginalWorkMem string         `json:"original_work_mem"`
	AppliedWorkMem  string         `json:"applied_work_mem"`
	Restored        bool           `json:"restored"`
	BaselineMS      float64        `json:"baseline_wall_ms"`
	GraphMS         float64        `json:"investigation_wall_ms"`
	MemoryMS        float64        `json:"memory_wall_ms"`
	VerifyMS        float64        `json:"verify_wall_ms"`
}

func memoryCause(e IndexEvidence) bool {
	m := e.Memory
	return e.Decision == Investigate && m != nil && m.IdentityVerified && m.FunctionOID != 0 && e.Plan.Node == "Sort" && e.Plan.ExaminedRows == 100000 && e.Plan.ReturnedRows == 100000 && nonnegativeFinite(m.TempBytes) && nonnegativeFinite(m.InternalTempBytes) && m.TempBytes >= memoryMinSpill && m.InternalTempBytes >= memoryMinSpill && m.SortNodes == 1 && m.MaxConcurrentCalls == 3 && m.CandidateBytes == 32*1024*1024 && m.AggregateBytes == 96*1024*1024 && m.CurrentWorkMem == memoryOriginal && m.CandidateWorkMem == memoryCandidate
}
func qualifyMemory(e IndexEvidence, p IndexProposal, table uint32, now time.Time) bool {
	return memoryCause(e) && e.Analyze == nil && e.Statistics == nil && e.Vacuum == nil && table != 0 && e.TableOID == table && e.Column == 2 && e.RunID != "" && p.Outcome == "propose" && p.Repair == "memory" && p.RunID == e.RunID && p.TableOID == table && p.Column == 2 && len(p.Reason) > 0 && len(p.Reason) <= 1000 && !e.At.IsZero() && !e.At.After(now) && now.Sub(e.At) <= 30*time.Second
}
func verifyMemory(before, after MemorySample) (string, string) {
	for _, s := range []MemorySample{before, after} {
		if s.Reads != 10 || s.HealthyReads != 10 || s.Writes != 10 || s.SortRows != 100000 || !s.ResultsCorrect || !nonnegativeFinite(s.TempBytes) || !nonnegativeFinite(s.InternalTempBytes) || !nonnegativeFinite(s.ReadMS) || !nonnegativeFinite(s.ReadMAD) || !nonnegativeFinite(s.HealthyMS) || !nonnegativeFinite(s.HealthyMAD) || !nonnegativeFinite(s.WriteMS) || !nonnegativeFinite(s.WriteMAD) {
			return "inconclusive", "missing matched result, latency, spill or protected traffic evidence"
		}
	}
	for _, m := range []struct{ before, after, noise, relative, absolute float64 }{{before.ReadMS, after.ReadMS, math.Max(before.ReadMAD, after.ReadMAD), .2, 5}, {before.HealthyMS, after.HealthyMS, math.Max(before.HealthyMAD, after.HealthyMAD), .2, 5}, {before.WriteMS, after.WriteMS, math.Max(before.WriteMAD, after.WriteMAD), .1, 2}} {
		d := m.after - m.before
		if d >= m.absolute && d >= m.relative*m.before && d > 3*m.noise {
			return "regressed", "a measured read or write regressed"
		}
	}
	gain := before.ReadMS - after.ReadMS
	if before.TempBytes >= memoryMinSpill && before.InternalTempBytes >= memoryMinSpill && after.TempBytes <= before.TempBytes*.5 && after.InternalTempBytes <= before.InternalTempBytes*.5 && gain >= 5 && gain >= .2*before.ReadMS && gain > 3*math.Max(before.ReadMAD, after.ReadMAD) {
		return "improved_tested_inputs", "attributed spill and measured latency improved with no sampled regression"
	}
	return "inconclusive", "measured spill and latency did not meet all prespecified benefit gates"
}
