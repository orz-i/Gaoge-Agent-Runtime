package models

// Singleton identity is initialized by Migrate, never by a recovery attempt.
type KernelManagedAuthorityRecord struct {
	Key        string `gorm:"size:64;primaryKey"`
	Namespace  string `gorm:"size:64;not null"`
	Generation uint64 `gorm:"not null"`
	Version    uint32 `gorm:"not null"`
}

func (KernelManagedAuthorityRecord) TableName() string { return "agent_kernel_managed_authority" }

// Historical certificates are never overwritten by a later suspension.
type KernelManagedHistoryRecord struct {
	SuspensionID string `gorm:"size:128;primaryKey"`
	RecoveryID   string `gorm:"size:128;not null;uniqueIndex"`
	RunID        string `gorm:"size:64;not null;uniqueIndex:idx_managed_history_run_revision"`
	Revision     uint64 `gorm:"not null;uniqueIndex:idx_managed_history_run_revision"`
	Payload      string `gorm:"type:text;not null"`
}

func (KernelManagedHistoryRecord) TableName() string { return "agent_kernel_managed_history" }
