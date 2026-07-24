package models

import "time"

// AgentManifestRevisionRecord stores one immutable tenant-visible Agent
// Manifest revision. Updates append a new row rather than mutating history.
type AgentManifestRevisionRecord struct {
	BaseModel
	ManifestID         string `gorm:"size:64;not null;uniqueIndex:uk_agent_manifest_revision,priority:2"`
	Revision           int    `gorm:"not null;uniqueIndex:uk_agent_manifest_revision,priority:3"`
	TenantID           string `gorm:"size:64;not null;uniqueIndex:uk_agent_manifest_revision,priority:1;index:idx_agent_manifest_tenant_status,priority:1"`
	Name               string `gorm:"size:128;not null;default:'';index:idx_agent_manifest_name"`
	Description        string `gorm:"type:text;not null;default:''"`
	Instructions       string `gorm:"type:text;not null;default:''"`
	Status             string `gorm:"size:32;not null;default:'active';index:idx_agent_manifest_tenant_status,priority:2"`
	ModelName          string `gorm:"size:128;not null;default:''"`
	ExecutionMode      string `gorm:"size:32;not null;default:''"`
	ToolKeysJSON       string `gorm:"type:text;not null;default:'[]'"`
	SkillRefsJSON      string `gorm:"type:text;not null;default:'[]'"`
	MaxChildRuns       int    `gorm:"not null;default:1"`
	MaxDepth           int    `gorm:"not null;default:1"`
	CreatedByTenantID  string `gorm:"size:64;not null;default:''"`
	CreatedByActorID   string `gorm:"size:64;not null;default:''"`
	RequestID          string `gorm:"size:64;not null;default:'';index:idx_agent_manifest_request,priority:2"`
	RequestFingerprint string `gorm:"size:64;not null;default:''"`
	RevisionNote       string `gorm:"size:255;not null;default:''"`
}

func (AgentManifestRevisionRecord) TableName() string { return "agent_manifest_revisions" }

// RunHandoffRecord persists one replay-safe parent-to-child delegation.
type RunHandoffRecord struct {
	BaseModel
	HandoffID             string `gorm:"size:64;not null;uniqueIndex:uk_agent_run_handoff"`
	ClientHandoffID       string `gorm:"size:64;not null;uniqueIndex:uk_agent_run_handoff_client,priority:3"`
	RequestFingerprint    string `gorm:"size:64;not null"`
	TenantID              string `gorm:"size:64;not null;uniqueIndex:uk_agent_run_handoff_client,priority:1;index:idx_agent_run_handoff_actor,priority:1"`
	ActorID               string `gorm:"size:64;not null;uniqueIndex:uk_agent_run_handoff_client,priority:2;index:idx_agent_run_handoff_actor,priority:2"`
	RootRunID             string `gorm:"size:64;not null;index:idx_agent_run_handoff_root"`
	ParentRunID           string `gorm:"size:64;not null;index:idx_agent_run_handoff_parent"`
	ChildRunID            string `gorm:"size:64;not null;uniqueIndex:uk_agent_run_handoff_child"`
	AgentManifestID       string `gorm:"size:64;not null"`
	AgentManifestRevision string `gorm:"size:64;not null"`
	AgentName             string `gorm:"size:128;not null;default:''"`
	Goal                  string `gorm:"type:text;not null;default:''"`
	Status                string `gorm:"size:32;not null;default:'queued';index:idx_agent_run_handoff_status"`
	Depth                 int    `gorm:"not null;default:1"`
	InputProjectionKind   string `gorm:"size:64;not null;default:''"`
	InputProjectionID     string `gorm:"size:128;not null;default:''"`
	ResultSummary         string `gorm:"type:text;not null;default:''"`
	ResultOutputIDsJSON   string `gorm:"type:text;not null;default:'[]'"`
	ErrorCode             string `gorm:"size:64;not null;default:''"`
	ErrorMessage          string `gorm:"size:255;not null;default:''"`
	CompletedAt           *time.Time
}

func (RunHandoffRecord) TableName() string { return "agent_run_handoffs" }

// RunHandoffJoinRecord persists one immutable selection of sibling handoffs
// and its monotonic fan-in decision state.
type RunHandoffJoinRecord struct {
	BaseModel
	JoinID               string `gorm:"size:64;not null;uniqueIndex:uk_agent_run_handoff_join"`
	ClientJoinID         string `gorm:"size:64;not null;uniqueIndex:uk_agent_run_handoff_join_client,priority:3"`
	RequestFingerprint   string `gorm:"size:64;not null"`
	TenantID             string `gorm:"size:64;not null;uniqueIndex:uk_agent_run_handoff_join_client,priority:1;index:idx_agent_run_handoff_join_actor,priority:1"`
	ActorID              string `gorm:"size:64;not null;uniqueIndex:uk_agent_run_handoff_join_client,priority:2;index:idx_agent_run_handoff_join_actor,priority:2"`
	RootRunID            string `gorm:"size:64;not null;index:idx_agent_run_handoff_join_root"`
	ParentRunID          string `gorm:"size:64;not null;index:idx_agent_run_handoff_join_parent_status,priority:1"`
	HandoffIDsJSON       string `gorm:"type:text;not null;default:'[]'"`
	ResumeCheckpointID   string `gorm:"size:64;not null;default:'';index:idx_agent_run_handoff_join_checkpoint"`
	Mode                 string `gorm:"size:32;not null"`
	Quorum               int    `gorm:"not null;default:1"`
	FailurePolicy        string `gorm:"size:32;not null"`
	Status               string `gorm:"size:32;not null;default:'pending';index:idx_agent_run_handoff_join_parent_status,priority:2"`
	CompletedCount       int    `gorm:"not null;default:0"`
	FailedCount          int    `gorm:"not null;default:0"`
	CancelledCount       int    `gorm:"not null;default:0"`
	PendingCount         int    `gorm:"not null;default:0"`
	ResultHandoffIDsJSON string `gorm:"type:text;not null;default:'[]'"`
	ErrorCode            string `gorm:"size:64;not null;default:''"`
	ErrorMessage         string `gorm:"size:255;not null;default:''"`
	ResolvedAt           *time.Time
}

func (RunHandoffJoinRecord) TableName() string { return "agent_run_handoff_joins" }
