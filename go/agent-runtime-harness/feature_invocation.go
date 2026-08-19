package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/planexecute"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/team"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/workflow"
)

const (
	teamCapabilityKey        = "runtime.team"
	planExecuteCapabilityKey = "runtime.plan_execute"
	workflowCapabilityKey    = "runtime.workflow"
	runtimeCapabilityVersion = "v1"
)

// TeamFeature is the narrow Team Runtime capability consumed by Harness.
type TeamFeature interface {
	StartRun(context.Context, team.StartRequest) (kernel.Snapshot, error)
	Resume(context.Context, string, uint64) (kernel.Snapshot, error)
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
	ParentItemID   string
	RequestID      string
	Goal           string
	Model          string
	ApprovalPolicy planexecute.ApprovalPolicy
	MaxSteps       int
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
	inputHash, err := hashInvocationValue(struct {
		Goal    string
		Mode    team.ExecutionMode
		Members []team.Member
		Join    handoff.Join
	}{request.Goal, request.Mode, request.Members, request.Join})
	if err != nil {
		return Snapshot{}, err
	}
	invocation, child, replayed, err := runner.beginChildInvocation(
		ctx, turnID, request.ParentItemID, request.RequestID,
		teamCapabilityKey, ExecutionTeam, inputHash,
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
	inputHash, err := hashInvocationValue(struct {
		Goal           string
		Model          string
		ApprovalPolicy planexecute.ApprovalPolicy
		MaxSteps       int
	}{request.Goal, request.Model, request.ApprovalPolicy, request.MaxSteps})
	if err != nil {
		return Snapshot{}, err
	}
	invocation, child, replayed, err := runner.beginChildInvocation(
		ctx, turnID, request.ParentItemID, request.RequestID,
		planExecuteCapabilityKey, ExecutionPlanExecute, inputHash,
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
		Model: strings.TrimSpace(request.Model), ApprovalPolicy: request.ApprovalPolicy, MaxSteps: request.MaxSteps,
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
	inputHash, err := hashInvocationValue(struct {
		Goal       string
		Definition workflow.Definition
		Input      json.RawMessage
	}{request.Goal, request.Definition, request.Input})
	if err != nil {
		return Snapshot{}, err
	}
	invocation, child, replayed, err := runner.beginChildInvocation(
		ctx, turnID, request.ParentItemID, request.RequestID,
		workflowCapabilityKey, ExecutionWorkflow, inputHash,
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
	invocation, turn, runtimeSnapshot, err := runner.loadInvocationRuntime(ctx, invocationID)
	if err != nil {
		return Snapshot{}, err
	}
	return runner.syncChildInvocationSnapshot(ctx, turn, invocation, runtimeSnapshot)
}

// ResumeInvocation resumes a running child Feature through the exact typed adapter selected at creation.
func (runner *Runner) ResumeInvocation(ctx context.Context, invocationID string) (Snapshot, error) {
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

// CancelInvocation cancels one child Runtime Feature by its durable Invocation identity.
func (runner *Runner) CancelInvocation(ctx context.Context, invocationID, reason string) (Snapshot, error) {
	invocation, turn, runtimeSnapshot, err := runner.loadInvocationRuntime(ctx, invocationID)
	if err != nil {
		return Snapshot{}, err
	}
	if terminalRuntimeStatus(runtimeSnapshot.Run.Status) {
		return runner.syncChildInvocationSnapshot(ctx, turn, invocation, runtimeSnapshot)
	}
	cancelled, cancelErr := runner.runtime.Cancel(
		ctx, invocation.ExecutionRefID, runtimeSnapshot.Run.Revision, strings.TrimSpace(reason),
	)
	if cancelled.Run.ID == "" {
		return Snapshot{}, cancelErr
	}
	snapshot, syncErr := runner.syncChildInvocationSnapshot(ctx, turn, invocation, cancelled)
	return snapshot, errors.Join(cancelErr, syncErr)
}

func (runner *Runner) beginChildInvocation(
	ctx context.Context,
	turnID, parentItemID, requestID, capabilityKey string,
	executionClass ExecutionClass,
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
		CapabilityKey: capabilityKey, DefinitionVersion: runtimeCapabilityVersion, ExecutionClass: executionClass,
		InputHash: inputHash, ExecutionRefID: stableID("hcr", invocationID), Status: InvocationAccepted,
		Attempt: 1, OutputRefs: []HostRef{}, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	created, fresh, err := runner.store.CreateInvocation(ctx, value)
	if err != nil {
		return Invocation{}, childInvocationContext{}, false, err
	}
	if err = runner.recordInvocationItem(ctx, created); err != nil {
		return Invocation{}, childInvocationContext{}, false, err
	}
	if runner.relations != nil {
		if _, err = runner.relations.Ensure(ctx, runrelation.Draft{
			ParentRunID: child.parentExecutionRefID, ChildRunID: created.ExecutionRefID,
			Kind: runrelation.KindCapability, OwnerNodeID: created.ID,
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
	if err != nil {
		return childInvocationContext{}, err
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
	items, err := runner.store.ListItems(ctx, turnID, 0, defaultItemListLimit)
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
	return snapshot, errors.Join(startErr, syncErr)
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
	default:
		return kernel.Snapshot{}, ErrInvalidRequest
	}
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
			ID: stableID("hico", invocation.ID, "result"), TurnID: invocation.TurnID,
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
		ID: stableID("hicd", invocation.ID, string(invocation.Status)), TurnID: invocation.TurnID,
		Kind: ItemDiagnostic, Status: invocationItemStatus(invocation.Status), RunID: invocation.ExecutionRefID,
		InvocationID: invocation.ID, ParentItemID: invocationLifecycleItemID(invocation, InvocationAccepted, 1),
		Payload: payload, CreatedAt: now, UpdatedAt: now,
	})
	return err
}

func hashInvocationValue(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return hashInvocationInput(string(raw)), nil
}
