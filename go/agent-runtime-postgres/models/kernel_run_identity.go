package models

// KernelRunIdentityRecord is immutable; an empty cancellation payload is the
// positive Create identity, never a second mutable execution status.
type KernelRunIdentityRecord struct {
	RunID            string `gorm:"size:64;primaryKey"`
	CancellationJSON string `gorm:"type:text;not null"`
}

func (KernelRunIdentityRecord) TableName() string { return "agent_kernel_run_identities" }
