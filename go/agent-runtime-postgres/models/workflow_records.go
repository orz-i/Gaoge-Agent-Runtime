package models

import "time"

// WorkflowDefinitionRevisionRecord stores one immutable compiled definition.
type WorkflowDefinitionRevisionRecord struct {
	BaseModel
	WorkflowID         string `gorm:"size:64;not null;uniqueIndex:uk_agent_workflow_revision,priority:1"`
	Revision           int    `gorm:"not null;uniqueIndex:uk_agent_workflow_revision,priority:2"`
	SchemaVersion      int    `gorm:"not null;default:1"`
	Scope              string `gorm:"size:16;not null;index:idx_agent_workflow_scope_owner,priority:1"`
	TenantID           string `gorm:"size:64;not null;default:'';index:idx_agent_workflow_scope_owner,priority:2"`
	OwnerActorID       string `gorm:"size:64;not null;default:'';index:idx_agent_workflow_scope_owner,priority:3"`
	Name               string `gorm:"size:128;not null;default:'';index:idx_agent_workflow_name"`
	Description        string `gorm:"type:text;not null;default:''"`
	Status             string `gorm:"size:32;not null;default:'active';index:idx_agent_workflow_scope_owner,priority:4"`
	InputSchemaJSON    string `gorm:"type:text;not null"`
	OutputSchemaJSON   string `gorm:"type:text;not null"`
	RootJSON           string `gorm:"type:text;not null"`
	LimitsJSON         string `gorm:"type:text;not null"`
	DependenciesJSON   string `gorm:"type:text;not null;default:'[]'"`
	DependencyHash     string `gorm:"size:64;not null"`
	DefinitionHash     string `gorm:"size:64;not null"`
	CreatedByTenantID  string `gorm:"size:64;not null;default:''"`
	CreatedByActorID   string `gorm:"size:64;not null;default:''"`
	RequestID          string `gorm:"size:64;not null;default:'';index:idx_agent_workflow_request"`
	RequestFingerprint string `gorm:"size:64;not null;default:''"`
	RevisionNote       string `gorm:"size:255;not null;default:''"`
}

func (WorkflowDefinitionRevisionRecord) TableName() string {
	return "agent_workflow_definition_revisions"
}

type WorkflowExecutionRecord struct {
	BaseModel
	RunID               string `gorm:"size:64;not null;uniqueIndex:uk_agent_workflow_execution_run"`
	WorkflowID          string `gorm:"size:64;not null;index:idx_agent_workflow_execution_definition,priority:1"`
	WorkflowRevision    int    `gorm:"not null;index:idx_agent_workflow_execution_definition,priority:2"`
	DefinitionHash      string `gorm:"size:64;not null"`
	DependencyHash      string `gorm:"size:64;not null"`
	RootRunID           string `gorm:"size:64;not null;index:idx_agent_workflow_execution_root"`
	BudgetOwnerRunID    string `gorm:"size:64;not null;index:idx_agent_workflow_execution_budget"`
	ParentRunID         string `gorm:"size:64;not null;default:'';index:idx_agent_workflow_execution_parent"`
	Depth               int    `gorm:"not null;default:0"`
	Version             int64  `gorm:"not null;default:1"`
	Status              string `gorm:"size:32;not null;index:idx_agent_workflow_execution_status"`
	StateJSON           string `gorm:"type:text;not null"`
	VarsJSON            string `gorm:"type:text;not null"`
	WaitsJSON           string `gorm:"type:text;not null"`
	CompensationJSON    string `gorm:"type:text;not null"`
	BudgetJSON          string `gorm:"type:text;not null"`
	EnvironmentSnapshot string `gorm:"type:text;not null"`
	WorkspaceSnapshot   string `gorm:"type:text;not null"`
	ThreadSnapshotHash  string `gorm:"size:64;not null"`
	CompletionSeq       int64  `gorm:"not null;default:0"`
	ErrorCode           string `gorm:"size:64;not null;default:''"`
	ErrorMessage        string `gorm:"size:255;not null;default:''"`
	StartedAt           time.Time
	EndedAt             *time.Time
}

func (WorkflowExecutionRecord) TableName() string { return "agent_workflow_executions" }

type RunResultRecord struct {
	BaseModel
	RunID         string `gorm:"size:64;not null;uniqueIndex:uk_agent_run_result_run"`
	RuntimeKind   string `gorm:"size:16;not null"`
	CanonicalJSON string `gorm:"type:text;not null"`
	Presentation  string `gorm:"type:text;not null;default:''"`
	SchemaHash    string `gorm:"size:64;not null"`
	ContentHash   string `gorm:"size:64;not null"`
}

func (RunResultRecord) TableName() string { return "agent_run_results" }

type WorkflowCacheEntryRecord struct {
	BaseModel
	CacheKey         string    `gorm:"size:64;not null;uniqueIndex:uk_agent_workflow_cache_key"`
	TenantID         string    `gorm:"size:64;not null;index:idx_agent_workflow_cache_actor,priority:1"`
	ActorID          string    `gorm:"size:64;not null;index:idx_agent_workflow_cache_actor,priority:2"`
	WorkflowID       string    `gorm:"size:64;not null"`
	WorkflowRevision string    `gorm:"size:64;not null"`
	NodeID           string    `gorm:"size:128;not null"`
	DependencyHash   string    `gorm:"size:64;not null"`
	SchemaHash       string    `gorm:"size:64;not null"`
	ContextHash      string    `gorm:"size:64;not null"`
	InputHash        string    `gorm:"size:64;not null"`
	ValueJSON        string    `gorm:"type:text;not null"`
	ContentHash      string    `gorm:"size:64;not null"`
	ExpiresAt        time.Time `gorm:"not null;index:idx_agent_workflow_cache_expiry"`
}

func (WorkflowCacheEntryRecord) TableName() string { return "agent_workflow_cache_entries" }
