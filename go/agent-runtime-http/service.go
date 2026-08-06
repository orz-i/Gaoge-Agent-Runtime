package http

import (
	"context"

	runtime "github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/planexecute"
)

// ThreadQuery resolves host-owned thread metadata required by create commands.
type ThreadQuery interface {
	ResolveThread(context.Context, model.ActorRef, model.ThreadRef) (*runtime.ThreadSnapshot, error)
}

// PlanRunCommands owns explicit Plan-and-Execute creation and approval.
type PlanRunCommands interface {
	StartRun(context.Context, planexecute.StartRequest) (kernel.Snapshot, error)
	ResolveApproval(context.Context, string, uint64, planexecute.ApprovalResponse) (kernel.Snapshot, error)
}

// TextRunCommands owns direct Text Run creation and recovery commands.
type TextRunCommands interface {
	TextRunPolicy() runtime.TextRunPolicy
	StartTextRun(context.Context, runtime.StartTextRunInput) (*runtime.TextRunStartResult, error)
	ResumeTextRun(context.Context, runtime.ResumeTextRunInput) (*model.Checkpoint, bool, error)
	RetireTextRun(context.Context, model.ActorRef, string) (*model.Run, bool, error)
}

// RunCommands owns feature-neutral Run lifecycle transitions.
type RunCommands interface {
	CancelRun(context.Context, model.ActorRef, string) (bool, error)
	ReleaseRunUsageReservation(context.Context, *runtime.UsageBalanceReservation, string) error
}

// RunQueries owns shared Run, event, checkpoint and Workbench reads.
type RunQueries interface {
	ListRunRecords(context.Context, model.ActorRef, model.ThreadRef, int, int) ([]model.Run, int64, error)
	GetTextRunDetail(context.Context, model.ActorRef, string) (*runtime.TextRunDetail, error)
	GetWorkbench(context.Context, model.ActorRef, string) (*runtime.Workbench, error)
	GetRunCursor(context.Context, model.ActorRef, string) (*model.RunCursor, error)
	ListRunEventsAfter(context.Context, model.ActorRef, string, int64) ([]model.Event, error)
	SubscribeRunNotifications(context.Context, model.ActorRef, string, int64) ([]runtime.GenerationStreamEvent, <-chan runtime.GenerationStreamEvent, func(), bool)
	ListRunEventHistory(context.Context, model.ActorRef, string, int64, int) (*runtime.RunEventHistoryPage, error)
	GetRunEvent(context.Context, model.ActorRef, string, string) (*model.Event, error)
	GetPlan(context.Context, model.ActorRef, string) (*runtime.PlanView, error)
	ListRunInteractions(context.Context, model.ActorRef, string) ([]model.Interaction, error)
	ResolveRunInteraction(context.Context, runtime.ResolveRunInteractionInput) (*model.Interaction, error)
	ListRunCheckpoints(context.Context, model.ActorRef, string) ([]model.Checkpoint, error)
	GetRunResult(context.Context, model.ActorRef, string) (*model.RunResult, error)
	GetRunTaskTree(context.Context, model.ActorRef, string) (*runtime.RunTaskTree, error)
	GetRuntimeExecutionProvenance(context.Context, model.ActorRef, string) (*runtime.RuntimeExecutionProvenanceV1, error)
}

// RunQueueCommands owns the user-facing queued-send contract.
type RunQueueCommands interface {
	ListRunQueue(context.Context, model.ActorRef, model.ThreadRef) ([]model.QueueItem, error)
	EnqueueRun(context.Context, runtime.EnqueueRunInput) (*model.QueueItem, bool, error)
	UpdateRunQueue(context.Context, model.ActorRef, model.ThreadRef, string, int, runtime.RunQueueRequest) (*model.QueueItem, error)
	CancelRunQueue(context.Context, model.ActorRef, model.ThreadRef, string) (*model.QueueItem, error)
	PrioritizeRunQueue(context.Context, model.ActorRef, model.ThreadRef, string) (*model.QueueItem, error)
	InterruptAndSendRun(context.Context, model.ActorRef, model.ThreadRef, string) (*model.QueueItem, error)
}

// OutputQueries owns immutable output and evidence reads/writes.
type OutputQueries interface {
	ListOutputs(context.Context, model.ActorRef, string) ([]model.OutputRef, error)
	ListUserOutputs(context.Context, model.ActorRef, string, string, int) ([]model.OutputListItem, string, error)
	GetOutputVersion(context.Context, model.ActorRef, string, int) (*model.OutputListItem, error)
	ListOutputVersions(context.Context, model.ActorRef, string, int, int) ([]model.OutputListItem, bool, error)
	BuildOutputPreview(context.Context, model.ActorRef, string, int) (*model.OutputListItem, *runtime.OutputPreview, error)
	OpenOutputDownload(context.Context, model.ActorRef, string, int) (*model.OutputListItem, *runtime.FileContentResult, error)
	CreateEvidence(context.Context, runtime.CreateEvidenceInput) (*model.Evidence, error)
}

// AgentCommands owns Agent Manifest and Handoff resources.
type AgentCommands interface {
	ListAgentManifests(context.Context, model.ActorRef, model.AgentManifestFilter) (model.AgentManifestPage, error)
	GetAgentManifest(context.Context, model.ActorRef, model.ResourceRef) (*model.AgentManifest, error)
	CreateAgentManifestRevision(context.Context, runtime.AgentManifestRevisionInput) (*model.AgentManifest, bool, error)
	DelegateTextRun(context.Context, runtime.DelegateTextRunInput) (*runtime.DelegateTextRunResult, error)
	CreateRunHandoffJoin(context.Context, runtime.CreateRunHandoffJoinInput) (*model.RunHandoffJoin, bool, error)
	ListRunHandoffJoins(context.Context, model.ActorRef, model.RunHandoffJoinFilter) (model.RunHandoffJoinPage, error)
	GetRunHandoffJoin(context.Context, model.ActorRef, string) (*model.RunHandoffJoin, error)
}

// TeamCommands owns explicit Team Run creation.
type TeamCommands interface {
	StartAgentTeam(context.Context, runtime.StartAgentTeamInput) (*runtime.AgentTeamStartResult, error)
}

// WorkflowCommands owns explicit Workflow definitions and Run creation.
type WorkflowCommands interface {
	StartWorkflow(context.Context, runtime.StartWorkflowInput) (*runtime.WorkflowStartResult, error)
	ListWorkflowDefinitions(context.Context, model.ActorRef, model.WorkflowDefinitionFilter) (model.WorkflowDefinitionPage, error)
	GetWorkflowDefinition(context.Context, model.ActorRef, model.ResourceRef) (*model.WorkflowDefinition, error)
	ValidateWorkflowDefinition(context.Context, runtime.WorkflowDefinitionRevisionInput) (*runtime.WorkflowDefinitionValidation, error)
	CreateWorkflowDefinition(context.Context, runtime.WorkflowDefinitionRevisionInput) (*model.WorkflowDefinition, bool, error)
}

// ContinuationCommands owns operational dead-letter inspection and replay.
type ContinuationCommands interface {
	ListContinuationJobs(context.Context, model.ContinuationJobFilter) (runtime.ContinuationJobInspectionPage, error)
	RequeueDeadLetterContinuationJob(context.Context, runtime.RequeueDeadLetterContinuationInput) (*runtime.ContinuationJobInspection, error)
}

// Service is the explicit composition of HTTP capability facets. Each facet is
// independently replaceable by the new Runtime packages; Handler never depends
// on a concrete application type.
type Service interface {
	ThreadQuery
	TextRunCommands
	RunCommands
	RunQueries
	RunQueueCommands
	OutputQueries
	AgentCommands
	TeamCommands
	WorkflowCommands
	ContinuationCommands
}
