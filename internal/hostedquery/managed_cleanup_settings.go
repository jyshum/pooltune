package hostedquery

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"pooltune/internal/supabaseproject"
)

// Sampled values stay in PostgreSQL or executor memory. The private keyed
// digest only permits matching a resampled value after a worker restart.
func managedCleanupInputDigest(secret string, target ProjectIndexTarget, values []int32) string {
	h := hmac.New(sha256.New, []byte(secret))
	raw, _ := json.Marshal(struct {
		Target ProjectIndexTarget
		Values []int32
	}{target, values})
	h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}

type ManagedCleanupReport struct {
	Trigger       CleanupTriggerReport       `json:"cleanup_trigger"`
	Before        ManagedIndexMeasurement    `json:"before"`
	After         *ManagedIndexMeasurement   `json:"after,omitempty"`
	BeforeTraffic *ManagedMaintenanceTraffic `json:"before_traffic,omitempty"`
	AfterTraffic  *ManagedMaintenanceTraffic `json:"after_traffic,omitempty"`
	Outcome       string                     `json:"outcome"`
	Reason        string                     `json:"reason"`
	Measurement   string                     `json:"measurement"`
}

type managedCleanupAction struct {
	Version         int
	RunID           string
	Intent          ProjectCleanupSettingsIntent
	Observed        supabaseproject.DiscoveredIndexEvidence
	Earlier, Before ProjectCleanupSettingsObservation
	Receipt         *ProjectCleanupSettingsObservation
	Restore         *ProjectCleanupSettingsIntent
	Deadline        time.Time
	InputDigest     string
	Sample          ManagedIndexMeasurement
	ControlMeans    map[int64]float64
	ControlCounters map[int64]managedControlCounter
	Controls        ManagedMaintenanceTraffic
	Evidence        IndexEvidence
	Proposal        IndexProposal
}

func (a managedCleanupAction) owned() OwnedCleanupSettingsAction {
	out := OwnedCleanupSettingsAction{Intent: a.Intent, Earlier: a.Earlier, Before: a.Before, VerificationEnds: a.Deadline}
	if a.Receipt != nil {
		out.Receipt = *a.Receipt
	}
	return out
}
func (a managedCleanupAction) controls() *managedTrafficControls {
	return &managedTrafficControls{ReadMS: a.Controls.ReadMeanMS, WriteMS: a.Controls.WriteMeanMS, ReadCalls: a.Controls.ReadCalls, WriteCalls: a.Controls.WriteCalls, means: a.ControlMeans, counters: a.ControlCounters}
}
func (a managedCleanupAction) report(outcome, reason string) *ManagedCleanupReport {
	r := &ManagedCleanupReport{Before: a.Sample, BeforeTraffic: &a.Controls, Outcome: outcome, Reason: reason, Measurement: "same_sampled_input_and_normal_database_traffic", Trigger: CleanupTriggerReport{BeforeOption: a.Intent.BeforeOption, AfterOption: a.Intent.AfterOption, Earlier: a.Earlier, Before: a.Before}}
	if a.Receipt != nil {
		r.Trigger.After = *a.Receipt
		r.Trigger.SettingChanged = true
	}
	return r
}
func validManagedCleanupAction(a managedCleanupAction, claim investigationClaim) error {
	if a.Version != 2 || a.RunID == "" || a.RunID != a.Intent.RunID || a.Intent.Restoring || a.Intent.Target.DatabaseOID != claim.Database || a.Observed.DatabaseOID != claim.Database || a.Observed.RoleOID != claim.Role || a.Observed.QueryID != claim.Query || a.Observed.TableOID != a.Intent.Target.TableOID || len(a.Observed.Columns) != 1 || len(a.InputDigest) != 64 || a.Before.Target != a.Intent.Target || a.Earlier.Target != a.Intent.Target || !a.Before.OnlyDeadTupleTrigger || !a.Before.AutovacuumEnabled || a.Before.AutovacuumNaptimeSeconds <= 0 || a.Before.AutovacuumNaptimeSeconds > maximumCleanupNaptime || !a.Deadline.Equal(a.Before.At.Add(2*ownedCleanupNaptime(a.Before)+20*time.Second)) || len(a.ControlMeans) == 0 || len(a.ControlMeans) != len(a.ControlCounters) {
		return errors.New("recorded managed cleanup identity is inconsistent")
	}
	if a.Evidence.Vacuum == nil || a.Evidence.Vacuum.CleanupTrigger == nil || a.Evidence.TableOID != a.Intent.Target.TableOID || a.Evidence.Column != a.Observed.Columns[0] || a.Evidence.RunID != a.RunID || a.Proposal.Repair != "vacuum" || ValidateDiagnosticProposal(a.Evidence, a.Proposal, a.Evidence.At) != nil || !sameManagedCleanupObservation(a.Evidence.Vacuum.CleanupTrigger.Earlier, a.Earlier) || !sameManagedCleanupObservation(a.Evidence.Vacuum.CleanupTrigger.Later, a.Before) {
		return errors.New("recorded cleanup decision does not match the measured trigger")
	}
	planned, err := planProjectCleanupSettings(ProjectCleanupSettingsRequest{RunID: a.RunID, Earlier: a.Earlier, Later: a.Before}, a.Before)
	if err != nil || planned != a.Intent {
		return errors.New("recorded cleanup change was not justified by its backlog")
	}
	if a.Receipt != nil && !validOwnedCleanupReceipt(a.owned(), *a.Receipt) {
		return errors.New("recorded cleanup acknowledgement is inconsistent")
	}
	if a.Restore != nil && !reversesOwnedCleanup(a.Intent, *a.Restore) {
		return errors.New("recorded cleanup restoration changed its scope")
	}
	return nil
}

func sameManagedCleanupObservation(a, b ProjectCleanupSettingsObservation) bool {
	if !a.At.Equal(b.At) || !a.Boot.Equal(b.Boot) || !a.StatsReset.Equal(b.StatsReset) || !a.LastAutovacuum.Equal(b.LastAutovacuum) {
		return false
	}
	a.At, a.Boot, a.StatsReset, a.LastAutovacuum = b.At, b.Boot, b.StatsReset, b.LastAutovacuum
	return a == b
}
