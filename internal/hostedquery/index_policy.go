package hostedquery

import (
	"errors"
	"slices"
	"time"
)

// IndexEvidence is a bounded snapshot of an enrolled synthetic lookup.
type IndexEvidence struct {
	RunID       string              `json:"run_id"`
	At          time.Time           `json:"observed_at"`
	Decision    Decision            `json:"decision"`
	TableOID    uint32              `json:"table_oid"`
	Columns     []int16             `json:"column_numbers,omitempty"`
	Column      int16               `json:"column_number"`
	Existing    bool                `json:"covering_index"`
	Cron        *CronEvidence       `json:"cron,omitempty"`
	Memory      *MemoryEvidence     `json:"memory,omitempty"`
	Vacuum      *VacuumEvidence     `json:"vacuum,omitempty"`
	Statistics  *StatisticsEvidence `json:"statistics,omitempty"`
	Analyze     *AnalyzeEvidence    `json:"analyze,omitempty"`
	Plan        PlanEvidence        `json:"plan"`
	ExecutionMS float64             `json:"-"`
	Label       string              `json:"lookup_column"`
}

// IndexProposal refers to evidence by identity. Only Go resolves identifiers.
type ModelCall struct {
	Model        string  `json:"model"`
	DurationMS   float64 `json:"duration_ms"`
	Requests     int     `json:"requests"`
	InputTokens  *int    `json:"input_tokens,omitempty"`
	OutputTokens *int    `json:"output_tokens,omitempty"`
}

type IndexProposal struct {
	Diagnostic *DiagnosticTrace `json:"diagnostic,omitempty"`
	ModelCall  *ModelCall       `json:"model_call,omitempty"`
	Repair     string           `json:"repair,omitempty"`
	Outcome    string           `json:"outcome"`
	RunID      string           `json:"run_id"`
	TableOID   uint32           `json:"table_oid"`
	Columns    []int16          `json:"column_numbers,omitempty"`
	Column     int16            `json:"column_number"`
	Reason     string           `json:"reason"`
}

func qualifyIndex(e IndexEvidence, p IndexProposal, now time.Time) error {
	switch {
	case e.Decision != Investigate:
		return errors.New("lookup did not trigger investigation")
	case e.At.IsZero() || e.At.After(now) || now.Sub(e.At) > 30*time.Second:
		return errors.New("index evidence expired")
	case p.Repair != "" || p.Outcome != "propose" || e.RunID == "" || p.RunID != e.RunID ||
		e.TableOID == 0 || p.TableOID != e.TableOID || e.Column <= 0 || p.Column != e.Column ||
		len(p.Reason) == 0 || len(p.Reason) > 1000:
		return errors.New("proposal does not match the enrolled lookup and evidence")
	case !ValidIndexColumns(e.Column, e.Columns) || !slices.Equal(e.Columns, p.Columns):
		return errors.New("proposal changed ordered index keys")
	case e.Existing:
		return errors.New("a covering index is already present")
	case e.Plan.Node != "Seq Scan" || e.Plan.ExaminedRows < 10_000 ||
		e.Plan.ReturnedRows <= 0 || e.Plan.ExaminedRows/e.Plan.ReturnedRows < 10:
		return errors.New("plan does not show a selective sequential scan")
	}
	return nil
}

// ValidIndexColumns bounds the currently enrolled scalar and compound shapes.
func ValidIndexColumns(column int16, columns []int16) bool {
	return column > 0 && (len(columns) == 0 || (column == 2 && slices.Equal(columns, []int16{2, 3})))
}
