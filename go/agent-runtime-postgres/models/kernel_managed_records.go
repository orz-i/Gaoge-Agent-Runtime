package models

// Singleton identity is initialized by Migrate, never by a recovery attempt.
type KernelManagedAuthorityRecord struct {
	Key        string `gorm:"size:64;primaryKey"`
	Namespace  string `gorm:"size:64;not null"`
	Generation uint64 `gorm:"not null"`
	Version    uint32 `gorm:"not null"`
}

func (KernelManagedAuthorityRecord) TableName() string { return "agent_kernel_managed_authority" }

// One current index per Run. The referenced history owns the full payload.
// Payload is a legacy NOT NULL column, written as JSON null until AC03.
type KernelManagedTransferRecord struct {
	RunID        string `gorm:"size:64;primaryKey"`
	SuspensionID string `gorm:"size:128;not null;uniqueIndex"`
	RecoveryID   string `gorm:"size:128;not null;uniqueIndex"`
	Revision     uint64 `gorm:"not null"`
	Payload      string `gorm:"type:text;not null"`
}

func (KernelManagedTransferRecord) TableName() string { return "agent_kernel_managed_transfers" }

// Historical certificates are never overwritten by a later suspension.
type KernelManagedHistoryRecord struct {
	SuspensionID string `gorm:"size:128;primaryKey"`
	RecoveryID   string `gorm:"size:128;not null;uniqueIndex"`
	RunID        string `gorm:"size:64;not null;index"`
	Revision     uint64 `gorm:"not null"`
	Payload      string `gorm:"type:text;not null"`
}

func (KernelManagedHistoryRecord) TableName() string { return "agent_kernel_managed_history" }
