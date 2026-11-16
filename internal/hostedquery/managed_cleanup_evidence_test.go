package hostedquery

import (
	"testing"
	"time"
)

func TestManagedCleanupDiagnosticRequiresActualTriggerEvidence(t *testing.T) {
	a, _ := managedCleanupExample()
	now := a.Before.At.Add(time.Second)
	trigger := ManagedCleanupEvidence{Earlier: a.Earlier, Later: a.Before, NextScaleFactor: .15}
	e := IndexEvidence{RunID: a.RunID, TableOID: a.Intent.Target.TableOID, Column: 1, At: now, Decision: Investigate, Existing: true, Plan: PlanEvidence{Node: "Index Only Scan"}, Vacuum: &VacuumEvidence{HeapFetches: 2000, ScanRows: 3000, ConditionsClear: true, CleanupTrigger: &trigger}}
	p := IndexProposal{RunID: e.RunID, TableOID: e.TableOID, Column: 1, Outcome: "propose", Repair: "vacuum", Reason: "Growing backlog is above the bounded next cleanup threshold."}
	if err := ValidateDiagnosticProposal(e, p, now); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*ManagedCleanupEvidence){
		"invented threshold": func(c *ManagedCleanupEvidence) { c.NextScaleFactor = .01 },
		"no growth":          func(c *ManagedCleanupEvidence) { c.Later.DeadTuples = c.Earlier.DeadTuples },
		"no writes":          func(c *ManagedCleanupEvidence) { c.Later.Updated = c.Earlier.Updated },
		"different table":    func(c *ManagedCleanupEvidence) { c.Earlier.Target.TableOID++; c.Later.Target.TableOID++ },
		"insert trigger":     func(c *ManagedCleanupEvidence) { c.Later.OnlyDeadTupleTrigger = false },
		"stale": func(c *ManagedCleanupEvidence) {
			c.Earlier.At = c.Earlier.At.Add(-time.Minute)
			c.Later.At = c.Later.At.Add(-time.Minute)
		},
		"changed generation": func(c *ManagedCleanupEvidence) { c.Later.Boot = c.Later.Boot.Add(time.Second) },
		"naptime":            func(c *ManagedCleanupEvidence) { c.Later.AutovacuumNaptimeSeconds = 121 },
	} {
		t.Run(name, func(t *testing.T) {
			bad := trigger
			change(&bad)
			v := *e.Vacuum
			v.CleanupTrigger = &bad
			badE := e
			badE.Vacuum = &v
			if ValidateDiagnosticProposal(badE, p, now) == nil {
				t.Fatal("unsupported trigger repair accepted")
			}
		})
	}
}
