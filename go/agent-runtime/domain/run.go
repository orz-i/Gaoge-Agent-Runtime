package domain

import "time"

const (
	RunStatusQueued       = "queued"
	RunStatusPreparing    = "preparing"
	RunStatusWaitingInput = "waiting_input"
	RunStatusRunning      = "running"
	RunStatusCompleted    = "completed"
	RunStatusFailed       = "failed"
	RunStatusCancelled    = "cancelled"
	RunStatusSuspended    = "suspended"
)

// Run is the durable root of an Agent Runtime execution.
type Run struct {
	RunID                    string
	RequestID                string
	Actor                    ActorRef
	Thread                   ThreadRef
	InputProjection          ProjectionRef
	OutputProjection         ProjectionRef
	Environment              ResourceRef
	AgentManifest            ResourceRef
	AgentName                string
	RootRunID                string
	ParentRunID              string
	HandoffID                string
	Depth                    int
	Goal                     string
	RunConfigSnapshotJSON    string
	RequestFingerprint       string
	CurrentStepID            string
	CurrentPlanID            string
	PendingInteractionID     string
	StatusReason             string
	LastEventSeq             int64
	LastPresentationEventSeq int64
	StartedBy                string
	Endpoint                 string
	Provider                 string
	ProviderProtocol         string
	RequestedModelName       string
	PlatformModelName        string
	RoutedBindingCode        string
	ModelVendor              string
	ModelIcon                string
	UpstreamModelName        string
	InputTokens              int64
	OutputTokens             int64
	CacheReadTokens          int64
	CacheWriteTokens         int64
	ReasoningTokens          int64
	LLMCallsCount            int
	ToolCallsCount           int
	BilledCurrency           string
	BilledNanousd            int64
	LastBillingSnapshotJSON  string
	FirstTokenLatencyMS      int64
	TotalLatencyMS           int64
	Status                   string
	ErrorCode                string
	ErrorMessage             string
	StartedAt                time.Time
	EndedAt                  *time.Time
	CreatedAt                time.Time
	UpdatedAt                time.Time
	Activity                 []ActivityItem
}

type RunCursor struct {
	Status                   string
	LastEventSeq             int64
	LastPresentationEventSeq int64
	CurrentStepID            string
	PendingInteractionID     string
}

// TerminalIntent is the complete deterministic terminal state persisted for a run.
// Host projections are finalized through TurnProjectionWriter in the same UnitOfWork.
type TerminalIntent struct {
	Actor                            ActorRef
	Thread                           ThreadRef
	RunID, Outcome, CurrentStepID    string
	Summary, ErrorCode, ErrorMessage string
	DiagnosticJSON                   string
	Output                           *OutputRef
	Retire                           bool
}

const (
	TerminalCompleted = "completed"
	TerminalFailed    = "failed"
	TerminalCancelled = "cancelled"
)
