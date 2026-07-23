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
