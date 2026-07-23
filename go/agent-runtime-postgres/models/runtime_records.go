package models

import (
	"time"

	"gorm.io/gorm"
)

// BaseModel contains only private Agent Runtime persistence metadata.
type BaseModel struct {
	ID        uint `gorm:"primaryKey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

// RunRecord is the Agent Runtime run aggregate. Host ownership is represented
// exclusively by opaque SDK refs; no Conversation database key is persisted.
type RunRecord struct {
	BaseModel
	RunID                    string    `gorm:"size:64;not null;default:'';uniqueIndex:idx_agent_runs_run_id"`
	RequestID                string    `gorm:"size:64;not null;default:'';index:idx_agent_runs_request_id"`
	TenantID                 string    `gorm:"size:64;not null;default:'default';index:idx_agent_runs_actor,priority:1"`
	ActorID                  string    `gorm:"size:64;not null;default:'';index:idx_agent_runs_actor,priority:2"`
	ThreadKind               string    `gorm:"size:64;not null;default:'';index:idx_agent_runs_thread,priority:1"`
	ThreadID                 string    `gorm:"size:128;not null;default:'';index:idx_agent_runs_thread,priority:2"`
	InputProjectionKind      string    `gorm:"size:64;not null;default:''"`
	InputProjectionID        string    `gorm:"size:128;not null;default:''"`
	OutputProjectionKind     string    `gorm:"size:64;not null;default:''"`
	OutputProjectionID       string    `gorm:"size:128;not null;default:''"`
	EnvironmentKind          string    `gorm:"size:64;not null;default:''"`
	EnvironmentID            string    `gorm:"size:128;not null;default:''"`
	EnvironmentRevision      string    `gorm:"size:64;not null;default:''"`
	Goal                     string    `gorm:"type:text;not null;default:''"`
	RunConfigSnapshotJSON    string    `gorm:"type:text;not null;default:''"`
	RequestFingerprint       string    `gorm:"size:64;not null;default:'';index:idx_agent_runs_request_fingerprint"`
	CurrentStepID            string    `gorm:"size:64;not null;default:'';index:idx_agent_runs_current_step_id"`
	CurrentPlanID            string    `gorm:"size:64;not null;default:'';index:idx_agent_runs_current_plan_id"`
	PendingInteractionID     string    `gorm:"size:64;not null;default:'';index:idx_agent_runs_pending_interaction_id"`
	StatusReason             string    `gorm:"size:255;not null;default:''"`
	LastEventSeq             int64     `gorm:"not null;default:0"`
	LastPresentationEventSeq int64     `gorm:"not null;default:0"`
	StartedBy                string    `gorm:"size:32;not null;default:''"`
	Endpoint                 string    `gorm:"size:32;not null;default:'';index:idx_agent_runs_endpoint"`
	Provider                 string    `gorm:"size:32;not null;default:'';index:idx_agent_runs_provider"`
	ProviderProtocol         string    `gorm:"size:64;not null;default:'';index:idx_agent_runs_provider_protocol"`
	UpstreamName             string    `gorm:"size:128;not null;default:''"`
	RequestedModelName       string    `gorm:"size:128;not null;default:'';index:idx_agent_runs_requested_model_name"`
	PlatformModelName        string    `gorm:"size:128;not null;default:'';index:idx_agent_runs_platform_model_name"`
	RoutedBindingCode        string    `gorm:"size:64;not null;default:'';index:idx_agent_runs_routed_binding_code"`
	ModelVendor              string    `gorm:"size:64;not null;default:''"`
	ModelIcon                string    `gorm:"size:64;not null;default:''"`
	UpstreamModelName        string    `gorm:"size:256;not null;default:''"`
	InputTokens              int64     `gorm:"not null;default:0"`
	OutputTokens             int64     `gorm:"not null;default:0"`
	CacheReadTokens          int64     `gorm:"not null;default:0"`
	CacheWriteTokens         int64     `gorm:"not null;default:0"`
	ReasoningTokens          int64     `gorm:"not null;default:0"`
	LLMCallsCount            int       `gorm:"not null;default:0"`
	ToolCallsCount           int       `gorm:"not null;default:0"`
	BilledCurrency           string    `gorm:"size:16;not null;default:''"`
	BilledNanousd            int64     `gorm:"not null;default:0"`
	LastBillingSnapshotJSON  string    `gorm:"type:text;not null;default:''"`
	FirstTokenLatencyMS      int64     `gorm:"not null;default:0"`
	TotalLatencyMS           int64     `gorm:"not null;default:0"`
	Status                   string    `gorm:"size:32;not null;default:'';index:idx_agent_runs_status"`
	ErrorCode                string    `gorm:"size:64;not null;default:''"`
	ErrorMessage             string    `gorm:"size:255;not null;default:''"`
	StartedAt                time.Time `gorm:"not null"`
	EndedAt                  *time.Time
}

func (RunRecord) TableName() string { return "agent_runs" }

type EventRecord struct {
	BaseModel
	RunID           string    `gorm:"size:64;not null;default:'';index:idx_agent_run_events_run_id;uniqueIndex:uk_agent_run_events_run_scope_event,priority:1"`
	TenantID        string    `gorm:"size:64;not null;default:'default';index:idx_agent_run_events_actor,priority:1"`
	ActorID         string    `gorm:"size:64;not null;default:'';index:idx_agent_run_events_actor,priority:2"`
	ThreadKind      string    `gorm:"size:64;not null;default:''"`
	ThreadID        string    `gorm:"size:128;not null;default:''"`
	ProjectionKind  string    `gorm:"size:64;not null;default:''"`
	ProjectionID    string    `gorm:"size:128;not null;default:''"`
	EventScope      string    `gorm:"size:32;not null;default:'';index:idx_agent_run_events_scope;uniqueIndex:uk_agent_run_events_run_scope_event,priority:2"`
	EventID         string    `gorm:"size:255;not null;default:'';uniqueIndex:uk_agent_run_events_run_scope_event,priority:3"`
	EventType       string    `gorm:"size:32;not null;default:'';index:idx_agent_run_events_type"`
	SchemaVersion   int       `gorm:"not null;default:0"`
	StepID          string    `gorm:"size:64;not null;default:'';index:idx_agent_run_events_step_id"`
	Visibility      string    `gorm:"size:32;not null;default:'user'"`
	Phase           string    `gorm:"size:32;not null;default:'';index:idx_agent_run_events_phase"`
	Stage           string    `gorm:"size:32;not null;default:'';index:idx_agent_run_events_stage"`
	RoundID         string    `gorm:"size:64;not null;default:'';index:idx_agent_run_events_round_id"`
	ParentEventID   string    `gorm:"size:255;not null;default:'';index:idx_agent_run_events_parent_event_id"`
	Status          string    `gorm:"size:32;not null;default:'';index:idx_agent_run_events_status"`
	Title           string    `gorm:"size:255;not null;default:''"`
	Summary         string    `gorm:"size:255;not null;default:''"`
	ContentMarkdown string    `gorm:"type:text;not null;default:''"`
	PayloadJSON     string    `gorm:"type:text;not null;default:''"`
	Seq             int64     `gorm:"not null;default:0;index:idx_agent_run_events_seq"`
	ToolCallID      string    `gorm:"size:255;not null;default:'';index:idx_agent_run_events_tool_call_id"`
	ToolName        string    `gorm:"size:128;not null;default:'';index:idx_agent_run_events_tool_name"`
	LatencyMS       int64     `gorm:"not null;default:0"`
	InputJSON       string    `gorm:"type:text;not null;default:''"`
	OutputJSON      string    `gorm:"type:text;not null;default:''"`
	ErrorJSON       string    `gorm:"type:text;not null;default:''"`
	StartedAt       time.Time `gorm:"not null"`
	EndedAt         *time.Time
}

func (EventRecord) TableName() string { return "agent_run_events" }

type RunStep struct {
	BaseModel
	StepID, RunID, ParentStepID, PlanID  string
	StepIndex, Attempt                   int
	Kind, Title, Description, Status     string
	DependsOnJSON, ExpectedToolsJSON     string
	ResourceRefsJSON                     string
	ApprovalRequired                     bool
	ResultSummary, InputJSON, OutputJSON string
	ErrorJSON                            string
	StartedAt, EndedAt                   *time.Time
}

func (RunStep) TableName() string { return "agent_run_steps" }

// ContextRecord stores either a context snapshot or one immutable artifact.
// All host correlations are opaque refs.
type ContextRecord struct {
	BaseModel
	RecordType             string `gorm:"size:32;not null;index:idx_agent_context_records_type"`
	SnapshotID             string `gorm:"size:64;not null;default:'';index:idx_agent_context_records_snapshot"`
	ArtifactID             string `gorm:"size:64;not null;default:'';uniqueIndex:uk_agent_context_records_artifact"`
	RunID                  string `gorm:"size:64;not null;default:'';index:idx_agent_context_records_run"`
	TenantID               string `gorm:"size:64;not null;default:'';index:idx_agent_context_records_actor,priority:1"`
	ActorID                string `gorm:"size:64;not null;default:'';index:idx_agent_context_records_actor,priority:2"`
	ThreadKind             string `gorm:"size:64;not null;default:'';index:idx_agent_context_records_thread,priority:1"`
	ThreadID               string `gorm:"size:128;not null;default:'';index:idx_agent_context_records_thread,priority:2"`
	InputProjectionKind    string `gorm:"size:64;not null;default:''"`
	InputProjectionID      string `gorm:"size:128;not null;default:''"`
	ResourceKind           string `gorm:"size:64;not null;default:''"`
	ResourceID             string `gorm:"size:128;not null;default:''"`
	ResourceRevision       string `gorm:"size:64;not null;default:''"`
	SourceType             string `gorm:"size:64;not null;default:''"`
	SourceID               string `gorm:"size:128;not null;default:''"`
	SourceTitle            string `gorm:"size:255;not null;default:''"`
	SchemaVersion          int
	ThreadPathHash         string `gorm:"size:64;not null;default:''"`
	ContentJSON            string `gorm:"type:text;not null;default:''"`
	Content                string `gorm:"type:text;not null;default:''"`
	ContentHash            string `gorm:"size:64;not null;default:'';index:idx_agent_context_records_hash"`
	MetadataJSON           string `gorm:"type:text;not null;default:''"`
	TokenEstimate          int64
	Score                  float64
	ExpiresAt              *time.Time `gorm:"index"`
	FileCount              int
	RAGCount               int
	SkillCount             int
	MemoryCount            int
	OutputCount            int
	EvidenceCount          int
	RetrievalFallbackCount int
	SkippedCount           int
}

func (ContextRecord) TableName() string { return "agent_context_records" }
