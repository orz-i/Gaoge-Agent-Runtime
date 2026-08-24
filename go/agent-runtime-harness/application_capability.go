package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// ApplicationCapabilityExecutor is the single statically composed host port
// for product/application capabilities. It is intentionally not a registry:
// the host implementation receives the already-authorized durable Invocation
// and owns only application semantics, while Harness owns lifecycle/wait/retry.
type ApplicationCapabilityExecutor interface {
	ExecuteApplicationCapability(context.Context, ApplicationCapabilityRequest) (ApplicationCapabilityResult, error)
}

type ApplicationCapabilityRequest struct {
	Invocation Invocation
	Session    Session
	Goal       string
	Input      json.RawMessage
}

type ApplicationCapabilityResult struct {
	OutputRefs  []HostRef
	Interaction *ApplicationInteractionRequest
}

type ApplicationInteractionRequest struct {
	ApplicationRef *HostRef
	ArtifactRefs   []HostRef
	Key            string
	Kind           InteractionKind
	Schema         json.RawMessage
	Presentation   json.RawMessage
}

type ApplicationTurnRequest struct {
	StartRequest
	CapabilityKey     string
	DefinitionVersion string
	Input             json.RawMessage
}

type ApplicationInvocationRequest struct {
	ParentItemID      string
	RequestID         string
	Goal              string
	CapabilityKey     string
	DefinitionVersion string
	Input             json.RawMessage
}

type applicationInvocationInput struct {
	Goal   string           `json:"goal"`
	Actor  kernel.ActorRef  `json:"actor,omitempty"`
	Thread kernel.ThreadRef `json:"thread,omitempty"`
	Input  json.RawMessage  `json:"input"`
}

func (runner *Runner) StartApplicationTurn(ctx context.Context, request ApplicationTurnRequest) (Snapshot, error) {
	if runner == nil || runner.applications == nil {
		return Snapshot{}, ErrInvalidRequest
	}
	input, inputHash, err := marshalInvocationValue(applicationInvocationInput{
		Goal: strings.TrimSpace(request.Goal), Actor: request.Actor, Thread: request.Thread,
		Input: normalizedApplicationInput(request.Input),
	})
	if err != nil {
		return Snapshot{}, err
	}
	prepared, err := runner.prepareTopLevelFeatureStart(
		ctx, request.StartRequest, strings.TrimSpace(request.CapabilityKey), strings.TrimSpace(request.DefinitionVersion),
		ExecutionApplication, input, inputHash,
	)
	if err != nil {
		return Snapshot{}, err
	}
	if prepared.replayed && (prepared.invocation.Status == InvocationWaitingInput || terminalInvocationStatus(prepared.invocation.Status)) {
		return runner.loadSnapshot(ctx, prepared.turn, nil)
	}
	return runner.executeApplicationInvocation(ctx, prepared.turn, prepared.invocation)
}

func (runner *Runner) StartApplicationInvocation(
	ctx context.Context,
	turnID string,
	request ApplicationInvocationRequest,
) (Snapshot, error) {
	if runner == nil || runner.applications == nil {
		return Snapshot{}, ErrInvalidRequest
	}
	input, inputHash, err := marshalInvocationValue(applicationInvocationInput{
		Goal: strings.TrimSpace(request.Goal), Input: normalizedApplicationInput(request.Input),
	})
	if err != nil {
		return Snapshot{}, err
	}
	invocation, child, replayed, err := runner.beginChildInvocation(
		ctx, turnID, request.ParentItemID, request.RequestID,
		strings.TrimSpace(request.CapabilityKey), strings.TrimSpace(request.DefinitionVersion),
		ExecutionApplication, input, inputHash,
	)
	if err != nil {
		return Snapshot{}, err
	}
	if replayed && (invocation.Status == InvocationWaitingInput || terminalInvocationStatus(invocation.Status)) {
		return runner.loadSnapshot(ctx, child.turn, nil)
	}
	return runner.executeApplicationInvocation(ctx, child.turn, invocation)
}

func normalizedApplicationInput(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return append(json.RawMessage(nil), raw...)
}

func (runner *Runner) executeApplicationInvocation(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
) (Snapshot, error) {
	if runner.applications == nil || invocation.ExecutionClass != ExecutionApplication || !json.Valid(invocation.Input) {
		return Snapshot{}, ErrInvalidRequest
	}
	if invocation.Status == InvocationWaitingInput || terminalInvocationStatus(invocation.Status) {
		return runner.loadSnapshot(ctx, turn, nil)
	}
	turn, invocation, request, err := runner.prepareApplicationExecution(ctx, turn, invocation)
	if err != nil {
		return Snapshot{}, err
	}
	result, executeErr := runner.applications.ExecuteApplicationCapability(ctx, request)
	if executeErr != nil {
		return runner.failApplicationInvocation(ctx, turn, invocation, executeErr)
	}
	if result.Interaction != nil {
		return runner.requestApplicationInteraction(ctx, turn, invocation, *result.Interaction)
	}
	return runner.completeApplicationInvocation(ctx, turn, invocation, result.OutputRefs)
}

func (runner *Runner) prepareApplicationExecution(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
) (Turn, Invocation, ApplicationCapabilityRequest, error) {
	var input applicationInvocationInput
	if err := json.Unmarshal(invocation.Input, &input); err != nil {
		return Turn{}, Invocation{}, ApplicationCapabilityRequest{}, ErrConflict
	}
	if err := runner.reconcileInvocationStatus(ctx, invocation.ID, InvocationRunning); err != nil {
		return Turn{}, Invocation{}, ApplicationCapabilityRequest{}, err
	}
	updatedTurn, changed, err := runner.reconcileTurnStatus(ctx, turn.ID, TurnRunning)
	if err != nil {
		return Turn{}, Invocation{}, ApplicationCapabilityRequest{}, err
	}
	if changed {
		runner.publishTurnStatus(ctx, updatedTurn, EventTurnStarted, false)
	}
	invocation, err = runner.store.GetInvocation(ctx, invocation.ID)
	if err != nil {
		return Turn{}, Invocation{}, ApplicationCapabilityRequest{}, err
	}
	session, err := runner.store.GetSession(ctx, updatedTurn.SessionID)
	if err != nil {
		return Turn{}, Invocation{}, ApplicationCapabilityRequest{}, err
	}
	return updatedTurn, invocation, ApplicationCapabilityRequest{
		Invocation: cloneInvocation(invocation), Session: session,
		Goal: strings.TrimSpace(input.Goal), Input: normalizedApplicationInput(input.Input),
	}, nil
}

func (runner *Runner) requestApplicationInteraction(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	request ApplicationInteractionRequest,
) (Snapshot, error) {
	parentItemID := invocationLifecycleItemID(invocation, invocation.Status, invocation.Revision)
	return runner.RequestInteraction(ctx, turn.ID, RequestInteraction{
		InvocationID: invocation.ID, ParentItemID: parentItemID,
		ApplicationRef: request.ApplicationRef, ArtifactRefs: append([]HostRef(nil), request.ArtifactRefs...),
		Key: strings.TrimSpace(request.Key), Kind: request.Kind,
		Schema: append(json.RawMessage(nil), request.Schema...), Presentation: append(json.RawMessage(nil), request.Presentation...),
	})
}

func (runner *Runner) completeApplicationInvocation(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	outputRefs []HostRef,
) (Snapshot, error) {
	if !validInvocationOutputRefs(outputRefs) {
		return Snapshot{}, ErrInvalidRequest
	}
	current, err := runner.store.GetInvocation(ctx, invocation.ID)
	if err != nil {
		return Snapshot{}, err
	}
	if terminalInvocationStatus(current.Status) {
		return runner.loadSnapshot(ctx, turn, nil)
	}
	current.Status = InvocationCompleted
	current.OutputRefs = append([]HostRef(nil), outputRefs...)
	current.ErrorCode, current.ErrorDetail = "", ""
	current.UpdatedAt = runner.clock.Now().UTC()
	updated, err := runner.store.UpdateInvocation(ctx, current, current.Revision)
	if err != nil {
		return Snapshot{}, err
	}
	if err = runner.recordInvocationItem(ctx, updated); err != nil {
		return Snapshot{}, err
	}
	if strings.TrimSpace(updated.ParentItemID) == "" {
		completedTurn, changed, turnErr := runner.reconcileTurnStatus(ctx, turn.ID, TurnCompleted)
		if turnErr != nil {
			return Snapshot{}, turnErr
		}
		if changed {
			runner.publishTurnStatus(ctx, completedTurn, EventTurnCompleted, true)
		}
		return runner.loadSnapshot(ctx, completedTurn, nil)
	}
	return runner.loadSnapshot(ctx, turn, nil)
}

func (runner *Runner) failApplicationInvocation(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	cause error,
) (Snapshot, error) {
	if strings.TrimSpace(invocation.ParentItemID) == "" {
		failed, failErr := runner.failTopLevelInvocationAndTurn(ctx, turn, invocation, cause)
		return failed, errors.Join(cause, failErr)
	}
	return runner.failChildInvocation(ctx, turn, invocation, cause)
}

func (runner *Runner) finishResolvedApplicationInteraction(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	result InteractionResponseResult,
) (Snapshot, error) {
	snapshot, err := runner.completeApplicationInvocation(ctx, turn, invocation, result.OutputRefs)
	if err != nil || strings.TrimSpace(invocation.ParentItemID) == "" {
		return snapshot, err
	}
	updatedTurn, err := runner.reopenApplicationParentTurn(ctx, turn.ID)
	if err != nil {
		return Snapshot{}, err
	}
	return runner.resumeApplicationParentAgent(ctx, updatedTurn)
}

func (runner *Runner) reopenApplicationParentTurn(ctx context.Context, turnID string) (Turn, error) {
	turn, err := runner.store.GetTurn(ctx, turnID)
	if err != nil {
		return Turn{}, err
	}
	updated, changed, err := runner.reconcileTurnStatus(ctx, turn.ID, TurnRunning)
	if err != nil {
		return Turn{}, err
	}
	if changed {
		runner.publishTurnStatus(ctx, updated, EventTurnStarted, false)
	}
	return updated, nil
}

func (runner *Runner) resumeApplicationParentAgent(ctx context.Context, updatedTurn Turn) (Snapshot, error) {
	root, err := loadTopLevelInvocation(ctx, runner.store, updatedTurn.ID)
	if err != nil || root.ExecutionClass != ExecutionAgent {
		return Snapshot{}, errors.Join(ErrConflict, err)
	}
	runtimeSnapshot, err := runner.runtime.Load(ctx, root.ExecutionRefID)
	if err != nil {
		return Snapshot{}, err
	}
	if terminalRuntimeStatus(runtimeSnapshot.Run.Status) {
		return runner.syncRuntimeSnapshotWithRetry(ctx, updatedTurn, root, runtimeSnapshot)
	}
	resumed, resumeErr := runner.resumeInvocationExecution(ctx, root, runtimeSnapshot.Run.Revision)
	if resumed.Run.ID == "" {
		return Snapshot{}, resumeErr
	}
	resumedSnapshot, syncErr := runner.syncRuntimeSnapshotWithRetry(ctx, updatedTurn, root, resumed)
	return resumedSnapshot, errors.Join(resumeErr, syncErr)
}

func (runner *Runner) refreshApplicationInvocation(ctx context.Context, invocation Invocation, turn Turn) (Snapshot, error) {
	if invocation.Status == InvocationAccepted || invocation.Status == InvocationRunning {
		return runner.executeApplicationInvocation(ctx, turn, invocation)
	}
	return runner.loadSnapshot(ctx, turn, nil)
}

func (runner *Runner) cancelApplicationInvocation(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	reason string,
) (Snapshot, error) {
	if terminalInvocationStatus(invocation.Status) {
		return runner.loadSnapshot(ctx, turn, nil)
	}
	invocation.Status = InvocationCancelled
	invocation.ErrorCode = "run.cancelled"
	invocation.ErrorDetail = strings.TrimSpace(reason)
	invocation.UpdatedAt = runner.clock.Now().UTC()
	updated, err := runner.store.UpdateInvocation(ctx, invocation, invocation.Revision)
	if err != nil {
		return Snapshot{}, err
	}
	if err = runner.recordInvocationItem(ctx, updated); err != nil {
		return Snapshot{}, err
	}
	if strings.TrimSpace(updated.ParentItemID) == "" {
		cancelledTurn, changed, turnErr := runner.reconcileTurnStatus(ctx, turn.ID, TurnCancelled)
		if turnErr != nil {
			return Snapshot{}, turnErr
		}
		if changed {
			runner.publishTurnStatus(ctx, cancelledTurn, EventTurnCancelled, true)
		}
		return runner.loadSnapshot(ctx, cancelledTurn, nil)
	}
	return runner.loadSnapshot(ctx, turn, nil)
}
