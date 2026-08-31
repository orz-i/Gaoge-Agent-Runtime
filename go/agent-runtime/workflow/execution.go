package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/observability"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
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
	ErrInputSchema      = errors.New("workflow input violates definition schema")
	ErrOutputSchema     = errors.New("workflow output violates definition schema")
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
	EffectIDs []string         `json:"effectIDs,omitempty"`
	WaitID    string           `json:"waitID,omitempty"`
	Output    json.RawMessage  `json:"output,omitempty"`
	ErrorCode string           `json:"errorCode,omitempty"`
	Error     string           `json:"error,omitempty"`
}

// Effect is one stable external dispatch intent and terminal receipt.
type Effect struct {
	ID           string               `json:"id"`
	ActivationID string               `json:"activationID"`
	NodeID       string               `json:"nodeID"`
	Class        EffectClass          `json:"class"`
	Kind         string               `json:"kind"`
	Revision     string               `json:"revision,omitempty"`
	Definition   *DefinitionReference `json:"definition,omitempty"`
	OutputKey    string               `json:"outputKey,omitempty"`
	MapIndex     int                  `json:"mapIndex,omitempty"`
	Mapped       bool                 `json:"mapped,omitempty"`
	Compensation bool                 `json:"compensation,omitempty"`
	Input        json.RawMessage      `json:"input"`
	MaxCostUnits int64                `json:"maxCostUnits"`
	CostUnits    int64                `json:"costUnits"`
	NestedDepth  int                  `json:"nestedDepth"`
	Attempt      int                  `json:"attempt"`
	Retry        RetryPolicy          `json:"retry"`
	Status       EffectStatus         `json:"status"`
	ChildRunID   string               `json:"childRunID,omitempty"`
	ReceiptID    string               `json:"receiptID,omitempty"`
	Output       json.RawMessage      `json:"output,omitempty"`
	ErrorCode    string               `json:"errorCode,omitempty"`
	Error        string               `json:"error,omitempty"`
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
	NodeActivations   int   `json:"nodeActivations"`
	Effects           int   `json:"effects"`
	Segments          int   `json:"segments"`
	StateBytes        int   `json:"stateBytes"`
	CostUnitsUsed     int64 `json:"costUnitsUsed"`
	CostUnitsReserved int64 `json:"costUnitsReserved"`
}

// CompensationStatus is the durable lifecycle of one registered undo call.
type CompensationStatus string

const (
	CompensationPending   CompensationStatus = "pending"
	CompensationRunning   CompensationStatus = "running"
	CompensationCompleted CompensationStatus = "completed"
	CompensationFailed    CompensationStatus = "failed"
)

// Compensation is one undo call registered only after its do call succeeds.
type Compensation struct {
	ID        string             `json:"id"`
	NodeID    string             `json:"nodeID"`
	Call      EffectCall         `json:"call"`
	Input     json.RawMessage    `json:"input"`
	Status    CompensationStatus `json:"status"`
	EffectID  string             `json:"effectID,omitempty"`
	ReceiptID string             `json:"receiptID,omitempty"`
	ErrorCode string             `json:"errorCode,omitempty"`
	Error     string             `json:"error,omitempty"`
}

// FailureIntent preserves the original terminal request while compensations run.
type FailureIntent struct {
	Status kernel.RunStatus `json:"status"`
	Code   string           `json:"code"`
	Detail string           `json:"detail"`
}

// View is the public durable Workflow execution state.
type View struct {
	Definition    Definition      `json:"definition"`
	Input         json.RawMessage `json:"input"`
	NestedDepth   int             `json:"nestedDepth"`
	CurrentNode   int             `json:"currentNode"`
	Activations   []Activation    `json:"activations"`
	Effects       []Effect        `json:"effects"`
	Waits         []Wait          `json:"waits"`
	CurrentWaitID string          `json:"currentWaitID,omitempty"`
	Compensations []Compensation  `json:"compensations,omitempty"`
	Failure       *FailureIntent  `json:"failure,omitempty"`
	Budget        Budget          `json:"budget"`
}

// StartRequest starts one explicit Workflow Run from an exact compiled Definition.
type StartRequest struct {
	ID          string
	Actor       kernel.ActorRef
	Thread      kernel.ThreadRef
	RequestID   string
	Goal        string
	Definition  Definition
	Input       json.RawMessage
	NestedDepth int
}

// EffectRequest dispatches one already-persisted stable intent.
type EffectRequest struct {
	RunID          string
	Actor          kernel.ActorRef
	Thread         kernel.ThreadRef
	DefinitionID   string
	DefinitionHash string
	EffectID       string
	NodeID         string
	Class          EffectClass
	Kind           string
	Revision       string
	Definition     *DefinitionReference
	OutputKey      string
	MapIndex       int
	Compensation   bool
	Input          json.RawMessage
	MaxCostUnits   int64
	NestedDepth    int
	Attempt        int
	MaxAttempts    int
	Policy         DefinitionPolicy
}

// EffectResult is one executor observation. Completed results require a receipt.
type EffectResult struct {
	Disposition EffectDisposition
	ChildRunID  string
	ReceiptID   string
	Output      json.RawMessage
	ErrorCode   string
	ErrorDetail string
	CostUnits   int64
}

// EffectExecutor consumes only stable intents that already exist in Kernel state.
type EffectExecutor interface {
	Execute(context.Context, EffectRequest) (EffectResult, error)
}

// Dependencies are the only requirements of the Workflow feature.
type Dependencies struct {
	Runtime         *kernel.Runtime
	Effects         EffectExecutor
	Registry        *DefinitionRegistry
	Relations       runrelation.Recorder
	Telemetry       []observability.Recorder
	Ceiling         Limits
	DeferResumption bool
}

// Runner owns deterministic Workflow interpretation and no background workers.
type Runner struct {
	runtime     *kernel.Runtime
	effects     EffectExecutor
	relations   runrelation.Recorder
	telemetry   *observability.Set
	ceiling     Limits
	deferResume bool
	registry    *DefinitionRegistry
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
	telemetry, err := observability.NewSet(dependencies.Telemetry...)
	if err != nil {
		return nil, errors.Join(ErrInvalidExecution, err)
	}
	ceiling := normalizeCeiling(dependencies.Ceiling)
	return &Runner{
		runtime: dependencies.Runtime, effects: dependencies.Effects,
		relations: dependencies.Relations, telemetry: telemetry, ceiling: ceiling,
		deferResume: dependencies.DeferResumption, registry: dependencies.Registry,
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
	if err := validateWorkflowContract(request.Definition.InputSchema, request.Input, ErrInputSchema); err != nil {
		return kernel.Snapshot{}, err
	}
	state := executionState{
		Definition: cloneDefinition(request.Definition), Input: cloneJSON(request.Input),
		NestedDepth: request.NestedDepth,
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
	runner.recordRunTelemetry(ctx, snapshot, state, observability.PhaseStarted)
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
		Events: []kernel.EventDraft{{Type: "workflow.wait.resolved", Message: waitID, Wakeup: true}},
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
		if state.Failure != nil {
			return runner.runCompensations(ctx, snapshot, state, &segment)
		}
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
	if nodeHasEffects(node) {
		return runner.advanceNodeEffects(ctx, snapshot, state, node, segment)
	}
	return snapshot, true, ErrInvalidExecution
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
	switch {
	case nodeHasEffects(node):
		prepared, err := runner.prepareNodeEffects(ctx, snapshot, state, node)
		if err != nil {
			return prepared, true, err
		}
		preparedState, err := decodeExecutionState(prepared.State)
		if err != nil {
			return kernel.Snapshot{}, true, err
		}
		return runner.advanceNodeEffects(ctx, prepared, preparedState, node, segment)
	case node.Type == NodeIf:
		return runner.executeIf(ctx, snapshot, state, node)
	case node.Type == NodeWait:
		waiting, err := runner.prepareWait(ctx, snapshot, state, node)
		return waiting, true, err
	case node.Type == NodeReturn:
		completed, err := runner.complete(ctx, snapshot, state, node)
		return completed, true, err
	default:
		failed, err := runner.fail(ctx, snapshot, state, "workflow.node_invalid", ErrInvalidExecution)
		return failed, true, err
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

func (runner *Runner) prepareWait(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	node Node,
) (kernel.Snapshot, error) {
	payload := node.Wait.Payload
	if node.Wait.Source != nil {
		var err error
		payload, err = resolveValueSource(state, *node.Wait.Source, nil, 0)
		if err != nil {
			return runner.fail(ctx, snapshot, state, "workflow.wait_payload", err)
		}
	}
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
		Payload: cloneJSON(payload), Status: WaitPending,
	})
	state.CurrentWaitID = waitID
	state.Budget.NodeActivations++
	checkpointPayload, err := json.Marshal(WaitRequest{
		WaitID: waitID, NodeID: node.ID, Kind: node.Wait.Kind, Payload: cloneJSON(payload),
	})
	if err != nil {
		return kernel.Snapshot{}, errors.Join(ErrInvalidExecution, err)
	}
	checkpoint := &kernel.Checkpoint{
		ID: waitID, Kind: workflowWaitCheckpointKind, Status: kernel.CheckpointPending,
		Payload: checkpointPayload, CreatedAt: runner.runtime.Now(),
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
	if err = validateWorkflowContract(state.Definition.OutputSchema, result, ErrOutputSchema); err != nil {
		return runner.fail(ctx, snapshot, state, "workflow.output_schema", err)
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
	completed, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: encoded, Checkpoint: snapshot.Checkpoint,
		Result: &kernel.Result{ContentType: "application/json", Content: cloneJSON(result)},
		Events: []kernel.EventDraft{{Type: "workflow.completed", Message: state.Definition.Hash}},
	})
	if err == nil {
		runner.recordRunTelemetry(ctx, completed, state, observability.PhaseCompleted)
	}
	return completed, err
}

func workflowReturnValue(state executionState, node ReturnNode) (json.RawMessage, error) {
	if node.Source != nil {
		return resolveValueSource(state, *node.Source, nil, 0)
	}
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
		Events: []kernel.EventDraft{{Type: "workflow.segment.yielded", Message: reason, Wakeup: true}},
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
	return runner.beginFailure(ctx, snapshot, state, kernel.RunStatusFailed, code, cause)
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
	node := state.Definition.Nodes[state.Activations[activationIndex].NodeIndex]
	next, err := nextNodeIndex(*state, node)
	if err != nil {
		return err
	}
	state.CurrentNode = next
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
	return request.Goal != "" && request.NestedDepth >= 0 &&
		request.NestedDepth <= request.Definition.Limits.MaxNestedDepth &&
		json.Valid(request.Input) && ValidateDefinition(request.Definition) == nil
}

func validateWorkflowContract(schema json.RawMessage, value json.RawMessage, contractErr error) error {
	validator, err := jsoncontract.Compile(schema)
	if err != nil {
		return errors.Join(ErrInvalidExecution, err)
	}
	if err = validator.Validate(value); err != nil {
		return errors.Join(ErrInvalidExecution, contractErr, err)
	}
	return nil
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
	if ceiling.MaxFanOut <= 0 {
		ceiling.MaxFanOut = 1024
	}
	if ceiling.MaxConcurrency <= 0 {
		ceiling.MaxConcurrency = 64
	}
	if ceiling.MaxNestedDepth <= 0 {
		ceiling.MaxNestedDepth = 32
	}
	if ceiling.MaxAttemptsPerEffect <= 0 {
		ceiling.MaxAttemptsPerEffect = 16
	}
	return ceiling
}

func withinCeiling(limits Limits, ceiling Limits) bool {
	return limits.MaxNodeActivations <= ceiling.MaxNodeActivations &&
		limits.MaxEffects <= ceiling.MaxEffects && limits.MaxSegments <= ceiling.MaxSegments &&
		limits.MaxActivationsPerSegment <= ceiling.MaxActivationsPerSegment &&
		limits.MaxStateBytes <= ceiling.MaxStateBytes && limits.MaxFanOut <= ceiling.MaxFanOut &&
		limits.MaxConcurrency <= ceiling.MaxConcurrency && limits.MaxNestedDepth <= ceiling.MaxNestedDepth &&
		limits.MaxAttemptsPerEffect <= ceiling.MaxAttemptsPerEffect
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
		view.Activations[index].EffectIDs = append([]string(nil), view.Activations[index].EffectIDs...)
		view.Activations[index].Output = cloneJSON(view.Activations[index].Output)
	}
	view.Effects = append([]Effect(nil), view.Effects...)
	for index := range view.Effects {
		view.Effects[index].Definition = cloneDefinitionReference(view.Effects[index].Definition)
		view.Effects[index].Retry = normalizeRetryPolicy(view.Effects[index].Retry)
		view.Effects[index].Input = cloneJSON(view.Effects[index].Input)
		view.Effects[index].Output = cloneJSON(view.Effects[index].Output)
	}
	view.Waits = append([]Wait(nil), view.Waits...)
	for index := range view.Waits {
		view.Waits[index].Payload = cloneJSON(view.Waits[index].Payload)
		view.Waits[index].Response = cloneJSON(view.Waits[index].Response)
	}
	view.Compensations = append([]Compensation(nil), view.Compensations...)
	for index := range view.Compensations {
		view.Compensations[index].Call.Definition = cloneDefinitionReference(view.Compensations[index].Call.Definition)
		view.Compensations[index].Call.Input.Value = cloneJSON(view.Compensations[index].Call.Input.Value)
		view.Compensations[index].Input = cloneJSON(view.Compensations[index].Input)
	}
	if view.Failure != nil {
		failure := *view.Failure
		view.Failure = &failure
	}
	return view
}

func errorText(err error) string {
	if err == nil {
		return ErrInvalidExecution.Error()
	}
	return err.Error()
}
