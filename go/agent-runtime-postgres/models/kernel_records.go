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
