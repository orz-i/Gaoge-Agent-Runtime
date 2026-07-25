package models

import "time"

type RuntimePlanRecord struct {
	BaseModel
	PlanID, RunID string
	Revision      int
	Status        string
	Goal, Summary string
	PayloadJSON   string
	ApprovedAt    *time.Time
}

func (RuntimePlanRecord) TableName() string { return "agent_plans" }

type RunInteraction struct {
	BaseModel
	InteractionID, RunID, StepID, ToolCallID string
	Type, Status                             string
	RequestPayloadJSON, ResponseSchemaJSON   string
	ResponseJSON, ResolveRequestID           string
	ResumeFingerprint                        string
	RequestedAt                              time.Time
	ExpiresAt, ResolvedAt                    *time.Time
	ResolvedByTenantID                       string
	ResolvedByActorID                        string
}

func (RunInteraction) TableName() string { return "agent_interactions" }

type RunCheckpoint struct {
	BaseModel
	CheckpointID, RunID, ParentCheckpointID string
	EventSeq                                int64
	StepID, ToolCallID                      string
	ContextSnapshotID                       string
	MessagePathHash, ManifestHash           string
	Kind, Status                            string
	ResumeStateJSON                         string
	ResumeRequestID, ResumeFingerprint      string
}

func (RunCheckpoint) TableName() string { return "agent_checkpoints" }

type RuntimeOutputIdentityRecord struct {
	BaseModel
	TenantID             string `gorm:"size:64;not null;uniqueIndex:uk_agent_output_identities_actor_output,priority:1"`
	ActorID              string `gorm:"size:64;not null;uniqueIndex:uk_agent_output_identities_actor_output,priority:2"`
	OutputID             string `gorm:"size:64;not null;uniqueIndex:uk_agent_output_identities_actor_output,priority:3"`
	LatestPublishedRefID uint
	NextVersion          int
	WriterRunID          string
	WriterHeadRefID      uint
	CreatedByRunID       string
}

func (RuntimeOutputIdentityRecord) TableName() string { return "agent_output_identities" }

type RuntimeOutputRefRecord struct {
	BaseModel
	IdentityID                          uint
	OutputID, RunID, StepID, ToolCallID string
	SourceToolCallID, SourceEventID     string
	Kind, Title, Summary, FileID        string
	ProjectionKind, ProjectionID        string
	PreviewJSON                         string
	Version                             int
	ParentRefID                         uint
	Status                              string
	SourceSnapshotID                    string
	FileSHA256, FileMIMEType            string
}

func (RuntimeOutputRefRecord) TableName() string { return "agent_output_refs" }

type RuntimeWorkbenchProjectionRecord struct {
	BaseModel
	RunID                      string
	ProjectionVersion          int
	SourcePresentationEventSeq int64
}

func (RuntimeWorkbenchProjectionRecord) TableName() string { return "agent_workbench_projections" }

type RuntimePhaseProjectionRecord struct {
	BaseModel
	PhaseID, RunID, Kind, Title, Summary, Status string
	StartSeq, EndSeq                             int64
	StepIDsJSON, ToolCallIDsJSON, OutputIDsJSON  string
	StartedAt                                    time.Time
	EndedAt                                      *time.Time
}

func (RuntimePhaseProjectionRecord) TableName() string { return "agent_phase_projections" }

type EvidenceSelection struct {
	BaseModel
	EvidenceID, SourceKind, SourceID string
	TenantID, ActorID                string
	ProjectionKind, ProjectionID     string
	Kind, SelectorJSON, Title        string
	Excerpt, ContentHash             string
	SourceContentHash                string
}

func (EvidenceSelection) TableName() string { return "agent_evidence" }

type RunQueueItemRecord struct {
	BaseModel
	QueueID, ClientQueueID, RequestFingerprint string
	TenantID, ActorID                          string
	ThreadKind, ThreadID                       string
	Status                                     string
	Position, Revision, AttemptCount           int
	RequestJSON                                string
	AnchorProjectionKind, AnchorProjectionID   string
	AnchorRunID, StartedRunID                  string
	ErrorCode, ErrorMessage                    string
	NextAttemptAt                              *time.Time
}

func (RunQueueItemRecord) TableName() string { return "agent_queue_items" }

type ContinuationJobRecord struct {
	BaseModel
	JobID                    string     `gorm:"size:64;not null;uniqueIndex:uk_agent_continuation_jobs_job_id"`
	SegmentKey               string     `gorm:"size:255;not null;uniqueIndex:uk_agent_continuation_jobs_segment_key"`
	RunID                    string     `gorm:"size:64;not null;index:idx_agent_continuation_jobs_run_id"`
	CheckpointID             string     `gorm:"size:64;not null;index:idx_agent_continuation_jobs_checkpoint_id"`
	TenantID                 string     `gorm:"size:64;not null;default:'default';index:idx_agent_continuation_jobs_actor,priority:1"`
	ActorID                  string     `gorm:"size:64;not null;default:'';index:idx_agent_continuation_jobs_actor,priority:2"`
	Source                   string     `gorm:"size:64;not null;default:''"`
	Status                   string     `gorm:"size:32;not null;index:idx_agent_continuation_jobs_dispatch,priority:1"`
	TraceParent              string     `gorm:"size:128;not null;default:''"`
	TraceState               string     `gorm:"size:512;not null;default:''"`
	ReservationAmountNanousd int64      `gorm:"not null;default:0"`
	ReservationRefNo         string     `gorm:"size:255;not null;default:''"`
	AttemptCount             int        `gorm:"not null;default:0"`
	MaxAttempts              int        `gorm:"not null;default:5"`
	AvailableAt              time.Time  `gorm:"not null;index:idx_agent_continuation_jobs_dispatch,priority:2"`
	LeaseOwner               string     `gorm:"size:128;not null;default:'';index:idx_agent_continuation_jobs_lease_owner"`
	LeaseExpiresAt           *time.Time `gorm:"index:idx_agent_continuation_jobs_lease_expiry"`
	HeartbeatAt              *time.Time
	LastError                string `gorm:"type:text;not null;default:''"`
}

func (ContinuationJobRecord) TableName() string { return "agent_continuation_jobs" }
