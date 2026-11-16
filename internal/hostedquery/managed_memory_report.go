package hostedquery

type ManagedMemoryReport struct {
	FunctionOID    uint32                     `json:"function_oid"`
	Measurement    string                     `json:"measurement"`
	Before         ManagedMemorySample        `json:"before"`
	After          *ManagedMemorySample       `json:"after,omitempty"`
	BeforeTraffic  *ManagedMaintenanceTraffic `json:"before_traffic,omitempty"`
	AfterTraffic   *ManagedMaintenanceTraffic `json:"after_traffic,omitempty"`
	Evidence       ManagedMemoryEvidence      `json:"evidence"`
	Outcome        string                     `json:"outcome"`
	Reason         string                     `json:"reason"`
	Restored       bool                       `json:"restored"`
	RecoveryStatus string                     `json:"recovery_status,omitempty"`
}
