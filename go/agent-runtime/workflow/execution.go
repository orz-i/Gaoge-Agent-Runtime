package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runrelation"
)

const (
	RunKind          kernel.RunKind    = "workflow"
	CapabilityRunner kernel.Capability = "workflow.runner"
)

const workflowWaitCheckpointKind = "workflow_wait"

var (
	ErrInvalidExecution = errors.New("invalid workflow execution")
	ErrEffectPending    = errors.New("workflow effect is pending")
	ErrEffectFailed     = errors.New("workflow effect failed")
	ErrWaitPending      = errors.New("workflow wait is pending")
	ErrSegmentYielded   = errors.New("workflow segment yielded")
	ErrBudgetExceeded   = errors.New("workflow budget exceeded")
	ErrStateTooLarge    = errors.New("workflow state exceeds limit")
	ErrWorkflowTerminal = errors.New("workflow run is terminal")
)

// ActivationStatus is the lifecycle of one deterministic node activation.
type ActivationStatus string

const (
	ActivationRunning   ActivationStatus = "running"
	ActivationWaiting   ActivationStatus = "waiting"
	ActivationCompleted ActivationStatus = "completed"
	ActivationFailed    ActivationStatus = "failed"
)

// EffectStatus is the durable lifecycle of one external Effect.
type EffectStatus string

const (
	EffectPending   EffectStatus = "pending"
	EffectCompleted EffectStatus = "completed"
	EffectFailed    EffectStatus = "failed"
)

// EffectDisposition is one executor observation of a persisted Effect intent.
type EffectDisposition string

const (
	DispositionPending   EffectDisposition = "pending"
	DispositionCompleted EffectDisposition = "completed"
	DispositionFailed    EffectDisposition = "failed"
)

// WaitStatus is the lifecycle of one explicit host Wait.
type WaitStatus string

const (
	WaitPending  WaitStatus = "pending"
	WaitResolved WaitStatus = "resolved"
)

// Activation records one node attempt in definition order.
type Activation struct {
	ID        string           `json:"id"`
	NodeID    string           `json:"nodeID"`
	NodeIndex int              `json:"nodeIndex"`
	Status    ActivationStatus `json:"status"`
	Attempt   int              `json:"attempt"`
	EffectID  string           `json:"effectID,omitempty"`
	WaitID    string           `json:"waitID,omitempty"`
	Output    json.RawMessage  `json:"output,omitempty"`
	ErrorCode string           `json:"errorCode,omitempty"`
	Error     string           `json:"error,omitempty"`
}

// Effect is one stable external dispatch intent and terminal receipt.
type Effect struct {
	ID           string          `json:"id"`
	ActivationID string          `json:"activationID"`
	NodeID       string          `json:"nodeID"`
	Kind         string          `json:"kind"`
	Input        json.RawMessage `json:"input"`
	Status       EffectStatus    `json:"status"`
	ChildRunID   string          `json:"childRunID,omitempty"`
	ReceiptID    string          `json:"receiptID,omitempty"`
	Output       json.RawMessage `json:"output,omitempty"`
	ErrorCode    string          `json:"errorCode,omitempty"`
	Error        string          `json:"error,omitempty"`
}

// Wait is one explicit host-resolved suspension point.
type Wait struct {
	ID           string          `json:"id"`
	ActivationID string          `json:"activationID"`
	NodeID       string          `json:"nodeID"`
	Kind         string          `json:"kind"`
	Payload      json.RawMessage `json:"payload"`
	Status       WaitStatus      `json:"status"`
	Response     json.RawMessage `json:"response,omitempty"`
}

// Budget is the durable execution ledger.
type Budget struct {
	NodeActivations int `json:"nodeActivations"`
	Effects         int `json:"effects"`
	Segments        int `json:"segments"`
	StateBytes      int `json:"stateBytes"`
}

// View is the public durable Workflow execution state.
type View struct {
	Definition    Definition      `json:"definition"`
	Input         json.RawMessage `json:"input"`
	CurrentNode   int             `json:"currentNode"`
	Activations   []Activation    `json:"activations"`
	Effects       []Effect        `json:"effects"`
	Waits         []Wait          `json:"waits"`
	CurrentWaitID string          `json:"currentWaitID,omitempty"`
	Budget        Budget          `json:"budget"`
}

// StartRequest starts one explicit Workflow Run from an exact compiled Definition.
type StartRequest struct {
	ID         string
	Actor      kernel.ActorRef
	Thread     kernel.ThreadRef
	RequestID  string
	Goal       string
	Definition Definition
	Input      json.RawMessage
}

// EffectRequest dispatches one already-persisted stable intent.
type EffectRequest struct {
	RunID          string
	DefinitionID   string
	DefinitionHash string
	EffectID       string
	NodeID         string
	Kind           string
	Input          json.RawMessage
}

// EffectResult is one executor observation. Completed results require a receipt.
type EffectResult struct {
	Disposition EffectDisposition
	ChildRunID  string
	ReceiptID   string
	Output      json.RawMessage
	ErrorCode   string
	ErrorDetail string
}

// EffectExecutor consumes only stable intents that already exist in Kernel state.
type EffectExecutor interface {
	Execute(context.Context, EffectRequest) (EffectResult, error)
}

// Dependencies are the only requirements of the Workflow feature.
type Dependencies struct {
	Runtime         *kernel.Runtime
	Effects         EffectExecutor
	Relations       runrelation.Recorder
	Ceiling         Limits
	DeferResumption bool
}

// Runner owns deterministic Workflow interpretation and no background workers.
type Runner struct {
	runtime     *kernel.Runtime
	effects     EffectExecutor
	relations   runrelation.Recorder
	ceiling     Limits
	deferResume bool
}

type executionState View

type segmentBudget struct {
	activations int
	effects     int
}

// WaitRequest is the host-facing payload stored in a Kernel Checkpoint.
type WaitRequest struct {
	WaitID  string          `json:"waitID"`
	NodeID  string          `json:"nodeID"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// NewRunner creates an independent deterministic Workflow feature.
func NewRunner(dependencies Dependencies) (*Runner, error) {
	if dependencies.Runtime == nil || dependencies.Effects == nil {
		return nil, ErrInvalidExecution
	}
	ceiling := normalizeCeiling(dependencies.Ceiling)
	return &Runner{
		runtime: dependencies.Runtime, effects: dependencies.Effects,
		relations: dependencies.Relations, ceiling: ceiling,
		deferResume: dependencies.DeferResumption,
	}, nil
}

// Descriptor declares the explicit Workflow capability graph.
func (runner *Runner) Descriptor() kernel.FeatureDescriptor {
	requires := []kernel.Capability{kernel.CapabilityRuntime}
	if runner != nil && runner.relations != nil {
		requires = append(requires, runrelation.CapabilityRelations)
	}
	return kernel.FeatureDescriptor{
		Name: "workflow", Requires: requires,
		Provides: []kernel.Capability{CapabilityRunner},
	}
}

// StartRun creates and advances one Workflow segment.
func (runner *Runner) StartRun(ctx context.Context, request StartRequest) (kernel.Snapshot, error) {
	request = normalizeStartRequest(request)
	if !validStartRequest(request) || !withinCeiling(request.Definition.Limits, runner.ceiling) {
		return kernel.Snapshot{}, ErrInvalidExecution
	}
	state := executionState{
		Definition: cloneDefinition(request.Definition), Input: cloneJSON(request.Input),
		Activations: make([]Activation, 0), Effects: make([]Effect, 0), Waits: make([]Wait, 0),
	}
	encoded, err := encodeExecutionState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	snapshot, err := runner.runtime.Create(ctx, kernel.CreateRequest{
		ID: request.ID, Kind: RunKind, Actor: request.Actor, Thread: request.Thread,
		RequestID: request.RequestID, Goal: request.Goal, State: encoded,
		Events: []kernel.EventDraft{{Type: "workflow.started", Message: request.Definition.Hash}},
	})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.runSegment(ctx, snapshot)
}

// Resume continues one running Workflow from its durable state.
func (runner *Runner) Resume(ctx context.Context, runID string, expectedRevision uint64) (kernel.Snapshot, error) {
	snapshot, err := runner.runtime.Load(ctx, runID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if snapshot.Run.Revision != expectedRevision {
		return kernel.Snapshot{}, kernel.ErrConflict
	}
	if snapshot.Run.Status == kernel.RunStatusWaitingInput {
		return snapshot, ErrWaitPending
	}
	if snapshot.Run.Status != kernel.RunStatusRunning {
		return snapshot, ErrWorkflowTerminal
	}
	return runner.runSegment(ctx, snapshot)
}

// ResolveWait resolves one pending host Wait and continues the next segment.
func (runner *Runner) ResolveWait(
	ctx context.Context,
	runID string,
	expectedRevision uint64,
	response json.RawMessage,
) (kernel.Snapshot, error) {
	if !json.Valid(response) {
		return kernel.Snapshot{}, ErrInvalidExecution
	}
	snapshot, state, err := runner.load(ctx, runID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if snapshot.Run.Revision != expectedRevision {
		return kernel.Snapshot{}, kernel.ErrConflict
	}
	if !validPendingWait(snapshot, state) {
		return kernel.Snapshot{}, ErrWaitPending
	}
	waitID := state.CurrentWaitID
	if err = resolveWaitState(&state, response); err != nil {
		return kernel.Snapshot{}, err
	}
	checkpoint := resolveCheckpoint(snapshot.Checkpoint, response, runner.runtime.Now())
	encoded, err := encodeExecutionState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	running, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: checkpoint,
		Events: []kernel.EventDraft{{Type: "workflow.wait.resolved", Message: waitID}},
	})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if runner.deferResume {
		return running, nil
	}
	return runner.runSegment(ctx, running)
}

// ViewState decodes an isolated public view from a Kernel snapshot.
func ViewState(snapshot kernel.Snapshot) (View, error) {
	state, err := decodeExecutionState(snapshot.State)
	if err != nil {
		return View{}, err
	}
	return cloneExecutionView(View(state)), nil
}

// WaitRequestFromCheckpoint decodes one Workflow Wait checkpoint.
func WaitRequestFromCheckpoint(checkpoint *kernel.Checkpoint) (WaitRequest, error) {
	if checkpoint == nil || checkpoint.Kind != workflowWaitCheckpointKind ||
		checkpoint.Status != kernel.CheckpointPending || !json.Valid(checkpoint.Payload) {
		return WaitRequest{}, ErrInvalidExecution
	}
	var request WaitRequest
	if err := json.Unmarshal(checkpoint.Payload, &request); err != nil {
		return WaitRequest{}, errors.Join(ErrInvalidExecution, err)
	}
	return request, nil
}

func (runner *Runner) runSegment(ctx context.Context, snapshot kernel.Snapshot) (kernel.Snapshot, error) {
	state, err := decodeExecutionState(snapshot.State)
	if err != nil || ValidateDefinition(state.Definition) != nil {
		return kernel.Snapshot{}, errors.Join(ErrInvalidExecution, err)
	}
	if state.Budget.Segments >= state.Definition.Limits.MaxSegments {
		return runner.fail(ctx, snapshot, state, "workflow.segment_budget", ErrBudgetExceeded)
	}
	state.Budget.Segments++
	segment := segmentBudget{}
	for snapshot.Run.Status == kernel.RunStatusRunning {
		next, done, stepErr := runner.advance(ctx, snapshot, state, &segment)
		if stepErr != nil || done {
			return next, stepErr
		}
		snapshot = next
		state, err = decodeExecutionState(snapshot.State)
		if err != nil {
			return kernel.Snapshot{}, err
		}
	}
	return snapshot, nil
}

func (runner *Runner) advance(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	segment *segmentBudget,
) (kernel.Snapshot, bool, error) {
	if state.CurrentNode >= len(state.Definition.Nodes) {
		failed, err := runner.fail(ctx, snapshot, state, "workflow.node_out_of_range", ErrInvalidExecution)
		return failed, true, err
	}
	node := state.Definition.Nodes[state.CurrentNode]
	if activationIndex(state, state.CurrentNode) < 0 {
		return runner.activateNode(ctx, snapshot, state, node, segment)
	}
	return runner.advanceEffect(ctx, snapshot, state, node, segment)
}

func (runner *Runner) activateNode(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	node Node,
	segment *segmentBudget,
) (kernel.Snapshot, bool, error) {
	if segment.activations >= state.Definition.Limits.MaxActivationsPerSegment {
		yielded, err := runner.yield(ctx, snapshot, state, "activation_budget")
		return yielded, true, err
	}
	if state.Budget.NodeActivations >= state.Definition.Limits.MaxNodeActivations {
		failed, err := runner.fail(ctx, snapshot, state, "workflow.activation_budget", ErrBudgetExceeded)
		return failed, true, err
	}
	segment.activations++
	switch node.Type {
	case NodeEffect:
		prepared, err := runner.prepareEffect(ctx, snapshot, state, node)
		if err != nil {
			return prepared, true, err
		}
		preparedState, err := decodeExecutionState(prepared.State)
		if err != nil {
			return kernel.Snapshot{}, true, err
		}
		return runner.advanceEffect(ctx, prepared, preparedState, node, segment)
	case NodeWait:
		waiting, err := runner.prepareWait(ctx, snapshot, state, node)
		return waiting, true, err
	case NodeReturn:
		completed, err := runner.complete(ctx, snapshot, state, node)
		return completed, true, err
	default:
		failed, err := runner.fail(ctx, snapshot, state, "workflow.node_invalid", ErrInvalidExecution)
		return failed, true, err
	}
}

func (runner *Runner) advanceEffect(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	node Node,
	segment *segmentBudget,
) (kernel.Snapshot, bool, error) {
	if node.Type != NodeEffect {
		return snapshot, true, ErrInvalidExecution
	}
	if segment.effects >= 1 {
		yielded, err := runner.yield(ctx, snapshot, state, "effect_budget")
		return yielded, true, err
	}
	segment.effects++
	return runner.dispatchEffect(ctx, snapshot, state)
}

func (runner *Runner) prepareEffect(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	node Node,
) (kernel.Snapshot, error) {
	if state.Budget.Effects >= state.Definition.Limits.MaxEffects {
		return runner.fail(ctx, snapshot, state, "workflow.effect_budget", ErrBudgetExceeded)
	}
	activationID, err := runner.runtime.NewID("activation")
	if err != nil {
		return kernel.Snapshot{}, err
	}
	effectID, err := runner.runtime.NewID("effect")
	if err != nil {
		return kernel.Snapshot{}, err
	}
	state.Activations = append(state.Activations, Activation{
		ID: activationID, NodeID: node.ID, NodeIndex: state.CurrentNode,
		Status: ActivationRunning, Attempt: 1, EffectID: effectID,
	})
	effectInput := node.Effect.Input
	if node.Effect.FromInput {
		effectInput = state.Input
	}
	state.Effects = append(state.Effects, Effect{
		ID: effectID, ActivationID: activationID, NodeID: node.ID,
		Kind: node.Effect.Kind, Input: cloneJSON(effectInput), Status: EffectPending,
	})
	state.Budget.NodeActivations++
	state.Budget.Effects++
	encoded, err := encodeExecutionState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{Type: "workflow.effect.intent_created", Message: effectID}},
	})
}

func (runner *Runner) dispatchEffect(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
) (kernel.Snapshot, bool, error) {
	activationIndex := activationIndex(state, state.CurrentNode)
	if activationIndex < 0 {
		return snapshot, true, ErrInvalidExecution
	}
	effectIndex := effectIndexByID(state, state.Activations[activationIndex].EffectID)
	if effectIndex < 0 || state.Effects[effectIndex].Status != EffectPending {
		return snapshot, true, ErrInvalidExecution
	}
	effect := state.Effects[effectIndex]
	result, err := runner.effects.Execute(ctx, EffectRequest{
		RunID: snapshot.Run.ID, DefinitionID: state.Definition.ID, DefinitionHash: state.Definition.Hash,
		EffectID: effect.ID, NodeID: effect.NodeID, Kind: effect.Kind, Input: cloneJSON(effect.Input),
	})
	if err != nil {
		failed, failErr := runner.failEffect(ctx, snapshot, state, activationIndex, effectIndex, "workflow.effect_dispatch", err)
		return failed, true, failErr
	}
	state.Effects[effectIndex].ChildRunID = strings.TrimSpace(result.ChildRunID)
	if err = runner.ensureEffectRelation(ctx, snapshot.Run.ID, state.Effects[effectIndex]); err != nil {
		failed, failErr := runner.failEffect(
			ctx, snapshot, state, activationIndex, effectIndex, "workflow.effect_relation", err,
		)
		return failed, true, failErr
	}
	switch result.Disposition {
	case DispositionPending:
		yielded, yieldErr := runner.yield(ctx, snapshot, state, "effect_pending")
		return yielded, true, errors.Join(ErrEffectPending, yieldErr)
	case DispositionCompleted:
		completed, completeErr := runner.completeEffect(ctx, snapshot, state, activationIndex, effectIndex, result)
		return completed, false, completeErr
	case DispositionFailed:
		failed, failErr := runner.failEffectResult(ctx, snapshot, state, activationIndex, effectIndex, result)
		return failed, true, failErr
	default:
		failed, failErr := runner.failEffect(ctx, snapshot, state, activationIndex, effectIndex, "workflow.effect_result", ErrInvalidExecution)
		return failed, true, failErr
	}
}

func (runner *Runner) ensureEffectRelation(ctx context.Context, parentRunID string, effect Effect) error {
	if runner.relations == nil || effect.ChildRunID == "" {
		return nil
	}
	_, err := runner.relations.Ensure(ctx, runrelation.Draft{
		ParentRunID: parentRunID, ChildRunID: effect.ChildRunID,
		Kind: runrelation.KindWorkflowEffect, OwnerNodeID: effect.NodeID,
	})
	return err
}

func (runner *Runner) completeEffect(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	activationIndex int,
	effectIndex int,
	result EffectResult,
) (kernel.Snapshot, error) {
	if strings.TrimSpace(result.ReceiptID) == "" || !json.Valid(result.Output) {
		return runner.failEffect(ctx, snapshot, state, activationIndex, effectIndex, "workflow.effect_receipt", ErrInvalidExecution)
	}
	state.Effects[effectIndex].Status = EffectCompleted
	state.Effects[effectIndex].ReceiptID = strings.TrimSpace(result.ReceiptID)
	state.Effects[effectIndex].Output = cloneJSON(result.Output)
	state.Activations[activationIndex].Status = ActivationCompleted
	state.Activations[activationIndex].Output = cloneJSON(result.Output)
	state.CurrentNode++
	encoded, err := encodeExecutionState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{Type: "workflow.effect.completed", Message: state.Effects[effectIndex].ID}},
	})
}

func (runner *Runner) failEffectResult(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	activationIndex int,
	effectIndex int,
	result EffectResult,
) (kernel.Snapshot, error) {
	code := strings.TrimSpace(result.ErrorCode)
	if code == "" {
		code = "workflow.effect_failed"
	}
	detail := strings.TrimSpace(result.ErrorDetail)
	if detail == "" {
		detail = ErrEffectFailed.Error()
	}
	state.Effects[effectIndex].Status = EffectFailed
	state.Effects[effectIndex].ErrorCode = code
	state.Effects[effectIndex].Error = detail
	state.Activations[activationIndex].Status = ActivationFailed
	state.Activations[activationIndex].ErrorCode = code
	state.Activations[activationIndex].Error = detail
	return runner.fail(ctx, snapshot, state, code, ErrEffectFailed)
}

func (runner *Runner) failEffect(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	activationIndex int,
	effectIndex int,
	code string,
	cause error,
) (kernel.Snapshot, error) {
	state.Effects[effectIndex].Status = EffectFailed
	state.Effects[effectIndex].ErrorCode = code
	state.Effects[effectIndex].Error = errorText(cause)
	state.Activations[activationIndex].Status = ActivationFailed
	state.Activations[activationIndex].ErrorCode = code
	state.Activations[activationIndex].Error = errorText(cause)
	return runner.fail(ctx, snapshot, state, code, errors.Join(ErrEffectFailed, cause))
}

func (runner *Runner) prepareWait(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	node Node,
) (kernel.Snapshot, error) {
	activationID, err := runner.runtime.NewID("activation")
	if err != nil {
		return kernel.Snapshot{}, err
	}
	waitID, err := runner.runtime.NewID("wait")
	if err != nil {
		return kernel.Snapshot{}, err
	}
	state.Activations = append(state.Activations, Activation{
		ID: activationID, NodeID: node.ID, NodeIndex: state.CurrentNode,
		Status: ActivationWaiting, Attempt: 1, WaitID: waitID,
	})
	state.Waits = append(state.Waits, Wait{
		ID: waitID, ActivationID: activationID, NodeID: node.ID, Kind: node.Wait.Kind,
		Payload: cloneJSON(node.Wait.Payload), Status: WaitPending,
	})
	state.CurrentWaitID = waitID
	state.Budget.NodeActivations++
	payload, err := json.Marshal(WaitRequest{
		WaitID: waitID, NodeID: node.ID, Kind: node.Wait.Kind, Payload: cloneJSON(node.Wait.Payload),
	})
	if err != nil {
		return kernel.Snapshot{}, errors.Join(ErrInvalidExecution, err)
	}
	checkpoint := &kernel.Checkpoint{
		ID: waitID, Kind: workflowWaitCheckpointKind, Status: kernel.CheckpointPending,
		Payload: payload, CreatedAt: runner.runtime.Now(),
	}
	encoded, err := encodeExecutionState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusWaitingInput, State: encoded, Checkpoint: checkpoint,
		Events: []kernel.EventDraft{{Type: "workflow.wait.created", Message: waitID}},
	})
}

func (runner *Runner) complete(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	node Node,
) (kernel.Snapshot, error) {
	result, err := workflowReturnValue(state, *node.Return)
	if err != nil {
		return runner.fail(ctx, snapshot, state, "workflow.return_invalid", err)
	}
	activationID, err := runner.runtime.NewID("activation")
	if err != nil {
		return kernel.Snapshot{}, err
	}
	state.Activations = append(state.Activations, Activation{
		ID: activationID, NodeID: node.ID, NodeIndex: state.CurrentNode,
		Status: ActivationCompleted, Attempt: 1, Output: cloneJSON(result),
	})
	state.Budget.NodeActivations++
	state.CurrentNode++
	encoded, err := encodeExecutionState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: encoded, Checkpoint: snapshot.Checkpoint,
		Result: &kernel.Result{ContentType: "application/json", Content: cloneJSON(result)},
		Events: []kernel.EventDraft{{Type: "workflow.completed", Message: state.Definition.Hash}},
	})
}

func workflowReturnValue(state executionState, node ReturnNode) (json.RawMessage, error) {
	if strings.TrimSpace(node.FromNode) == "" {
		if !json.Valid(node.Value) {
			return nil, ErrInvalidExecution
		}
		return cloneJSON(node.Value), nil
	}
	for index := len(state.Activations) - 1; index >= 0; index-- {
		activation := state.Activations[index]
		if activation.NodeID == node.FromNode && activation.Status == ActivationCompleted && json.Valid(activation.Output) {
			return cloneJSON(activation.Output), nil
		}
	}
	return nil, ErrInvalidExecution
}

func (runner *Runner) yield(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	reason string,
) (kernel.Snapshot, error) {
	encoded, err := encodeExecutionState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	yielded, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{Type: "workflow.segment.yielded", Message: reason}},
	})
	return yielded, errors.Join(ErrSegmentYielded, err)
}

func (runner *Runner) fail(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	code string,
	cause error,
) (kernel.Snapshot, error) {
	encoded, err := encodeExecutionState(state)
	if err != nil {
		return kernel.Snapshot{}, errors.Join(cause, err)
	}
	failed, transitionErr := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusFailed, State: encoded, Checkpoint: snapshot.Checkpoint,
		ErrorCode: strings.TrimSpace(code), ErrorDetail: errorText(cause),
		Events: []kernel.EventDraft{{Type: "workflow.failed", Message: strings.TrimSpace(code)}},
	})
	return failed, errors.Join(cause, transitionErr)
}

func (runner *Runner) load(ctx context.Context, runID string) (kernel.Snapshot, executionState, error) {
	snapshot, err := runner.runtime.Load(ctx, runID)
	if err != nil {
		return kernel.Snapshot{}, executionState{}, err
	}
	state, err := decodeExecutionState(snapshot.State)
	return snapshot, state, err
}

func validPendingWait(snapshot kernel.Snapshot, state executionState) bool {
	if snapshot.Run.Status != kernel.RunStatusWaitingInput || snapshot.Checkpoint == nil ||
		snapshot.Checkpoint.Kind != workflowWaitCheckpointKind || snapshot.Checkpoint.Status != kernel.CheckpointPending ||
		state.CurrentWaitID == "" {
		return false
	}
	waitIndex := waitIndexByID(state, state.CurrentWaitID)
	if waitIndex < 0 || state.Waits[waitIndex].Status != WaitPending {
		return false
	}
	return activationIndexByID(state, state.Waits[waitIndex].ActivationID) >= 0
}

func resolveWaitState(state *executionState, response json.RawMessage) error {
	waitIndex := waitIndexByID(*state, state.CurrentWaitID)
	if waitIndex < 0 {
		return ErrInvalidExecution
	}
	wait := &state.Waits[waitIndex]
	activationIndex := activationIndexByID(*state, wait.ActivationID)
	if activationIndex < 0 {
		return ErrInvalidExecution
	}
	wait.Status = WaitResolved
	wait.Response = cloneJSON(response)
	state.Activations[activationIndex].Status = ActivationCompleted
	state.Activations[activationIndex].Output = cloneJSON(response)
	state.CurrentNode++
	state.CurrentWaitID = ""
	return nil
}

func resolveCheckpoint(checkpoint *kernel.Checkpoint, response json.RawMessage, resolvedAt time.Time) *kernel.Checkpoint {
	resolved := *checkpoint
	resolved.Status = kernel.CheckpointResolved
	resolved.Payload = cloneJSON(checkpoint.Payload)
	resolved.Response = cloneJSON(response)
	value := resolvedAt.UTC()
	resolved.ResolvedAt = &value
	return &resolved
}

func normalizeStartRequest(request StartRequest) StartRequest {
	request.ID = strings.TrimSpace(request.ID)
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.Goal = strings.TrimSpace(request.Goal)
	request.Definition = cloneDefinition(request.Definition)
	request.Input = cloneJSON(request.Input)
	if len(request.Input) == 0 {
		request.Input = json.RawMessage(`null`)
	}
	return request
}

func validStartRequest(request StartRequest) bool {
	return request.Goal != "" && json.Valid(request.Input) && ValidateDefinition(request.Definition) == nil
}

func normalizeCeiling(ceiling Limits) Limits {
	if ceiling.MaxNodeActivations <= 0 {
		ceiling.MaxNodeActivations = 4096
	}
	if ceiling.MaxEffects <= 0 {
		ceiling.MaxEffects = 1024
	}
	if ceiling.MaxSegments <= 0 {
		ceiling.MaxSegments = 4096
	}
	if ceiling.MaxActivationsPerSegment <= 0 {
		ceiling.MaxActivationsPerSegment = 256
	}
	if ceiling.MaxStateBytes <= 0 {
		ceiling.MaxStateBytes = 8 << 20
	}
	return ceiling
}

func withinCeiling(limits Limits, ceiling Limits) bool {
	return limits.MaxNodeActivations <= ceiling.MaxNodeActivations &&
		limits.MaxEffects <= ceiling.MaxEffects && limits.MaxSegments <= ceiling.MaxSegments &&
		limits.MaxActivationsPerSegment <= ceiling.MaxActivationsPerSegment &&
		limits.MaxStateBytes <= ceiling.MaxStateBytes
}

func encodeExecutionState(state executionState) (json.RawMessage, error) {
	state.Budget.StateBytes = 0
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, errors.Join(ErrInvalidExecution, err)
	}
	for range 3 {
		state.Budget.StateBytes = len(encoded)
		encoded, err = json.Marshal(state)
		if err != nil {
			return nil, errors.Join(ErrInvalidExecution, err)
		}
	}
	if len(encoded) > state.Definition.Limits.MaxStateBytes {
		return nil, ErrStateTooLarge
	}
	return encoded, nil
}

func decodeExecutionState(encoded json.RawMessage) (executionState, error) {
	var state executionState
	if err := json.Unmarshal(encoded, &state); err != nil {
		return executionState{}, errors.Join(ErrInvalidExecution, err)
	}
	return state, nil
}

func activationIndex(state executionState, nodeIndex int) int {
	for index := range state.Activations {
		if state.Activations[index].NodeIndex == nodeIndex {
			return index
		}
	}
	return -1
}

func activationIndexByID(state executionState, activationID string) int {
	for index := range state.Activations {
		if state.Activations[index].ID == activationID {
			return index
		}
	}
	return -1
}

func effectIndexByID(state executionState, effectID string) int {
	for index := range state.Effects {
		if state.Effects[index].ID == effectID {
			return index
		}
	}
	return -1
}

func waitIndexByID(state executionState, waitID string) int {
	for index := range state.Waits {
		if state.Waits[index].ID == waitID {
			return index
		}
	}
	return -1
}

func cloneExecutionView(view View) View {
	view.Definition = cloneDefinition(view.Definition)
	view.Input = cloneJSON(view.Input)
	view.Activations = append([]Activation(nil), view.Activations...)
	for index := range view.Activations {
		view.Activations[index].Output = cloneJSON(view.Activations[index].Output)
	}
	view.Effects = append([]Effect(nil), view.Effects...)
	for index := range view.Effects {
		view.Effects[index].Input = cloneJSON(view.Effects[index].Input)
		view.Effects[index].Output = cloneJSON(view.Effects[index].Output)
	}
	view.Waits = append([]Wait(nil), view.Waits...)
	for index := range view.Waits {
		view.Waits[index].Payload = cloneJSON(view.Waits[index].Payload)
		view.Waits[index].Response = cloneJSON(view.Waits[index].Response)
	}
	return view
}

func errorText(err error) string {
	if err == nil {
		return ErrInvalidExecution.Error()
	}
	return err.Error()
}
