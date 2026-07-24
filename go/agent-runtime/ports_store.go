package agentruntime

import (
	"context"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

// Store composes the aggregate-oriented persistence capabilities required by
// Engine. Implementations must not read or write host-owned tables.
type Store interface {
	RunStore
	EventStore
	PlanningStore
	InteractionStore
	CheckpointStore
	ContextStore
	OutputStore
	EvidenceStore
	QueueStore
	ContinuationJobStore
	AgentManifestStore
	RunHandoffStore
	WorkbenchStore
}

type AgentManifestStore interface {
	CreateAgentManifestRevision(context.Context, *domain.AgentManifest, int) (*domain.AgentManifest, bool, error)
	GetAgentManifest(context.Context, domain.ActorRef, domain.ResourceRef) (*domain.AgentManifest, error)
	ListAgentManifests(context.Context, domain.ActorRef, domain.AgentManifestFilter) (domain.AgentManifestPage, error)
}

type RunHandoffStore interface {
	CreateRunHandoff(context.Context, *domain.RunHandoff) (*domain.RunHandoff, bool, error)
	CreateRunHandoffWithinLimit(context.Context, *domain.RunHandoff, int) (*domain.RunHandoff, bool, error)
	GetRunHandoff(context.Context, domain.ActorRef, string) (*domain.RunHandoff, error)
	GetRunHandoffByChildRun(context.Context, domain.ActorRef, string) (*domain.RunHandoff, error)
	ListRunHandoffs(context.Context, domain.ActorRef, domain.RunHandoffFilter) (domain.RunHandoffPage, error)
	CompleteRunHandoff(context.Context, domain.ActorRef, string, domain.RunHandoffCompletion) (*domain.RunHandoff, bool, error)
}

// ContinuationJobStore persists executable checkpoint handoffs. Enqueue calls
// participate in the caller's UnitOfWork so checkpoint and job commit atomically.
type ContinuationJobStore interface {
	CreateContinuationJob(context.Context, *domain.ContinuationJob) (*domain.ContinuationJob, bool, error)
	GetContinuationJob(context.Context, string) (*domain.ContinuationJob, error)
	ListContinuationJobs(context.Context, domain.ContinuationJobFilter) (domain.ContinuationJobPage, error)
	RequeueDeadLetterContinuationJob(context.Context, string, time.Time) (*domain.ContinuationJob, error)
	DeadLetterExpiredContinuationJob(context.Context, time.Time) (*domain.ContinuationJob, error)
	ClaimNextContinuationJob(context.Context, string, time.Time, time.Time) (*domain.ContinuationJob, error)
	HeartbeatContinuationJob(context.Context, string, string, time.Time, time.Time) error
	CompleteContinuationJob(context.Context, string, string, time.Time) error
	RetryContinuationJob(context.Context, string, string, string, time.Time, bool) error
}

type RunStore interface {
	CreateRunStartBundle(context.Context, *domain.Run, *domain.Step, *domain.ContextSnapshot, []domain.ContextArtifact, *domain.Checkpoint, []domain.Event) ([]domain.Event, error)
	GetRun(context.Context, domain.ActorRef, string) (*domain.Run, error)
	GetActiveRun(context.Context, domain.ActorRef, domain.ThreadRef) (*domain.Run, error)
	ListRuns(context.Context, domain.ActorRef, *domain.ThreadRef, int, int) ([]domain.Run, int64, error)
	ListNonterminalRuns(context.Context, time.Time) ([]domain.Run, error)
	GetRunCursor(context.Context, domain.ActorRef, string) (*domain.RunCursor, error)
	ListRunSteps(context.Context, string) ([]domain.Step, error)
}

type EventStore interface {
	AppendRunEvent(context.Context, *domain.Event) (*domain.Event, bool, error)
	AppendRunEvents(context.Context, []domain.Event) ([]domain.Event, error)
	AppendRunEventsIfCurrent(context.Context, string, string, int64, []domain.Event) ([]domain.Event, bool, error)
	ListRunEventsAfter(context.Context, domain.ActorRef, string, int64, int) ([]domain.Event, error)
	ListRunEventsBefore(context.Context, domain.ActorRef, string, int64, int) ([]domain.Event, error)
	GetRunEvent(context.Context, domain.ActorRef, string, string) (*domain.Event, error)
	GetRunToolResult(context.Context, domain.ActorRef, string, string) (*domain.Event, error)
	CountRunEventsByType(context.Context, domain.ActorRef, string, []string) (map[string]int, error)
	DeleteRunEventsBefore(context.Context, time.Time) (int64, error)
}

type PlanningStore interface {
	CreatePlanningBundle(context.Context, string, string, *domain.Plan, []domain.Step, *domain.Interaction, *domain.Checkpoint, []domain.Event) ([]domain.Event, error)
	GetCurrentPlan(context.Context, domain.ActorRef, string) (*domain.Plan, error)
	ListPlans(context.Context, domain.ActorRef, string) ([]domain.Plan, error)
}

type InteractionStore interface {
	CreateRunInteractionBundle(context.Context, string, string, *domain.Interaction, *domain.Checkpoint, []domain.Event) ([]domain.Event, error)
	GetRunInteraction(context.Context, domain.ActorRef, string, string) (*domain.Interaction, error)
	ListRunInteractions(context.Context, domain.ActorRef, string) ([]domain.Interaction, error)
	ListExpiredRunInteractions(context.Context, time.Time, int) ([]domain.ExpiredInteraction, error)
	ExpireRunInteraction(context.Context, string) ([]domain.Event, bool, error)
	ResolveRunInteractionWithCheckpoint(context.Context, domain.ActorRef, string, string, string, string, string, string, *domain.Checkpoint, []domain.Event) (*domain.Interaction, *domain.Checkpoint, []domain.Event, bool, error)
}

type CheckpointStore interface {
	CreateRunCheckpointBundle(context.Context, *domain.Checkpoint, []domain.Event) ([]domain.Event, error)
	GetRunCheckpoint(context.Context, domain.ActorRef, string, string) (*domain.Checkpoint, error)
	ListRunCheckpoints(context.Context, domain.ActorRef, string) ([]domain.Checkpoint, error)
	ResumeRun(context.Context, domain.ActorRef, string, string, string, string, string, *domain.Checkpoint, []domain.Event) (*domain.Checkpoint, *domain.Checkpoint, []domain.Event, bool, error)
	RenewExpiredRunInteraction(context.Context, domain.ActorRef, string, string, string, string, string, *domain.Interaction, *domain.Checkpoint, []domain.Event) (*domain.Checkpoint, *domain.Checkpoint, *domain.Interaction, []domain.Event, bool, error)
}

type ContextStore interface {
	CreateContextSnapshotBundle(context.Context, *domain.ContextSnapshot, []domain.ContextArtifact, *domain.Checkpoint, []domain.Event) ([]domain.Event, error)
	GetRunContextSnapshot(context.Context, domain.ActorRef, string) (*domain.ContextSnapshot, error)
	CreateContextArtifacts(context.Context, []domain.ContextArtifact) error
	ListRecentContextArtifacts(context.Context, domain.ActorRef, domain.ThreadRef, int) ([]domain.ContextArtifact, error)
	GetContextArtifact(context.Context, domain.ActorRef, string) (*domain.ContextArtifact, error)
	DeleteExpiredContextArtifacts(context.Context, time.Time, int) (int64, error)
}

type OutputStore interface {
	FinalizeRun(context.Context, domain.TerminalIntent) (*domain.OutputRef, []domain.Event, bool, error)
	AppendRunBilling(context.Context, string, string, string, int64, string, domain.Event) (*domain.Event, bool, error)
	CommitRunToolResultBundle(context.Context, *domain.Checkpoint, *domain.OutputRef, []domain.Event) (*domain.OutputRef, []domain.Event, bool, error)
	ListOutputs(context.Context, domain.ActorRef, string) ([]domain.OutputRef, error)
	GetOutputsByIDs(context.Context, domain.ActorRef, []string) ([]domain.OutputRef, error)
	ListUserOutputs(context.Context, domain.ActorRef, string, string, int) ([]domain.OutputListItem, string, error)
	GetOutputVersion(context.Context, domain.ActorRef, string, int) (*domain.OutputListItem, error)
	ListOutputVersions(context.Context, domain.ActorRef, string, int, int) ([]domain.OutputListItem, bool, error)
}

type EvidenceStore interface {
	CreateEvidence(context.Context, *domain.Evidence) error
	GetEvidenceByIDs(context.Context, domain.ActorRef, []string) ([]domain.Evidence, error)
}

type QueueStore interface {
	CreateRunQueueItem(context.Context, *domain.QueueItem) (*domain.QueueItem, bool, error)
	GetRunQueueItem(context.Context, domain.ActorRef, domain.ThreadRef, string) (*domain.QueueItem, error)
	ListRunQueueItems(context.Context, domain.ActorRef, domain.ThreadRef) ([]domain.QueueItem, error)
	UpdateRunQueueItem(context.Context, *domain.QueueItem, int) error
	CancelRunQueueItem(context.Context, domain.ActorRef, domain.ThreadRef, string) (*domain.QueueItem, error)
	PrioritizeRunQueueItem(context.Context, domain.ActorRef, domain.ThreadRef, string) (*domain.QueueItem, error)
	ClaimNextRunQueueItem(context.Context, time.Time) (*domain.QueueItem, error)
	MarkRunQueueStarted(context.Context, string, string) error
	RequeueRunQueueItem(context.Context, string, string, string, *time.Time) error
}

type WorkbenchStore interface {
	LoadWorkbenchSnapshot(context.Context, domain.ActorRef, string) (*domain.WorkbenchSnapshot, error)
	ReplaceWorkbenchProjection(context.Context, domain.ActorRef, *domain.WorkbenchProjection, []domain.PhaseProjection) error
	ListPresentationEvents(context.Context, domain.ActorRef, string, int64) ([]domain.Event, error)
}
