package harnesspostgres

import "time"

type sessionRecord struct {
	ID             string    `gorm:"primaryKey;size:64"`
	HostThreadKind string    `gorm:"size:64;not null;uniqueIndex:uk_harness_session_host,priority:1"`
	HostThreadID   string    `gorm:"size:128;not null;uniqueIndex:uk_harness_session_host,priority:2"`
	TenantID       string    `gorm:"size:128;not null"`
	ActorID        string    `gorm:"size:128;not null"`
	Revision       uint64    `gorm:"not null"`
	CreatedAt      time.Time `gorm:"not null"`
	UpdatedAt      time.Time `gorm:"not null"`
}

func (sessionRecord) TableName() string { return "agent_harness_sessions" }

type turnRecord struct {
	ID                            string    `gorm:"primaryKey;size:64"`
	SessionID                     string    `gorm:"size:64;not null;index:idx_harness_turn_session;uniqueIndex:uk_harness_turn_host,priority:1"`
	HostTurnKind                  string    `gorm:"size:64;not null;uniqueIndex:uk_harness_turn_host,priority:2"`
	HostTurnID                    string    `gorm:"size:128;not null;uniqueIndex:uk_harness_turn_host,priority:3"`
	ConfigSnapshotID              string    `gorm:"size:64;not null;index:idx_harness_turn_config"`
	ContextCheckpointID           string    `gorm:"size:128;not null;default:'';index:idx_harness_turn_context"`
	ContextRefID                  string    `gorm:"size:128;not null;default:''"`
	ContextGeneration             int       `gorm:"not null;default:0"`
	ContextRevision               int       `gorm:"not null;default:0"`
	ContextLineageHash            string    `gorm:"size:64;not null;default:''"`
	ContextCoveredThroughSourceID string    `gorm:"size:128;not null;default:''"`
	ContextContentHash            string    `gorm:"size:64;not null;default:''"`
	Status                        string    `gorm:"size:32;not null;index:idx_harness_turn_status"`
	Revision                      uint64    `gorm:"not null"`
	ErrorCode                     string    `gorm:"size:128;not null;default:''"`
	ErrorDetail                   string    `gorm:"type:text;not null;default:''"`
	CreatedAt                     time.Time `gorm:"not null"`
	UpdatedAt                     time.Time `gorm:"not null"`
}

func (turnRecord) TableName() string { return "agent_harness_turns" }

type invocationRecord struct {
	ID                string    `gorm:"primaryKey;size:64"`
	TurnID            string    `gorm:"size:64;not null;index:idx_harness_invocation_turn"`
	ParentItemID      string    `gorm:"size:64;not null;default:''"`
	CapabilityKey     string    `gorm:"size:128;not null"`
	DefinitionVersion string    `gorm:"size:128;not null;default:''"`
	ExecutionClass    string    `gorm:"size:32;not null"`
	InputJSON         string    `gorm:"type:text;not null;default:''"`
	InputHash         string    `gorm:"size:64;not null;default:''"`
	ExecutionRefID    string    `gorm:"size:128;not null;default:'';uniqueIndex:uk_harness_invocation_execution,where:execution_ref_id <> ''"`
	Status            string    `gorm:"size:32;not null;index:idx_harness_invocation_status"`
	Attempt           int       `gorm:"not null"`
	OutputRefsJSON    string    `gorm:"type:text;not null;default:'[]'"`
	ErrorCode         string    `gorm:"size:128;not null;default:''"`
	ErrorDetail       string    `gorm:"type:text;not null;default:''"`
	Revision          uint64    `gorm:"not null"`
	CreatedAt         time.Time `gorm:"not null"`
	UpdatedAt         time.Time `gorm:"not null"`
}

func (invocationRecord) TableName() string { return "agent_harness_invocations" }

type interactionRecord struct {
	ID                 string    `gorm:"primaryKey;size:64"`
	TurnID             string    `gorm:"size:64;not null;index:idx_harness_interaction_turn;uniqueIndex:uk_harness_interaction_waiting_turn,where:status = 'waiting'"`
	InvocationID       string    `gorm:"size:64;not null;index:idx_harness_interaction_invocation"`
	ParentItemID       string    `gorm:"size:64;not null;default:''"`
	ApplicationRefJSON string    `gorm:"type:text;not null;default:''"`
	ArtifactRefsJSON   string    `gorm:"type:text;not null;default:'[]'"`
	Key                string    `gorm:"size:128;not null"`
	Kind               string    `gorm:"size:32;not null"`
	SchemaJSON         string    `gorm:"type:text;not null"`
	PresentationJSON   string    `gorm:"type:text;not null;default:''"`
	Status             string    `gorm:"size:32;not null;index:idx_harness_interaction_status"`
	ResponseJSON       string    `gorm:"type:text;not null;default:''"`
	Revision           uint64    `gorm:"not null"`
	CreatedAt          time.Time `gorm:"not null"`
	UpdatedAt          time.Time `gorm:"not null"`
}

func (interactionRecord) TableName() string { return "agent_harness_interactions" }

type configRecord struct {
	ID          string    `gorm:"primaryKey;size:64"`
	TurnID      string    `gorm:"size:64;not null;index:idx_harness_config_turn"`
	ContentHash string    `gorm:"size:64;not null;uniqueIndex:uk_harness_config_hash,priority:2"`
	PayloadJSON string    `gorm:"type:text;not null"`
	CreatedAt   time.Time `gorm:"not null"`
}

func (configRecord) TableName() string { return "agent_harness_config_snapshots" }

type contextCheckpointRecord struct {
	ID                     string `gorm:"primaryKey;size:128"`
	ScopeID                string `gorm:"size:128;not null;index:idx_harness_context_checkpoint_scope,priority:1;index:idx_harness_context_checkpoint_reuse,priority:1"`
	Generation             int    `gorm:"not null;index:idx_harness_context_checkpoint_scope,priority:2"`
	Revision               int    `gorm:"not null;index:idx_harness_context_checkpoint_scope,priority:3"`
	StaticFingerprint      string `gorm:"size:64;not null;index:idx_harness_context_checkpoint_reuse,priority:2"`
	CoveredThroughSourceID string `gorm:"size:128;not null;index:idx_harness_context_checkpoint_reuse,priority:4"`
	CoveredPathHash        string `gorm:"size:64;not null"`
	SourceAligned          bool   `gorm:"not null;index:idx_harness_context_checkpoint_reuse,priority:3"`
	PayloadJSON            string `gorm:"type:text;not null"`
}

func (contextCheckpointRecord) TableName() string { return "agent_harness_context_checkpoints" }

type contextHeadRecord struct {
	ScopeID      string    `gorm:"primaryKey;size:128"`
	CheckpointID string    `gorm:"size:128;not null;index:idx_harness_context_head_checkpoint"`
	Revision     uint64    `gorm:"not null"`
	UpdatedAt    time.Time `gorm:"not null"`
}

func (contextHeadRecord) TableName() string { return "agent_harness_context_heads" }

type contextArtifactRecord struct {
	ID          string `gorm:"primaryKey;size:128"`
	ScopeID     string `gorm:"size:128;not null;index:idx_harness_context_artifact_scope,priority:1"`
	Generation  int    `gorm:"not null;index:idx_harness_context_artifact_scope,priority:2"`
	PayloadJSON string `gorm:"type:text;not null"`
}

func (contextArtifactRecord) TableName() string { return "agent_harness_context_artifacts" }

type itemRecord struct {
	ID           string    `gorm:"primaryKey;size:64"`
	TurnID       string    `gorm:"size:64;not null;uniqueIndex:uk_harness_item_seq,priority:1;index:idx_harness_item_turn"`
	Seq          uint64    `gorm:"not null;uniqueIndex:uk_harness_item_seq,priority:2"`
	Kind         string    `gorm:"size:32;not null"`
	Status       string    `gorm:"size:32;not null"`
	HostRefKind  string    `gorm:"size:64;not null;default:''"`
	HostRefID    string    `gorm:"size:128;not null;default:''"`
	RunID        string    `gorm:"size:64;not null;default:'';index:idx_harness_item_run"`
	InvocationID string    `gorm:"size:64;not null;default:'';index:idx_harness_item_invocation"`
	ParentItemID string    `gorm:"size:64;not null;default:''"`
	PayloadJSON  string    `gorm:"type:text;not null;default:'{}'"`
	CreatedAt    time.Time `gorm:"not null"`
	UpdatedAt    time.Time `gorm:"not null"`
}

func (itemRecord) TableName() string { return "agent_harness_items" }
