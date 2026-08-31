package models

import "time"

// KernelRunRecord stores only the feature-neutral Kernel aggregate.
// Feature State, Checkpoint and Result remain opaque JSON.
type KernelRunRecord struct {
	RunID          string     `gorm:"size:64;primaryKey"`
	RequestID      string     `gorm:"size:128;not null;default:'';index:idx_agent_kernel_runs_request"`
	Kind           string     `gorm:"size:32;not null;index:idx_agent_kernel_runs_kind"`
	TenantID       string     `gorm:"size:64;not null;index:idx_agent_kernel_runs_actor,priority:1"`
	ActorID        string     `gorm:"size:64;not null;index:idx_agent_kernel_runs_actor,priority:2"`
	ThreadKind     string     `gorm:"size:64;not null;index:idx_agent_kernel_runs_thread,priority:1"`
	ThreadID       string     `gorm:"size:128;not null;index:idx_agent_kernel_runs_thread,priority:2"`
	Goal           string     `gorm:"type:text;not null"`
	Status         string     `gorm:"size:32;not null;index:idx_agent_kernel_runs_status"`
	Revision       uint64     `gorm:"not null"`
	ErrorCode      string     `gorm:"size:128;not null;default:''"`
	ErrorDetail    string     `gorm:"type:text;not null;default:''"`
	DeadlineAt     *time.Time `gorm:"index:idx_agent_kernel_runs_deadline"`
	EndedAt        *time.Time `gorm:"index:idx_agent_kernel_runs_ended"`
	StateJSON      string     `gorm:"type:text;not null"`
	CheckpointJSON string     `gorm:"type:text;not null;default:''"`
	ResultJSON     string     `gorm:"type:text;not null;default:''"`
	LastEventSeq   int64      `gorm:"not null;default:0"`
	CreatedAt      time.Time  `gorm:"not null"`
	UpdatedAt      time.Time  `gorm:"not null"`
}

func (KernelRunRecord) TableName() string { return "agent_kernel_runs" }

// KernelEventRecord stores the monotonic event stream for one Kernel Run.
type KernelEventRecord struct {
	RunID     string    `gorm:"size:64;primaryKey"`
	Seq       int64     `gorm:"primaryKey;autoIncrement:false"`
	Type      string    `gorm:"size:128;not null;index:idx_agent_kernel_events_type"`
	Message   string    `gorm:"type:text;not null;default:''"`
	DataJSON  string    `gorm:"type:text;not null;default:''"`
	CreatedAt time.Time `gorm:"not null;index:idx_agent_kernel_events_created"`
}

func (KernelEventRecord) TableName() string { return "agent_kernel_events" }

// KernelTransitionOutboxRecord stores one feature-neutral committed Run
// transition until all composed projectors have durably handed it off. V1 has
// one continuation projector, so acknowledgement removes the row.
type KernelTransitionOutboxRecord struct {
	ID          string     `gorm:"size:160;primaryKey"`
	RunID       string     `gorm:"size:64;not null;index:idx_agent_kernel_transition_run;uniqueIndex:idx_agent_kernel_transition_revision,priority:1"`
	Kind        string     `gorm:"size:32;not null"`
	Status      string     `gorm:"size:32;not null"`
	Revision    uint64     `gorm:"not null;uniqueIndex:idx_agent_kernel_transition_revision,priority:2"`
	EventsJSON  string     `gorm:"type:text;not null"`
	CommittedAt time.Time  `gorm:"not null;index:idx_agent_kernel_transition_ready,priority:2"`
	AvailableAt time.Time  `gorm:"not null;index:idx_agent_kernel_transition_ready,priority:1"`
	Attempts    uint32     `gorm:"not null;default:0"`
	LeaseID     string     `gorm:"size:256;not null;default:''"`
	WorkerID    string     `gorm:"size:128;not null;default:''"`
	LeaseUntil  *time.Time `gorm:"index:idx_agent_kernel_transition_lease"`
}

func (KernelTransitionOutboxRecord) TableName() string { return "agent_kernel_transition_outbox" }

// RunRelationRecord stores immutable parent/child Run ownership.
type RunRelationRecord struct {
	ChildRunID  string    `gorm:"size:64;primaryKey"`
	ParentRunID string    `gorm:"size:64;not null;index:idx_agent_run_relations_parent;uniqueIndex:idx_agent_run_relations_owner,priority:1"`
	Kind        string    `gorm:"size:32;not null;uniqueIndex:idx_agent_run_relations_owner,priority:2"`
	OwnerNodeID string    `gorm:"size:128;not null;uniqueIndex:idx_agent_run_relations_owner,priority:3"`
	CreatedAt   time.Time `gorm:"not null;index:idx_agent_run_relations_created"`
}

func (RunRelationRecord) TableName() string { return "agent_run_relations" }
