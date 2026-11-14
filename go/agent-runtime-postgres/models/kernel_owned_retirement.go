package models

// Durable fact for one genuinely retired Root-owned physical grant. It follows
// the original Run's retention boundary, and is not a lease or dispatch token.
type KernelOwnedRetirementRecord struct {
	GrantID         string `gorm:"size:128;primaryKey"`
	RunID           string `gorm:"size:64;not null;index"`
	SettledRevision uint64 `gorm:"not null"`
	Version         uint64 `gorm:"not null"`
	Phase           string `gorm:"size:32;not null"`
	Payload         string `gorm:"type:text;not null"`
}

func (KernelOwnedRetirementRecord) TableName() string { return "agent_kernel_owned_retirements" }
