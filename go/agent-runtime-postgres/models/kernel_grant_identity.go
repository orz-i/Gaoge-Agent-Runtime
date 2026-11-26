package models

// A unique, permanent identity reservation independent of ancestor fence rows.
type KernelGrantIdentityRecord struct {
	GrantID string `gorm:"size:128;primaryKey"`
	RunID   string `gorm:"size:64;not null"`
}

func (KernelGrantIdentityRecord) TableName() string { return "agent_kernel_grant_identities" }
