package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/planexecute"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

// TeamFeature is the narrow Team Runtime capability consumed by Harness.
type TeamFeature interface {
	StartRun(context.Context, team.StartRequest) (kernel.Snapshot, error)
	Resume(context.Context, string, uint64) (kernel.Snapshot, error)
}

// TeamTurnRequest starts Team as the top-level capability of one Harness Turn.
type TeamTurnRequest struct {
	StartRequest
	Mode    team.ExecutionMode
	Members []team.Member
	Join    handoff.Join
}

// PlanExecuteTurnRequest starts Plan-and-Execute as the top-level capability.
type PlanExecuteTurnRequest struct {
	StartRequest
	AllowedToolKeys []string
	ApprovalPolicy  planexecute.ApprovalPolicy
	MaxSteps        int
}

// WorkflowTurnRequest starts one compiled Dynamic Workflow as the top-level capability.
type WorkflowTurnRequest struct {
	StartRequest
	Definition workflow.Definition
	Input      json.RawMessage
}

type topLevelFeatureStart struct {
	turn       Turn
	invocation Invocation
	runContext context.Context
	replayed   bool
}

type topLevelFeatureEnvelope struct {
	request StartRequest
	turn    Turn
	config  ConfigSnapshot
	created bool
	now     time.Time
}

type teamInvocationInput struct {
	Goal    string             `json:"goal"`
	Actor   kernel.ActorRef    `json:"actor"`
	Thread  kernel.ThreadRef   `json:"thread"`
	Mode    team.ExecutionMode `json:"mode"`
	Members []team.Member      `json:"members"`
	Join    handoff.Join       `json:"join"`
}

type planExecuteInvocationInput struct {
	Goal            string                     `json:"goal"`
	Model           string                     `json:"model"`
	Actor           kernel.ActorRef            `json:"actor"`
	Thread          kernel.ThreadRef           `json:"thread"`
	AllowedToolKeys []string                   `json:"allowedToolKeys"`
	ApprovalPolicy  planexecute.ApprovalPolicy `json:"approvalPolicy"`
	MaxSteps        int                        `json:"maxSteps"`
}

type workflowInvocationInput struct {
	Goal       string              `json:"goal"`
	Actor      kernel.ActorRef     `json:"actor"`
	Thread     kernel.ThreadRef    `json:"thread"`
	Definition workflow.Definition `json:"definition"`
	Input      json.RawMessage     `json:"input"`
}

// StartTeamTurn starts Team without creating a placeholder Agent root.
func (runner *Runner) StartTeamTurn(ctx context.Context, request TeamTurnRequest) (Snapshot, error) {
	if runner == nil || runner.teams == nil {
		return Snapshot{}, ErrInvalidRequest
	}
	input, inputHash, err := marshalInvocationValue(teamInvocationInput{
		Goal: strings.TrimSpace(request.Goal), Actor: request.Actor, Thread: request.Thread,
		Mode: request.Mode, Members: append([]team.Member(nil), request.Members...), Join: request.Join,
	})
	if err != nil {
		return Snapshot{}, err
	}
	prepared, err := runner.prepareTopLevelFeatureStart(
		ctx, request.StartRequest, CapabilityTeam, RuntimeCapabilityVersion, ExecutionTeam, input, inputHash,
	)
	if err != nil {
		return Snapshot{}, err
	}
	if prepared.replayed {
		if snapshot, handled, replayErr := runner.replayTopLevelFeatureStart(ctx, prepared); handled || replayErr != nil {
			return snapshot, replayErr
		}
	}
	runtimeSnapshot, startErr := runner.teams.StartRun(prepared.runContext, team.StartRequest{
		ID: prepared.invocation.ExecutionRefID, Actor: request.Actor, Thread: request.Thread,
		RequestID: firstNonEmpty(strings.TrimSpace(request.RequestID), prepared.turn.ID), Goal: request.Goal,
		Mode: request.Mode, Members: append([]team.Member(nil), request.Members...), Join: request.Join,
	})
	return runner.finishTopLevelFeatureStart(ctx, prepared.turn, prepared.invocation, runtimeSnapshot, startErr)
}

// StartPlanExecuteTurn starts Plan-and-Execute without a placeholder Agent root.
func (runner *Runner) StartPlanExecuteTurn(ctx context.Context, request PlanExecuteTurnRequest) (Snapshot, error) {
	if runner == nil || runner.plans == nil {
		return Snapshot{}, ErrInvalidRequest
	}
	input, inputHash, err := marshalInvocationValue(planExecuteInvocationInput{
		Goal: strings.TrimSpace(request.Goal), Model: strings.TrimSpace(request.Config.Model),
		Actor: request.Actor, Thread: request.Thread,
		AllowedToolKeys: append([]string{}, request.AllowedToolKeys...),
		ApprovalPolicy:  request.ApprovalPolicy, MaxSteps: request.MaxSteps,
	})
	if err != nil {
		return Snapshot{}, err
	}
	prepared, err := runner.prepareTopLevelFeatureStart(
		ctx, request.StartRequest, CapabilityPlanExecute, RuntimeCapabilityVersion, ExecutionPlanExecute, input, inputHash,
	)
	if err != nil {
		return Snapshot{}, err
	}
	if prepared.replayed {
		if snapshot, handled, replayErr := runner.replayTopLevelFeatureStart(ctx, prepared); handled || replayErr != nil {
			return snapshot, replayErr
		}
	}
	runtimeSnapshot, startErr := runner.plans.StartRun(prepared.runContext, planexecute.StartRequest{
		ID: prepared.invocation.ExecutionRefID, Actor: request.Actor, Thread: request.Thread,
		RequestID: firstNonEmpty(strings.TrimSpace(request.RequestID), prepared.turn.ID), Goal: request.Goal,
		AllowedToolKeys: append([]string{}, request.AllowedToolKeys...),
		Model:           request.Config.Model, ApprovalPolicy: request.ApprovalPolicy, MaxSteps: request.MaxSteps,
	})
	return runner.finishTopLevelFeatureStart(ctx, prepared.turn, prepared.invocation, runtimeSnapshot, startErr)
}

// StartWorkflowTurn starts Workflow without a placeholder Agent root.
func (runner *Runner) StartWorkflowTurn(ctx context.Context, request WorkflowTurnRequest) (Snapshot, error) {
	if runner == nil || runner.workflows == nil {
		return Snapshot{}, ErrInvalidRequest
	}
	input, inputHash, err := marshalInvocationValue(workflowInvocationInput{
		Goal: strings.TrimSpace(request.Goal), Actor: request.Actor, Thread: request.Thread,
		Definition: request.Definition, Input: append(json.RawMessage(nil), request.Input...),
	})
	if err != nil {
		return Snapshot{}, err
	}
	prepared, err := runner.prepareTopLevelFeatureStart(
		ctx, request.StartRequest, CapabilityWorkflow, RuntimeCapabilityVersion, ExecutionWorkflow, input, inputHash,
	)
	if err != nil {
		return Snapshot{}, err
	}
	if prepared.replayed {
		if snapshot, handled, replayErr := runner.replayTopLevelFeatureStart(ctx, prepared); handled || replayErr != nil {
			return snapshot, replayErr
		}
	}
	runtimeSnapshot, startErr := runner.workflows.StartRun(prepared.runContext, workflow.StartRequest{
		ID: prepared.invocation.ExecutionRefID, Actor: request.Actor, Thread: request.Thread,
		RequestID: firstNonEmpty(strings.TrimSpace(request.RequestID), prepared.turn.ID), Goal: request.Goal,
		Definition: request.Definition, Input: append(json.RawMessage(nil), request.Input...),
	})
	return runner.finishTopLevelFeatureStart(ctx, prepared.turn, prepared.invocation, runtimeSnapshot, startErr)
}

func (runner *Runner) prepareTopLevelFeatureStart(
	ctx context.Context,
	request StartRequest,
	capabilityKey string,
	definitionVersion string,
	executionClass ExecutionClass,
	input json.RawMessage,
	inputHash string,
) (topLevelFeatureStart, error) {
	envelope, err := runner.prepareTopLevelFeatureEnvelope(ctx, request)
	if err != nil {
		return topLevelFeatureStart{}, err
	}
	invocation, err := runner.prepareTopLevelFeatureInvocation(
		ctx, envelope, capabilityKey, definitionVersion, executionClass, input, inputHash,
	)
	if err != nil {
		return topLevelFeatureStart{}, err
	}
	if !envelope.created {
		turn, runCtx, contextErr := runner.restoreOrBuildContext(
			ctx, envelope.turn, envelope.request.Context, envelope.config,
		)
		if contextErr != nil {
			_, failErr := runner.failTopLevelInvocationAndTurn(ctx, envelope.turn, invocation, contextErr)
			return topLevelFeatureStart{}, errors.Join(contextErr, failErr)
		}
		runCtx = withContextWindowBinding(runCtx, turn.ID, ContextWindowReadOnly)
		return topLevelFeatureStart{turn: turn, invocation: invocation, runContext: runCtx, replayed: true}, nil
	}
	turn, runCtx, err := runner.prepareTopLevelFeatureContext(ctx, envelope, invocation)
	if err != nil {
		return topLevelFeatureStart{}, err
	}
	return topLevelFeatureStart{turn: turn, invocation: invocation, runContext: runCtx}, nil
}

func (runner *Runner) prepareTopLevelFeatureEnvelope(
	ctx context.Context,
	request StartRequest,
) (topLevelFeatureEnvelope, error) {
	request, sessionID, turnID, err := normalizeStartRequest(request)
	if err != nil {
		return topLevelFeatureEnvelope{}, err
	}
	now := runner.clock.Now().UTC()
	config, err := SealConfigSnapshot(turnID, request.Config, now)
	if err != nil {
		return topLevelFeatureEnvelope{}, err
	}
	turn, created, err := runner.persistStartEnvelope(ctx, request, sessionID, turnID, config, now)
	if err != nil {
		return topLevelFeatureEnvelope{}, err
	}
	return topLevelFeatureEnvelope{request: request, turn: turn, config: config, created: created, now: now}, nil
}

func (runner *Runner) prepareTopLevelFeatureInvocation(
	ctx context.Context,
	envelope topLevelFeatureEnvelope,
	capabilityKey string,
	definitionVersion string,
	executionClass ExecutionClass,
	input json.RawMessage,
	inputHash string,
) (Invocation, error) {
	requestID := firstNonEmpty(strings.TrimSpace(envelope.request.RequestID), envelope.turn.ID)
	invocationID, err := InvocationID(envelope.turn.ID, "", capabilityKey, requestID)
	if err != nil {
		return Invocation{}, err
	}
	invocation, fresh, err := runner.store.CreateInvocation(ctx, Invocation{
		ID: invocationID, TurnID: envelope.turn.ID, CapabilityKey: capabilityKey,
		DefinitionVersion: strings.TrimSpace(definitionVersion), ExecutionClass: executionClass,
		Input: append(json.RawMessage(nil), input...), InputHash: inputHash,
		ExecutionRefID: CapabilityExecutionRefID(invocationID), Status: InvocationAccepted,
		Attempt: 1, OutputRefs: []HostRef{}, Revision: 1, CreatedAt: envelope.now, UpdatedAt: envelope.now,
	})
	if err != nil {
		return Invocation{}, err
	}
	if envelope.created && !fresh {
		return Invocation{}, ErrConflict
	}
	if err = runner.recordInvocationItem(ctx, invocation); err != nil {
		return Invocation{}, err
	}
	return invocation, nil
}

func (runner *Runner) prepareTopLevelFeatureContext(
	ctx context.Context,
	envelope topLevelFeatureEnvelope,
	invocation Invocation,
) (Turn, context.Context, error) {
	turn := envelope.turn
	request := envelope.request
	if err := runner.recordHostMessageItems(ctx, turn, request.InputMessage, request.OutputMessage); err != nil {
		return Turn{}, nil, err
	}
	runner.publishTurnStatus(ctx, turn, EventTurnStarted, false)
	updated, runCtx, err := runner.restoreOrBuildContext(
		ctx, turn, request.Context, envelope.config,
	)
	if err != nil {
		_, failErr := runner.failTopLevelInvocationAndTurn(ctx, turn, invocation, err)
		return Turn{}, nil, errors.Join(err, failErr)
	}
	return updated, withContextWindowBinding(runCtx, updated.ID, ContextWindowReadOnly), nil
}

func (runner *Runner) replayTopLevelFeatureStart(
	ctx context.Context,
	prepared topLevelFeatureStart,
) (Snapshot, bool, error) {
	if terminalTurnStatus(prepared.turn.Status) || terminalInvocationStatus(prepared.invocation.Status) {
		snapshot, err := runner.loadSnapshot(ctx, prepared.turn, nil)
		return snapshot, true, err
	}
	runtimeSnapshot, err := runner.runtime.Load(ctx, prepared.invocation.ExecutionRefID)
	if err == nil {
		if runtimeSnapshot.Run.Status != kernel.RunStatusRunning {
			snapshot, syncErr := runner.syncRuntimeSnapshotWithRetry(ctx, prepared.turn, prepared.invocation, runtimeSnapshot)
			return snapshot, true, syncErr
		}
		resumed, resumeErr := runner.resumeFeature(
			prepared.runContext, prepared.invocation, runtimeSnapshot.Run.Revision,
		)
		// A replay can race the execution owner that accepted the same durable
		// invocation. The Runtime CAS then correctly rejects this second resume,
		// but the Harness request is still an idempotent success: another worker
		// has advanced (or is advancing) the exact same run. Refresh from the
		// durable Runtime snapshot instead of failing the product projection.
		if errors.Is(resumeErr, kernel.ErrConflict) {
			snapshot, refreshErr := runner.Refresh(ctx, prepared.turn.ID)
			return snapshot, true, refreshErr
		}
		if resumed.Run.ID == "" {
			return Snapshot{}, true, resumeErr
		}
		snapshot, syncErr := runner.syncRuntimeSnapshotWithRetry(ctx, prepared.turn, prepared.invocation, resumed)
		return snapshot, true, normalizedFeatureStartError(
			prepared.invocation.ExecutionClass, resumed, resumeErr, syncErr,
		)
	}
	if errors.Is(err, kernel.ErrNotFound) && prepared.invocation.Status == InvocationAccepted {
		return Snapshot{}, false, nil
	}
	return Snapshot{}, true, err
}

func (runner *Runner) finishTopLevelFeatureStart(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	runtimeSnapshot kernel.Snapshot,
	startErr error,
) (Snapshot, error) {
	if runtimeSnapshot.Run.ID == "" {
		if errors.Is(startErr, kernel.ErrConflict) {
			recovered, recoverErr := runner.Refresh(ctx, turn.ID)
			if recoverErr == nil && recovered.Turn.Status == TurnCancelled {
				return recovered, nil
			}
		}
		failed, failErr := runner.failTopLevelInvocationAndTurn(ctx, turn, invocation, startErr)
		return failed, errors.Join(startErr, failErr)
	}
	snapshot, syncErr := runner.syncRuntimeSnapshotWithRetry(ctx, turn, invocation, runtimeSnapshot)
	return snapshot, normalizedFeatureStartError(invocation.ExecutionClass, runtimeSnapshot, startErr, syncErr)
}

// PlanExecuteFeature is the narrow Plan-and-Execute Runtime capability consumed by Harness.
type PlanExecuteFeature interface {
	StartRun(context.Context, planexecute.StartRequest) (kernel.Snapshot, error)
	Resume(context.Context, string, uint64) (kernel.Snapshot, error)
}

// WorkflowFeature is the narrow Dynamic Workflow Runtime capability consumed by Harness.
type WorkflowFeature interface {
	StartRun(context.Context, workflow.StartRequest) (kernel.Snapshot, error)
	Resume(context.Context, string, uint64) (kernel.Snapshot, error)
	ResolveWait(context.Context, string, uint64, json.RawMessage) (kernel.Snapshot, error)
}

// TeamInvocationRequest starts one exact first-party Team capability.
type TeamInvocationRequest struct {
	ParentItemID string
	RequestID    string
	Goal         string
	Mode         team.ExecutionMode
	Members      []team.Member
	Join         handoff.Join
}

// PlanExecuteInvocationRequest starts one exact first-party Plan-and-Execute capability.
type PlanExecuteInvocationRequest struct {
	ParentItemID    string
	RequestID       string
	Goal            string
	Model           string
	AllowedToolKeys []string
	ApprovalPolicy  planexecute.ApprovalPolicy
	MaxSteps        int
}

// WorkflowInvocationRequest starts one exact first-party Dynamic Workflow capability.
type WorkflowInvocationRequest struct {
	ParentItemID string
	RequestID    string
	Goal         string
	Definition   workflow.Definition
	Input        json.RawMessage
}

type childInvocationContext struct {
	turn                 Turn
	parentExecutionRefID string
	actor                kernel.ActorRef
	thread               kernel.ThreadRef
}

// StartTeamInvocation starts Team as one child Invocation in an existing Harness Turn.
func (runner *Runner) StartTeamInvocation(
	ctx context.Context,
	turnID string,
	request TeamInvocationRequest,
) (Snapshot, error) {
	if runner == nil || runner.teams == nil {
		return Snapshot{}, ErrInvalidRequest
	}
	input, inputHash, err := marshalInvocationValue(teamInvocationInput{
		Goal: strings.TrimSpace(request.Goal), Mode: request.Mode, Members: append([]team.Member(nil), request.Members...), Join: request.Join,
	})
	if err != nil {
		return Snapshot{}, err
	}
	invocation, child, replayed, err := runner.beginChildInvocation(
		ctx, turnID, request.ParentItemID, request.RequestID,
		CapabilityTeam, RuntimeCapabilityVersion, ExecutionTeam, input, inputHash,
	)
	if err != nil {
		return Snapshot{}, err
	}
	if replayed {
		if snapshot, handled, replayErr := runner.replayChildInvocationStart(ctx, child.turn, invocation); handled || replayErr != nil {
			return snapshot, replayErr
		}
	}
	runtimeSnapshot, startErr := runner.teams.StartRun(ctx, team.StartRequest{
		ID: invocation.ExecutionRefID, Actor: child.actor, Thread: child.thread,
		RequestID: strings.TrimSpace(request.RequestID), Goal: strings.TrimSpace(request.Goal),
		Mode: request.Mode, Members: append([]team.Member(nil), request.Members...), Join: request.Join,
	})
	return runner.finishChildInvocationStart(ctx, child.turn, invocation, runtimeSnapshot, startErr)
}

// StartPlanExecuteInvocation starts Plan-and-Execute as one child Invocation.
func (runner *Runner) StartPlanExecuteInvocation(
	ctx context.Context,
	turnID string,
	request PlanExecuteInvocationRequest,
) (Snapshot, error) {
	if runner == nil || runner.plans == nil {
		return Snapshot{}, ErrInvalidRequest
	}
	input, inputHash, err := marshalInvocationValue(planExecuteInvocationInput{
		Goal: strings.TrimSpace(request.Goal), Model: strings.TrimSpace(request.Model),
		AllowedToolKeys: append([]string{}, request.AllowedToolKeys...),
		ApprovalPolicy:  request.ApprovalPolicy, MaxSteps: request.MaxSteps,
	})
	if err != nil {
		return Snapshot{}, err
	}
	invocation, child, replayed, err := runner.beginChildInvocation(
		ctx, turnID, request.ParentItemID, request.RequestID,
		CapabilityPlanExecute, RuntimeCapabilityVersion, ExecutionPlanExecute, input, inputHash,
	)
	if err != nil {
		return Snapshot{}, err
	}
	if replayed {
		if snapshot, handled, replayErr := runner.replayChildInvocationStart(ctx, child.turn, invocation); handled || replayErr != nil {
			return snapshot, replayErr
		}
	}
	runtimeSnapshot, startErr := runner.plans.StartRun(ctx, planexecute.StartRequest{
		ID: invocation.ExecutionRefID, Actor: child.actor, Thread: child.thread,
		RequestID: strings.TrimSpace(request.RequestID), Goal: strings.TrimSpace(request.Goal),
		AllowedToolKeys: append([]string{}, request.AllowedToolKeys...),
		Model:           strings.TrimSpace(request.Model), ApprovalPolicy: request.ApprovalPolicy, MaxSteps: request.MaxSteps,
	})
	return runner.finishChildInvocationStart(ctx, child.turn, invocation, runtimeSnapshot, startErr)
}

// StartWorkflowInvocation starts one exact compiled Dynamic Workflow as a child Invocation.
func (runner *Runner) StartWorkflowInvocation(
	ctx context.Context,
	turnID string,
	request WorkflowInvocationRequest,
) (Snapshot, error) {
	if runner == nil || runner.workflows == nil {
		return Snapshot{}, ErrInvalidRequest
	}
	input, inputHash, err := marshalInvocationValue(workflowInvocationInput{
		Goal: strings.TrimSpace(request.Goal), Definition: request.Definition, Input: append(json.RawMessage(nil), request.Input...),
	})
	if err != nil {
		return Snapshot{}, err
	}
	invocation, child, replayed, err := runner.beginChildInvocation(
		ctx, turnID, request.ParentItemID, request.RequestID,
		CapabilityWorkflow, RuntimeCapabilityVersion, ExecutionWorkflow, input, inputHash,
	)
	if err != nil {
		return Snapshot{}, err
	}
	if replayed {
		if snapshot, handled, replayErr := runner.replayChildInvocationStart(ctx, child.turn, invocation); handled || replayErr != nil {
			return snapshot, replayErr
		}
	}
	runtimeSnapshot, startErr := runner.workflows.StartRun(ctx, workflow.StartRequest{
		ID: invocation.ExecutionRefID, Actor: child.actor, Thread: child.thread,
		RequestID: strings.TrimSpace(request.RequestID), Goal: strings.TrimSpace(request.Goal),
		Definition: request.Definition, Input: append(json.RawMessage(nil), request.Input...),
	})
	return runner.finishChildInvocationStart(ctx, child.turn, invocation, runtimeSnapshot, startErr)
}

// RefreshInvocation reconciles one child Runtime execution into its durable Harness Invocation.
func (runner *Runner) RefreshInvocation(ctx context.Context, invocationID string) (Snapshot, error) {
	invocation, err := runner.store.GetInvocation(ctx, strings.TrimSpace(invocationID))
	if err != nil {
		return Snapshot{}, err
	}
	if invocation.ExecutionClass == ExecutionApplication {
		turn, loadErr := runner.store.GetTurn(ctx, invocation.TurnID)
		if loadErr != nil {
			return Snapshot{}, loadErr
		}
		return runner.refreshApplicationInvocation(ctx, invocation, turn)
	}
	invocation, turn, runtimeSnapshot, err := runner.loadInvocationRuntime(ctx, invocationID)
	if err != nil {
		return Snapshot{}, err
	}
	return runner.syncChildInvocationSnapshot(ctx, turn, invocation, runtimeSnapshot)
}

// ResumeInvocation resumes a running child Feature through the exact typed adapter selected at creation.
func (runner *Runner) ResumeInvocation(ctx context.Context, invocationID string) (Snapshot, error) {
	invocation, err := runner.store.GetInvocation(ctx, strings.TrimSpace(invocationID))
	if err != nil {
		return Snapshot{}, err
	}
	if invocation.ExecutionClass == ExecutionApplication {
		turn, loadErr := runner.store.GetTurn(ctx, invocation.TurnID)
		if loadErr != nil {
			return Snapshot{}, loadErr
		}
		return runner.refreshApplicationInvocation(ctx, invocation, turn)
	}
	invocation, turn, runtimeSnapshot, err := runner.loadInvocationRuntime(ctx, invocationID)
	if err != nil {
		return Snapshot{}, err
	}
	if runtimeSnapshot.Run.Status != kernel.RunStatusRunning {
		return runner.syncChildInvocationSnapshot(ctx, turn, invocation, runtimeSnapshot)
	}
	resumed, resumeErr := runner.resumeFeature(ctx, invocation, runtimeSnapshot.Run.Revision)
	if resumed.Run.ID == "" {
		return Snapshot{}, resumeErr
	}
	snapshot, syncErr := runner.syncChildInvocationSnapshot(ctx, turn, invocation, resumed)
	return snapshot, errors.Join(resumeErr, syncErr)
}

// RetryInvocation starts the next durable attempt of one failed or cancelled
// Feature Invocation. Replaying a partially-started retry repairs the same
// attempt rather than allocating another execution identity.
func (runner *Runner) RetryInvocation(ctx context.Context, invocationID string) (Snapshot, error) {
	invocation, err := runner.store.GetInvocation(ctx, strings.TrimSpace(invocationID))
	if err != nil {
		return Snapshot{}, err
	}
	if len(invocation.Input) == 0 {
		return Snapshot{}, ErrConflict
	}
	turn, err := runner.store.GetTurn(ctx, invocation.TurnID)
	if err != nil || (strings.TrimSpace(invocation.ParentItemID) != "" && terminalTurnStatus(turn.Status)) {
		return Snapshot{}, errors.Join(ErrConflict, err)
	}
	if invocation.Attempt > 1 && !retryableInvocationStatus(invocation.Status) {
		return runner.replayRetriedInvocation(ctx, turn, invocation)
	}
	if !retryableInvocationStatus(invocation.Status) {
		return Snapshot{}, ErrConflict
	}
	return runner.startNextInvocationAttempt(ctx, turn, invocation)
}

func (runner *Runner) startNextInvocationAttempt(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
) (Snapshot, error) {
	nextAttempt := invocation.Attempt + 1
	nextRef := invocationExecutionRefID(invocation.ID, invocation.ExecutionClass, nextAttempt)
	retried, err := runner.store.RetryInvocation(ctx, invocation.ID, invocation.Revision, nextRef, runner.clock.Now().UTC())
	if err != nil {
		return Snapshot{}, err
	}
	if err = runner.recordInvocationItem(ctx, retried); err != nil {
		return Snapshot{}, err
	}
	turn, err = runner.store.GetTurn(ctx, turn.ID)
	if err != nil {
		return Snapshot{}, err
	}
	return runner.startRetriedInvocation(ctx, turn, retried)
}

func (runner *Runner) replayRetriedInvocation(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
) (Snapshot, error) {
	if invocation.ExecutionClass == ExecutionApplication {
		if invocation.Status == InvocationAccepted || invocation.Status == InvocationRunning {
			return runner.executeApplicationInvocation(ctx, turn, invocation)
		}
		return runner.loadSnapshot(ctx, turn, nil)
	}
	loaded, err := runner.runtime.Load(ctx, invocation.ExecutionRefID)
	if err == nil {
		if strings.TrimSpace(invocation.ParentItemID) == "" {
			return runner.syncRuntimeSnapshotWithRetry(ctx, turn, invocation, loaded)
		}
		return runner.syncChildInvocationSnapshot(ctx, turn, invocation, loaded)
	}
	if !errors.Is(err, kernel.ErrNotFound) || invocation.Status != InvocationAccepted {
		return Snapshot{}, err
	}
	if err = runner.recordInvocationItem(ctx, invocation); err != nil {
		return Snapshot{}, err
	}
	return runner.startRetriedInvocation(ctx, turn, invocation)
}

func (runner *Runner) startRetriedInvocation(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
) (Snapshot, error) {
	if invocation.ExecutionClass == ExecutionApplication {
		return runner.executeApplicationInvocation(ctx, turn, invocation)
	}
	if strings.TrimSpace(invocation.ParentItemID) == "" {
		return runner.startRetriedTopLevelInvocation(ctx, turn, invocation)
	}
	child, err := runner.childInvocationContext(ctx, turn.ID, invocation.ParentItemID)
	if err != nil {
		return Snapshot{}, err
	}
	if runner.relations != nil {
		if _, err = runner.relations.Ensure(ctx, runrelation.Draft{
			ParentRunID: child.parentExecutionRefID, ChildRunID: invocation.ExecutionRefID,
			Kind: runrelation.KindCapability, OwnerNodeID: invocationRelationOwnerID(invocation),
		}); err != nil {
			return Snapshot{}, err
		}
	}
	runtimeSnapshot, startErr := runner.startInvocationAttempt(ctx, invocation, child)
	return runner.finishChildInvocationStart(ctx, turn, invocation, runtimeSnapshot, startErr)
}

func (runner *Runner) startRetriedTopLevelInvocation(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
) (Snapshot, error) {
	actor, thread, err := runner.topLevelInvocationIdentity(ctx, turn, invocation)
	if err != nil {
		return Snapshot{}, err
	}
	runCtx, err := runner.topLevelRetryContext(ctx, turn)
	if err != nil {
		return Snapshot{}, err
	}
	runner.publishTurnStatus(ctx, turn, EventTurnStarted, false)
	runtimeSnapshot, startErr := runner.startInvocationAttempt(runCtx, invocation, childInvocationContext{
		turn: turn, actor: actor, thread: thread,
	})
	return runner.finishTopLevelFeatureStart(ctx, turn, invocation, runtimeSnapshot, startErr)
}

func (runner *Runner) topLevelInvocationIdentity(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
) (kernel.ActorRef, kernel.ThreadRef, error) {
	var input struct {
		Actor  kernel.ActorRef  `json:"actor"`
		Thread kernel.ThreadRef `json:"thread"`
	}
	if err := json.Unmarshal(invocation.Input, &input); err != nil {
		return kernel.ActorRef{}, kernel.ThreadRef{}, ErrConflict
	}
	if validActor(input.Actor) && strings.TrimSpace(input.Thread.Kind) != "" && strings.TrimSpace(input.Thread.ID) != "" {
		return input.Actor, input.Thread, nil
	}
	session, err := runner.store.GetSession(ctx, turn.SessionID)
	if err != nil || !validActor(session.Actor) || strings.TrimSpace(session.HostThread.Kind) == "" ||
		strings.TrimSpace(session.HostThread.ID) == "" {
		return kernel.ActorRef{}, kernel.ThreadRef{}, errors.Join(ErrConflict, err)
	}
	return session.Actor, kernel.ThreadRef{Kind: session.HostThread.Kind, ID: session.HostThread.ID}, nil
}

func (runner *Runner) topLevelRetryContext(ctx context.Context, turn Turn) (context.Context, error) {
	checkpointID := strings.TrimSpace(turn.ContextCheckpointID)
	if checkpointID == "" {
		return ctx, nil
	}
	checkpoint, err := runner.store.GetContextCheckpoint(ctx, checkpointID)
	if err != nil {
		return nil, err
	}
	if checkpoint.ScopeID != turn.SessionID || !sameContextCheckpointRef(checkpoint, turn.ContextRef) || checkpoint.ID != checkpointID {
		return nil, ErrConflict
	}
	return withContextCheckpoint(ctx, checkpoint), nil
}

func (runner *Runner) startInvocationAttempt(
	ctx context.Context,
	invocation Invocation,
	child childInvocationContext,
) (kernel.Snapshot, error) {
	requestID := stableID("hretry", invocation.ID, attemptString(invocation.Attempt))
	switch invocation.ExecutionClass {
	case ExecutionAgent:
		return runner.startRetriedAgent(ctx, invocation, child, requestID)
	case ExecutionTeam:
		return runner.startRetriedTeam(ctx, invocation, child, requestID)
	case ExecutionPlanExecute:
		return runner.startRetriedPlanExecute(ctx, invocation, child, requestID)
	case ExecutionWorkflow:
		return runner.startRetriedWorkflow(ctx, invocation, child, requestID)
	case ExecutionApplication:
		return kernel.Snapshot{}, ErrInvalidRequest
	default:
		return kernel.Snapshot{}, ErrInvalidRequest
	}
}

func (runner *Runner) startRetriedAgent(ctx context.Context, invocation Invocation, child childInvocationContext, requestID string) (kernel.Snapshot, error) {
	var input agentInvocationInput
	if err := json.Unmarshal(invocation.Input, &input); err != nil {
		return kernel.Snapshot{}, ErrConflict
	}
	config, err := runner.store.GetConfigSnapshot(ctx, child.turn.ConfigSnapshotID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.agent.StartRun(ctx, agent.StartRequest{
		ID: invocation.ExecutionRefID, Actor: child.actor, Thread: child.thread,
		RequestID: requestID, Goal: input.Goal, Model: config.Model,
		ModelOptions: append(json.RawMessage(nil), config.ModelOptions...), ToolKeys: append([]string(nil), config.ToolKeys...),
		RequiredToolKeys: append([]string(nil), input.RequiredToolKeys...), Limits: config.Limits,
	})
}

func (runner *Runner) startRetriedTeam(ctx context.Context, invocation Invocation, child childInvocationContext, requestID string) (kernel.Snapshot, error) {
	if runner.teams == nil {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	var input teamInvocationInput
	if err := json.Unmarshal(invocation.Input, &input); err != nil {
		return kernel.Snapshot{}, ErrConflict
	}
	return runner.teams.StartRun(ctx, team.StartRequest{ID: invocation.ExecutionRefID, Actor: child.actor, Thread: child.thread, RequestID: requestID, Goal: input.Goal, Mode: input.Mode, Members: append([]team.Member(nil), input.Members...), Join: input.Join})
}

func (runner *Runner) startRetriedPlanExecute(ctx context.Context, invocation Invocation, child childInvocationContext, requestID string) (kernel.Snapshot, error) {
	if runner.plans == nil {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	var input planExecuteInvocationInput
	if err := json.Unmarshal(invocation.Input, &input); err != nil {
		return kernel.Snapshot{}, ErrConflict
	}
	return runner.plans.StartRun(ctx, planexecute.StartRequest{ID: invocation.ExecutionRefID, Actor: child.actor, Thread: child.thread, RequestID: requestID, Goal: input.Goal, Model: input.Model, AllowedToolKeys: append([]string{}, input.AllowedToolKeys...), ApprovalPolicy: input.ApprovalPolicy, MaxSteps: input.MaxSteps})
}

func (runner *Runner) startRetriedWorkflow(ctx context.Context, invocation Invocation, child childInvocationContext, requestID string) (kernel.Snapshot, error) {
	if runner.workflows == nil {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	var input workflowInvocationInput
	if err := json.Unmarshal(invocation.Input, &input); err != nil {
		return kernel.Snapshot{}, ErrConflict
	}
	return runner.workflows.StartRun(ctx, workflow.StartRequest{ID: invocation.ExecutionRefID, Actor: child.actor, Thread: child.thread, RequestID: requestID, Goal: input.Goal, Definition: input.Definition, Input: append(json.RawMessage(nil), input.Input...)})
}

// CancelInvocation cancels one child Runtime Feature by its durable Invocation identity.
func (runner *Runner) CancelInvocation(ctx context.Context, invocationID, reason string) (Snapshot, error) {
	invocation, err := runner.store.GetInvocation(ctx, strings.TrimSpace(invocationID))
	if err != nil {
		return Snapshot{}, err
	}
	if strings.TrimSpace(invocation.ParentItemID) == "" {
		return Snapshot{}, ErrConflict
	}
	turn, err := runner.store.GetTurn(ctx, invocation.TurnID)
	if err != nil {
		return Snapshot{}, err
	}
	if invocation.ExecutionClass == ExecutionApplication {
		return runner.cancelApplicationInvocation(ctx, turn, invocation, reason)
	}
	runtimeSnapshot, found, err := runner.cancelRuntimeRun(ctx, invocation.ExecutionRefID, reason)
	if err != nil {
		return Snapshot{}, err
	}
	if !found {
		runtimeSnapshot = cancelledRuntimeSnapshot(invocation.ExecutionRefID, reason)
	}
	runs := map[string]kernel.Snapshot{invocation.ExecutionRefID: runtimeSnapshot}
	if runtimeSnapshot.Run.Status == kernel.RunStatusCancelled {
		descendants, cancelErr := runner.cancelRelatedRuns(ctx, invocation.ExecutionRefID, nil, reason)
		if cancelErr != nil {
			return Snapshot{}, cancelErr
		}
		for runID, snapshot := range descendants {
			runs[runID] = snapshot
		}
	}
	if err = runner.syncInvocationRuns(ctx, turn, runs); err != nil {
		return Snapshot{}, err
	}
	return runner.loadSnapshot(ctx, turn, nil)
}

func (runner *Runner) beginChildInvocation(
	ctx context.Context,
	turnID, parentItemID, requestID, capabilityKey, definitionVersion string,
	executionClass ExecutionClass,
	input json.RawMessage,
	inputHash string,
) (Invocation, childInvocationContext, bool, error) {
	child, err := runner.childInvocationContext(ctx, turnID, parentItemID)
	if err != nil {
		return Invocation{}, childInvocationContext{}, false, err
	}
	invocationID, err := InvocationID(turnID, parentItemID, capabilityKey, requestID)
	if err != nil {
		return Invocation{}, childInvocationContext{}, false, err
	}
	now := runner.clock.Now().UTC()
	value := Invocation{
		ID: invocationID, TurnID: child.turn.ID, ParentItemID: strings.TrimSpace(parentItemID),
		CapabilityKey: capabilityKey, DefinitionVersion: strings.TrimSpace(definitionVersion), ExecutionClass: executionClass,
		Input: append(json.RawMessage(nil), input...), InputHash: inputHash,
		ExecutionRefID: CapabilityExecutionRefID(invocationID), Status: InvocationAccepted,
		Attempt: 1, OutputRefs: []HostRef{}, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	created, fresh, err := runner.store.CreateInvocation(ctx, value)
	if err != nil {
		return Invocation{}, childInvocationContext{}, false, err
	}
	if err = runner.recordInvocationItem(ctx, created); err != nil {
		return Invocation{}, childInvocationContext{}, false, err
	}
	if runner.relations != nil && executionClass != ExecutionApplication {
		if _, err = runner.relations.Ensure(ctx, runrelation.Draft{
			ParentRunID: child.parentExecutionRefID, ChildRunID: created.ExecutionRefID,
			Kind: runrelation.KindCapability, OwnerNodeID: invocationRelationOwnerID(created),
		}); err != nil {
			return Invocation{}, childInvocationContext{}, false, err
		}
	}
	return created, child, !fresh, nil
}

func (runner *Runner) childInvocationContext(
	ctx context.Context,
	turnID, parentItemID string,
) (childInvocationContext, error) {
	turn, err := runner.store.GetTurn(ctx, strings.TrimSpace(turnID))
	if err != nil || terminalTurnStatus(turn.Status) {
		return childInvocationContext{}, errors.Join(ErrConflict, err)
	}
	if err = runner.validateParentItem(ctx, turn.ID, parentItemID); err != nil {
		return childInvocationContext{}, err
	}
	parent, err := loadTopLevelInvocation(ctx, runner.store, turn.ID)
	if err != nil || strings.TrimSpace(parent.ExecutionRefID) == "" {
		return childInvocationContext{}, errors.Join(ErrConflict, err)
	}
	runtimeSnapshot, err := runner.runtime.Load(ctx, parent.ExecutionRefID)
	if err != nil || terminalRuntimeStatus(runtimeSnapshot.Run.Status) {
		return childInvocationContext{}, errors.Join(ErrConflict, err)
	}
	return childInvocationContext{
		turn: turn, parentExecutionRefID: parent.ExecutionRefID,
		actor: runtimeSnapshot.Run.Actor, thread: runtimeSnapshot.Run.Thread,
	}, nil
}

func (runner *Runner) validateParentItem(ctx context.Context, turnID, parentItemID string) error {
	parentItemID = strings.TrimSpace(parentItemID)
	if parentItemID == "" {
		return nil
	}
	items, err := listAllItems(ctx, runner.store, turnID)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.ID == parentItemID {
			return nil
		}
	}
	return ErrInvalidRequest
}

func (runner *Runner) replayChildInvocationStart(
	ctx context.Context,
	turn Turn,
	invocation Invocation,

) (Snapshot, bool, error) {
	runtimeSnapshot, err := runner.runtime.Load(ctx, invocation.ExecutionRefID)
	if err == nil {
		snapshot, syncErr := runner.syncChildInvocationSnapshot(ctx, turn, invocation, runtimeSnapshot)
		return snapshot, true, syncErr
	}
	if errors.Is(err, kernel.ErrNotFound) && invocation.Status == InvocationAccepted {
		return Snapshot{}, false, nil
	}
	if errors.Is(err, kernel.ErrNotFound) && terminalInvocationStatus(invocation.Status) {
		snapshot, loadErr := runner.loadSnapshot(ctx, turn, nil)
		return snapshot, true, loadErr
	}
	return Snapshot{}, true, err
}

func (runner *Runner) finishChildInvocationStart(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	runtimeSnapshot kernel.Snapshot,
	startErr error,
) (Snapshot, error) {
	if runtimeSnapshot.Run.ID == "" {
		return runner.failChildInvocation(ctx, turn, invocation, startErr)
	}
	snapshot, syncErr := runner.syncChildInvocationSnapshot(ctx, turn, invocation, runtimeSnapshot)
	return snapshot, normalizedFeatureStartError(invocation.ExecutionClass, runtimeSnapshot, startErr, syncErr)
}

func normalizedFeatureStartError(
	executionClass ExecutionClass,
	runtimeSnapshot kernel.Snapshot,
	startErr error,
	syncErr error,
) error {
	if syncErr != nil {
		return errors.Join(startErr, syncErr)
	}
	if startErr == nil || !pendingRuntimeStatus(runtimeSnapshot.Run.Status) {
		return startErr
	}
	if expectedFeaturePendingError(executionClass, startErr) {
		return nil
	}
	return startErr
}

func expectedFeaturePendingError(executionClass ExecutionClass, err error) bool {
	switch executionClass {
	case ExecutionTeam:
		return errors.Is(err, team.ErrMemberPending)
	case ExecutionPlanExecute:
		return errors.Is(err, planexecute.ErrApprovalRequired) || errors.Is(err, planexecute.ErrStepPending)
	case ExecutionWorkflow:
		return errors.Is(err, workflow.ErrEffectPending) || errors.Is(err, workflow.ErrWaitPending) ||
			errors.Is(err, workflow.ErrSegmentYielded)
	case ExecutionAgent, ExecutionApplication:
		return false
	default:
		return false
	}
}

func pendingRuntimeStatus(status kernel.RunStatus) bool {
	return status == kernel.RunStatusRunning || status == kernel.RunStatusWaitingInput
}

func (runner *Runner) failChildInvocation(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	cause error,
) (Snapshot, error) {
	invocation.Status = InvocationFailed
	invocation.ErrorCode = "harness.capability_start_failed"
	invocation.ErrorDetail = "capability execution did not start"
	if cause != nil {
		invocation.ErrorDetail = strings.TrimSpace(cause.Error())
	}
	invocation.UpdatedAt = runner.clock.Now().UTC()
	updated, err := runner.store.UpdateInvocation(ctx, invocation, invocation.Revision)
	if err != nil {
		return Snapshot{}, err
	}
	if err = runner.recordInvocationItem(ctx, updated); err != nil {
		return Snapshot{}, err
	}
	snapshot, loadErr := runner.loadSnapshot(ctx, turn, nil)
	return snapshot, errors.Join(cause, loadErr)
}

func (runner *Runner) loadInvocationRuntime(
	ctx context.Context,
	invocationID string,
) (Invocation, Turn, kernel.Snapshot, error) {
	invocation, err := runner.store.GetInvocation(ctx, strings.TrimSpace(invocationID))
	if err != nil {
		return Invocation{}, Turn{}, kernel.Snapshot{}, err
	}
	turn, err := runner.store.GetTurn(ctx, invocation.TurnID)
	if err != nil {
		return Invocation{}, Turn{}, kernel.Snapshot{}, err
	}
	runtimeSnapshot, err := runner.runtime.Load(ctx, invocation.ExecutionRefID)
	return invocation, turn, runtimeSnapshot, err
}

func (runner *Runner) resumeFeature(
	ctx context.Context,
	invocation Invocation,
	expectedRevision uint64,
) (kernel.Snapshot, error) {
	switch invocation.ExecutionClass {
	case ExecutionTeam:
		if runner.teams == nil {
			return kernel.Snapshot{}, ErrInvalidRequest
		}
		return runner.teams.Resume(ctx, invocation.ExecutionRefID, expectedRevision)
	case ExecutionPlanExecute:
		if runner.plans == nil {
			return kernel.Snapshot{}, ErrInvalidRequest
		}
		return runner.plans.Resume(ctx, invocation.ExecutionRefID, expectedRevision)
	case ExecutionWorkflow:
		if runner.workflows == nil {
			return kernel.Snapshot{}, ErrInvalidRequest
		}
		return runner.workflows.Resume(ctx, invocation.ExecutionRefID, expectedRevision)
	case ExecutionAgent, ExecutionApplication:
		return kernel.Snapshot{}, ErrInvalidRequest
	default:
		return kernel.Snapshot{}, ErrInvalidRequest
	}
}

func (runner *Runner) resumeInvocationExecution(
	ctx context.Context,
	invocation Invocation,
	expectedRevision uint64,
) (kernel.Snapshot, error) {
	if invocation.ExecutionClass == ExecutionAgent {
		resumer, ok := runner.agent.(agentResumer)
		if !ok {
			return kernel.Snapshot{}, ErrInvalidRequest
		}
		return resumer.Resume(ctx, invocation.ExecutionRefID, expectedRevision)
	}
	return runner.resumeFeature(ctx, invocation, expectedRevision)
}

func (runner *Runner) syncChildInvocationSnapshot(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	runtimeSnapshot kernel.Snapshot,
) (Snapshot, error) {
	if runtimeSnapshotConflicts(invocation, runtimeSnapshot) {
		return Snapshot{}, ErrConflict
	}
	status, err := turnStatusFromRuntime(runtimeSnapshot.Run.Status)
	if err != nil {
		return Snapshot{}, err
	}
	invocation, err = runner.syncInvocationProjection(ctx, invocation, runtimeSnapshot, status)
	if err != nil {
		return Snapshot{}, err
	}
	if err = runner.projectChildInvocationOutcome(ctx, invocation, runtimeSnapshot); err != nil {
		return Snapshot{}, err
	}
	if err = runner.projectWorkflowWaitInteraction(ctx, turn, invocation, runtimeSnapshot); err != nil {
		return Snapshot{}, err
	}
	turn, err = runner.store.GetTurn(ctx, turn.ID)
	if err != nil {
		return Snapshot{}, err
	}
	return runner.loadSnapshot(ctx, turn, nil)
}

func (runner *Runner) projectChildInvocationOutcome(
	ctx context.Context,
	invocation Invocation,
	runtimeSnapshot kernel.Snapshot,
) error {
	if invocation.Status == InvocationCompleted && runtimeSnapshot.Result != nil {
		payload, err := json.Marshal(struct {
			ContentType string          `json:"contentType"`
			Content     json.RawMessage `json:"content"`
		}{runtimeSnapshot.Result.ContentType, runtimeSnapshot.Result.Content})
		if err != nil {
			return err
		}
		now := runner.clock.Now().UTC()
		_, err = appendItemFact(ctx, runner.store, runner.turnFeed, Item{
			ID: stableID("hico", invocation.ID, "result", attemptString(invocation.Attempt)), TurnID: invocation.TurnID,
			Kind: ItemArtifact, Status: ItemCompleted, RunID: invocation.ExecutionRefID,
			InvocationID: invocation.ID, ParentItemID: invocationLifecycleItemID(invocation, InvocationAccepted, 1),
			Payload: payload, CreatedAt: now, UpdatedAt: now,
		})
		return err
	}
	if invocation.Status != InvocationFailed && invocation.Status != InvocationCancelled {
		return nil
	}
	payload, err := json.Marshal(struct {
		ErrorCode   string `json:"errorCode,omitempty"`
		ErrorDetail string `json:"errorDetail,omitempty"`
	}{invocation.ErrorCode, invocation.ErrorDetail})
	if err != nil {
		return err
	}
	now := runner.clock.Now().UTC()
	_, err = appendItemFact(ctx, runner.store, runner.turnFeed, Item{
		ID: stableID("hicd", invocation.ID, string(invocation.Status), attemptString(invocation.Attempt)), TurnID: invocation.TurnID,
		Kind: ItemDiagnostic, Status: invocationItemStatus(invocation.Status), RunID: invocation.ExecutionRefID,
		InvocationID: invocation.ID, ParentItemID: invocationLifecycleItemID(invocation, InvocationAccepted, 1),
		Payload: payload, CreatedAt: now, UpdatedAt: now,
	})
	return err
}
