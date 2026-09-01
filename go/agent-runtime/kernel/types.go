package kernel

import (
	"encoding/json"
	"time"
)

// Capability identifies one explicitly provided runtime capability.
type Capability string

const (
	// CapabilityRuntime is the minimal Run state-machine capability.
	CapabilityRuntime Capability = "kernel.runtime"
)

// RunKind identifies the state-machine owner selected before Run creation.
type RunKind string

// RunStatus is the lifecycle state shared by all runtime features.
type RunStatus string

const (
	RunStatusRunning      RunStatus = "running"
	RunStatusWaitingInput RunStatus = "waiting_input"
	RunStatusCompleted    RunStatus = "completed"
	RunStatusFailed       RunStatus = "failed"
	RunStatusCancelled    RunStatus = "cancelled"
)

// ActorRef identifies a tenant-scoped runtime actor.
type ActorRef struct {
	TenantID string `json:"tenantID"`
	ActorID  string `json:"actorID"`
}

// ThreadRef identifies a host-owned thread without exposing database keys.
type ThreadRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Run is the minimal durable runtime root.
type Run struct {
	ID          string     `json:"id"`
	Kind        RunKind    `json:"kind"`
	Actor       ActorRef   `json:"actor"`
	Thread      ThreadRef  `json:"thread"`
	RequestID   string     `json:"requestID,omitempty"`
	Goal        string     `json:"goal"`
	Status      RunStatus  `json:"status"`
	Revision    uint64     `json:"revision"`
	ErrorCode   string     `json:"errorCode,omitempty"`
	ErrorDetail string     `json:"errorDetail,omitempty"`
	DeadlineAt  *time.Time `json:"deadlineAt,omitempty"`
	EndedAt     *time.Time `json:"endedAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// EventDraft is an unsequenced event submitted with an atomic transition.
type EventDraft struct {
	Type    string          `json:"type"`
	Message string          `json:"message,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
	// Wakeup marks this committed fact as requiring durable future projection.
	// It is transaction metadata, not part of the public Event journal shape.
	Wakeup bool `json:"-"`
	// WakeupAt delays the projected external wakeup until the specified instant.
	// Stores persist it only inside the committed-transition outbox; the public
	// Event journal remains unchanged.
	WakeupAt *time.Time `json:"wakeupAt,omitempty"`
}

// Event is an append-only fact sequenced within one Run.
type Event struct {
	Seq       int64           `json:"seq"`
	Type      string          `json:"type"`
	Message   string          `json:"message,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}

// CheckpointStatus is the lifecycle of a generic feature-owned checkpoint.
type CheckpointStatus string

const (
	CheckpointPending  CheckpointStatus = "pending"
	CheckpointResolved CheckpointStatus = "resolved"
)

// Checkpoint persists an opaque feature wait payload without Kernel interpretation.
type Checkpoint struct {
	ID         string           `json:"id"`
	Kind       string           `json:"kind"`
	Status     CheckpointStatus `json:"status"`
	Payload    json.RawMessage  `json:"payload"`
	Response   json.RawMessage  `json:"response,omitempty"`
	CreatedAt  time.Time        `json:"createdAt"`
	ResolvedAt *time.Time       `json:"resolvedAt,omitempty"`
}

// Result is the terminal feature-neutral Run output.
type Result struct {
	ContentType string          `json:"contentType"`
	Content     json.RawMessage `json:"content"`
}

// Snapshot is the atomically readable state of one Run.
type Snapshot struct {
	Run        Run             `json:"run"`
	State      json.RawMessage `json:"state"`
	Checkpoint *Checkpoint     `json:"checkpoint,omitempty"`
	Result     *Result         `json:"result,omitempty"`
	EventHead  int64           `json:"eventHead"`
}

// CreateRequest creates one explicit Runtime Kind with opaque feature state.
type CreateRequest struct {
	ID         string
	Kind       RunKind
	Actor      ActorRef
	Thread     ThreadRef
	RequestID  string
	Goal       string
	DeadlineAt *time.Time
	State      json.RawMessage
	Events     []EventDraft
}

// Mutation replaces feature state and advances the Run through one atomic CAS.
type Mutation struct {
	Status      RunStatus
	State       json.RawMessage
	Checkpoint  *Checkpoint
	Result      *Result
	ErrorCode   string
	ErrorDetail string
	Events      []EventDraft
}
