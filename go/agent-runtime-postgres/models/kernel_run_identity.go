package models

// KernelRunIdentityRecord is immutable; an empty decision payload is the
// positive Create identity, never a second mutable execution status.
type KernelRunIdentityRecord struct {
	RunID        string `gorm:"size:64;primaryKey"`
	DecisionJSON string `gorm:"type:text;not null"`
}

func (KernelRunIdentityRecord) TableName() string { return "agent_kernel_run_identities" }
