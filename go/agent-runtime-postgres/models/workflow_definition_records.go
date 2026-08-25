package models

import "time"

// WorkflowDefinitionRevisionRecord stores one immutable compiled Definition revision.
type WorkflowDefinitionRevisionRecord struct {
	ScopeKind          string    `gorm:"size:16;primaryKey;uniqueIndex:idx_workflow_definition_request,priority:1"`
	TenantID           string    `gorm:"size:64;primaryKey;uniqueIndex:idx_workflow_definition_request,priority:2"`
	ActorID            string    `gorm:"size:64;primaryKey;uniqueIndex:idx_workflow_definition_request,priority:3"`
	DefinitionID       string    `gorm:"size:128;primaryKey"`
	Revision           int       `gorm:"primaryKey;autoIncrement:false"`
	DefinitionJSON     string    `gorm:"type:text;not null"`
	DefinitionHash     string    `gorm:"size:64;not null"`
	PublishedBy        string    `gorm:"size:128;not null"`
	IdempotencyKey     string    `gorm:"size:128;not null;uniqueIndex:idx_workflow_definition_request,priority:4"`
	RequestFingerprint string    `gorm:"size:64;not null"`
	PublishedAt        time.Time `gorm:"not null;index:idx_workflow_definition_published"`
}

func (WorkflowDefinitionRevisionRecord) TableName() string {
	return "agent_workflow_definition_revisions"
}

// WorkflowDefinitionHeadRecord stores the CAS-controlled active/latest pointer.
type WorkflowDefinitionHeadRecord struct {
	ScopeKind      string    `gorm:"size:16;primaryKey"`
	TenantID       string    `gorm:"size:64;primaryKey"`
	ActorID        string    `gorm:"size:64;primaryKey"`
	DefinitionID   string    `gorm:"size:128;primaryKey"`
	LatestRevision int       `gorm:"not null"`
	ActiveRevision int       `gorm:"not null;default:0"`
	Availability   string    `gorm:"size:16;not null"`
	Version        uint64    `gorm:"not null"`
	UpdatedAt      time.Time `gorm:"not null"`
}

func (WorkflowDefinitionHeadRecord) TableName() string {
	return "agent_workflow_definition_heads"
}
