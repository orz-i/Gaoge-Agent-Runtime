package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	runtimebudget "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/observability"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const (
	RunKind          kernel.RunKind    = "agent"
	CapabilityRunner kernel.Capability = "agent.runner"

	autonomousWakeupDelay = time.Second
)

var (
	ErrInvalidRequest       = errors.New("invalid agent run request")
	ErrInvalidModelResponse = errors.New("invalid agent model response")
	ErrModelFailure         = errors.New("agent model failure")
	ErrModelInvocationBusy  = errors.New("agent model invocation execution is leased")
	ErrToolFailure          = errors.New("agent tool failure")
	// ErrToolOutcomeUnknown indicates an external dispatch intent was
	// committed but its terminal receipt is absent. Do not replay.
	ErrToolOutcomeUnknown = errors.New("agent external tool result is uncertain")
	// The deadline of an externally pending native Tool has elapsed, while
	// its physical result remains unknown. The Run is intentionally blocked
	// for provider-verified or authorized manual reconciliation.
	ErrToolOutcomeOverdue       = errors.New("agent external Tool wait deadline expired with unknown result")
	ErrToolReceiptMismatch      = errors.New("agent tool receipt identity or disposition mismatch")
	ErrToolVerificationRequired = errors.New("agent trusted tool outcome verifier is not configured")
	ErrToolLateResult           = errors.New("agent Tool completed after configured timeout")
	ErrModelLateResult          = errors.New("agent Model completed after configured timeout")
	ErrCallLimit                = errors.New("agent run call limit exceeded")
	ErrBudgetExceeded           = errors.New("agent run budget exceeded")
	ErrUsageUnavailable         = errors.New("agent model usage unavailable for configured token budget")
	ErrApprovalRequired         = errors.New("agent run approval is required")
	ErrRunTerminal              = errors.New("agent run is terminal")
)

func (runner *Runner) completeWithToolResult(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	result tools.ExecutionResult,
) (kernel.Snapshot, error) {
	if err := validateStructuredDocument(state.OutputContract, result.Content); err != nil {
		return runner.fail(ctx, snapshot, state, "agent.output_schema_invalid", err)
	}
	state.Budget.Usage.OutputBytes = len(result.Content)
	if dimension := runtimebudget.Exceeded(state.Budget.Limits, state.Budget.Usage); dimension != "" {
		return runner.fail(
			ctx, snapshot, state, "agent."+string(dimension)+"_budget",
			fmt.Errorf("%w: %s", ErrBudgetExceeded, dimension),
		)
	}
	encodedState, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	completed, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: encodedState, Checkpoint: snapshot.Checkpoint,
		Result: &kernel.Result{
			ContentType: "application/json",
			Content:     append(json.RawMessage(nil), result.Content...),
		},
		Events: []kernel.EventDraft{
			{Type: "tool.completed", Message: result.Receipt.Disposition},
			{Type: "agent.completed", Message: "Terminal Tool completed the Agent loop"},
		},
	})
	if err == nil {
		runner.publishToolEvent(ctx, completed, EventToolCompleted, publicToolCompletionReceipt(state, result.Receipt))
		runner.publishRunEvent(ctx, completed, EventRunCompleted, true)
	}
	return completed, err
}

func agentWakeupAt(now time.Time) *time.Time {
	wakeupAt := now.UTC().Add(autonomousWakeupDelay)
	return &wakeupAt
}

func (runner *Runner) validateCompletion(
	ctx context.Context,
	run kernel.Run,
	response model.Response,
) (CompletionCorrection, error) {
	for _, policy := range runner.completionPolicies {
		correction, err := policy.ValidateCompletion(ctx, run, response)
		if err != nil {
			return CompletionCorrection{}, err
		}
		if !completionCorrectionRequired(correction) {
			continue
		}
		correction.Code = strings.TrimSpace(correction.Code)
		correction.Message = strings.TrimSpace(correction.Message)
		correction.BlockedToolKeys = normalizedToolKeys(correction.BlockedToolKeys)
		if correction.Code == "" || correction.Message == "" {
			return CompletionCorrection{}, ErrInvalidRequest
		}
		return correction, nil
	}
	return CompletionCorrection{}, nil
}

func completionCorrectionRequired(value CompletionCorrection) bool {
	return strings.TrimSpace(value.Code) != "" ||
		strings.TrimSpace(value.Message) != "" ||
		len(value.BlockedToolKeys) != 0
}

func (runner *Runner) correctCompletion(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	response model.Response,
	correction CompletionCorrection,
) (kernel.Snapshot, error) {
	if state.CompletionRepair || state.Budget.Usage.LLMCalls >= state.Budget.Limits.MaxLLMCalls {
		return runner.fail(ctx, snapshot, state, "agent.completion_repair_exhausted", ErrInvalidModelResponse)
	}
	state.CompletionRepair = true
	if content := strings.TrimSpace(response.Content); content != "" {
		state.Messages = append(state.Messages, model.Message{Role: model.RoleAssistant, Content: content})
	}
	state.Messages = withSystemGuidance(
		state.Messages,
		"Completion rejected by the Runtime: "+correction.Message+
			" The previous response was not delivered to the user. This is the only terminal repair attempt. Tools and delegation are unavailable. Finish using successful results already present in this transcript; clearly state gaps and never invent sources.",
	)
	state.BlockedToolKeys = normalizedToolKeys(append(state.BlockedToolKeys, correction.BlockedToolKeys...))
	state.RequireToolCall = false
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{
			Type: "agent.completion_corrected", Message: correction.Code, Wakeup: true,
			WakeupAt: agentWakeupAt(runner.clock.Now()),
		}},
	})
}

func compactCompletionPolicies(values []CompletionPolicy) []CompletionPolicy {
	result := make([]CompletionPolicy, 0, len(values))
	for _, value := range values {
		if value != nil {
			result = append(result, value)
		}
	}
	return result
}

// CompletionCorrection describes a recoverable terminal-output contract
// violation. The invalid model response is kept in the durable transcript and
// the same Agent Run is asked to repair only its terminal answer.
type CompletionCorrection struct {
	Code            string
	Message         string
	BlockedToolKeys []string
}

// CompletionPolicy lets a product validate a proposed terminal Agent response
// before the Runtime commits the Run as completed. It cannot mutate topology or
// call the provider; it may only accept the response, request one bounded
// correction, or return a fatal validation error.
type CompletionPolicy interface {
	ValidateCompletion(context.Context, kernel.Run, model.Response) (CompletionCorrection, error)
}

func runInvocation(operation plugin.RunOperation, snapshot kernel.Snapshot) plugin.RunInvocation {
	return plugin.RunInvocation{
		Operation: operation, Kind: snapshot.Run.Kind, RunID: snapshot.Run.ID,
		Actor: snapshot.Run.Actor, Thread: snapshot.Run.Thread, Goal: snapshot.Run.Goal,
	}
}

// Result is the structured terminal Agent output when hosted Tool facts or artifacts are present.
type Result struct {
	Content         string                 `json:"content,omitempty"`
	HostedToolCalls []model.HostedToolCall `json:"hostedToolCalls,omitempty"`
	Artifacts       []model.ArtifactRef    `json:"artifacts,omitempty"`
	Citations       []string               `json:"citations,omitempty"`
}

// HostedToolCatalog resolves provider-hosted Tool activations by canonical Tool Key.
type HostedToolCatalog interface {
	Resolve(context.Context, string, string) (model.HostedTool, bool, error)
}

// Limits are the common hard ceilings configured for one direct Agent loop.
// The alias keeps Agent composition concise while the vocabulary is shared by
// other feature-owned budget projections.
type Limits = runtimebudget.Limits

// ModelRequestPreparer projects messages and Host-scoped Tool presentation
// before the logical ModelInvocation intent is committed. It must not call a
// provider or expand Tool authority, modify model identity or execution limits.
// The complete request is frozen; recovery never invokes a preparer for a
// previously committed pending intent.
type ModelRequestPreparer func(context.Context, model.Request) (model.Request, error)

// ChainModelRequestPreparers composes trusted pre-intent projections in order.
// Every stage runs before the single ModelInvocation intent is persisted.
func ChainModelRequestPreparers(preparers ...ModelRequestPreparer) ModelRequestPreparer {
	stages := append([]ModelRequestPreparer(nil), preparers...)
	return func(ctx context.Context, request model.Request) (model.Request, error) {
		var err error
		for _, prepare := range stages {
			if prepare == nil {
				return model.Request{}, ErrInvalidRequest
			}
			request, err = prepare(ctx, request)
			if err != nil {
				return model.Request{}, err
			}
		}
		return request, nil
	}
}

// Dependencies explicitly provide the direct Agent loop capabilities.
type Dependencies struct {
	Runtime          *kernel.Runtime
	Model            model.Client
	Clock            kernel.Clock
	Catalog          tools.Catalog
	Executor         tools.Executor
	Approvals        plugin.ApprovalHandler
	ApprovalPolicies []plugin.ApprovalPolicy
	HostedTools      HostedToolCatalog
	ToolDiscovery    ToolDiscovery
	Observers        []plugin.Observer
	Telemetry        []observability.Recorder
	RunMiddleware    []plugin.RunMiddleware
	ModelMiddleware  []plugin.ModelMiddleware
	// PrepareModelRequest is the only pre-intent message/Tool schema projection
	// boundary. ModelMiddleware cannot rewrite persisted model-visible input.
	PrepareModelRequest ModelRequestPreparer
	ToolMiddleware      []plugin.ToolMiddleware
	CompletionPolicies  []CompletionPolicy
	Limits              Limits
	Execution           ExecutionPolicy
	// RunExecution opts the direct Agent StartRun/Resume path into the existing
	// authoritative Store epoch/lease. Nil retains the legacy behavior.
	RunExecution *RunExecution
	// ToolOutcomeVerifier is a trusted provider-side lookup, used only
	// for reconciling an uncertain native Tool dispatch. It does NOT retry.
	ToolOutcomeVerifier ToolOutcomeVerifier
	// DeferResumption leaves resolved approval transitions running for a composed
	// continuation worker. The standalone SDK keeps synchronous behavior by default.
	DeferResumption bool
}

// StartRequest starts one direct Agent Run.
type StartRequest struct {
	ID               string
	Actor            kernel.ActorRef
	Thread           kernel.ThreadRef
	RequestID        string
	Goal             string
	Instructions     string
	Model            string
	ModelOptions     json.RawMessage
	OutputContract   *model.OutputContract
	ToolKeys         []string
	RequiredToolKeys []string
	// InitialToolKeys are already authorized local tools shown on the first
	// model request when discovery is active. Unlike RequiredToolKeys, they
	// do NOT impose a mandatory invocation before completion.
	InitialToolKeys  []string
	HostedToolGrants []HostedToolGrant
	DeadlineAt       *time.Time
	Limits           Limits
	// CallAllowance is a renewable segment allowance, never the absolute hard ceiling.
	CallAllowance *CallAllowance
	// AutoAllowance is an opt-in bounded P2 policy, frozen with the Agent Run.
	AutoAllowance *AutoAllowancePolicy
}

// Runner owns only the direct Agent model and Tool loop.
type Runner struct {
	runtime             *kernel.Runtime
	model               model.Client
	clock               kernel.Clock
	catalog             tools.Catalog
	executor            tools.Executor
	approvals           plugin.ApprovalHandler
	approvalPolicies    *plugin.ApprovalPolicySet
	hostedTools         HostedToolCatalog
	discovery           ToolDiscovery
	observers           *plugin.ObserverSet
	telemetry           *observability.Set
	runChain            *plugin.RunChain
	modelChain          *plugin.ModelChain
	prepareModelRequest ModelRequestPreparer
	toolChain           *plugin.ToolChain
	completionPolicies  []CompletionPolicy
	limits              Limits
	execution           ExecutionPolicy
	runExecution        *RunExecution
	activeRunLease      *kernel.ExecutionLease
	toolOutcomeVerifier ToolOutcomeVerifier
	modelLimiter        *callLimiter
	toolLimiter         *callLimiter
	deferResume         bool
}

type runState struct {
	Messages         []model.Message        `json:"messages"`
	Model            string                 `json:"model,omitempty"`
	ModelOptions     json.RawMessage        `json:"modelOptions,omitempty"`
	OutputContract   *model.OutputContract  `json:"outputContract,omitempty"`
	ToolKeys         []string               `json:"toolKeys"`
	RequiredToolKeys []string               `json:"requiredToolKeys,omitempty"`
	HostedToolGrants []HostedToolGrant      `json:"hostedToolGrants,omitempty"`
	Discovery        *discoveryState        `json:"discovery,omitempty"`
	BlockedToolKeys  []string               `json:"blockedToolKeys,omitempty"`
	RequireToolCall  bool                   `json:"requireToolCall,omitempty"`
	CompletionRepair bool                   `json:"completionRepair,omitempty"`
	Budget           runtimebudget.Snapshot `json:"budget"`
	CallAllowance    *CallAllowance         `json:"callAllowance,omitempty"`
	AutoAllowance    *autoAllowanceState    `json:"autoAllowance,omitempty"`
	PendingCalls     []tools.Call           `json:"pendingCalls,omitempty"`
	PendingApproval  *pendingToolApproval   `json:"pendingApproval,omitempty"`
	ToolWait         *PendingTool           `json:"toolWait,omitempty"`
	DeferredChildren []deferredChildCall    `json:"deferredChildren,omitempty"`
	// A fenced Direct Agent commits its Tool dispatch ID before invoking
	// external effects. A missing receipt requires explicit reconciliation.
	ToolDispatchID   string                     `json:"toolDispatchID,omitempty"`
	ExternalSignals  []ExternalToolResultSignal `json:"externalSignals,omitempty"`
	ModelInvocations []ModelInvocation          `json:"modelInvocations,omitempty"`
}

// View is an isolated public projection of one persisted Agent Run. It exposes
// the durable transcript and bounded usage state without leaking the private
// execution representation or allowing callers to mutate Kernel state.
type View struct {
	Messages         []model.Message
	Model            string
	ModelOptions     json.RawMessage
	OutputContract   *model.OutputContract
	ToolKeys         []string
	RequiredToolKeys []string
	Budget           runtimebudget.Snapshot
	CallAllowance    *CallAllowance
	AutoAllowance    *AutoAllowanceProgress
	ModelInvocations []ModelInvocation
	PendingTool      *PendingToolView
}

// ViewState decodes an isolated public view from a Kernel Agent snapshot.
func ViewState(snapshot kernel.Snapshot) (View, error) {
	state, err := decodeState(snapshot.State)
	if err != nil {
		return View{}, err
	}
	return View{
		Messages:         publicModelMessages(state.Messages),
		Model:            state.Model,
		ModelOptions:     cloneRawJSON(state.ModelOptions),
		OutputContract:   model.CloneOutputContract(state.OutputContract),
		ToolKeys:         append([]string(nil), state.ToolKeys...),
		RequiredToolKeys: append([]string(nil), state.RequiredToolKeys...),
		Budget:           state.Budget,
		CallAllowance:    cloneCallAllowance(state.CallAllowance),
		AutoAllowance:    autoAllowanceProgress(state.AutoAllowance),
		ModelInvocations: publicModelInvocations(state.ModelInvocations),
		PendingTool:      publicPendingTool(snapshot, state.ToolWait),
	}, nil
}

// Provider replay payloads contain opaque signed Model content. Retain them in
// authoritative State, but do not expose them in ordinary public projections.
func publicModelMessages(values []model.Message) []model.Message {
	messages := model.CloneMessages(values)
	for index := range messages {
		messages[index].ProviderReplay = nil
	}
	return messages
}

func publicModelInvocations(values []ModelInvocation) []ModelInvocation {
	invocations := cloneModelInvocations(values)
	for index := range invocations {
		invocations[index].Response.ProviderReplay = nil
		invocations[index].Request.Messages = publicModelMessages(invocations[index].Request.Messages)
	}
	return invocations
}

// NewRunner constructs a direct Agent feature without planning or automatic routing.
func NewRunner(dependencies Dependencies) (*Runner, error) {
	if dependencies.Runtime == nil || dependencies.Model == nil ||
		(dependencies.Catalog == nil) != (dependencies.Executor == nil) {
		return nil, ErrInvalidRequest
	}
	runChain, err := plugin.NewRunChain(dependencies.RunMiddleware...)
	if err != nil {
		return nil, errors.Join(ErrInvalidRequest, err)
	}
	modelChain, err := plugin.NewModelChain(dependencies.ModelMiddleware...)
	if err != nil {
		return nil, errors.Join(ErrInvalidRequest, err)
	}
	toolChain, err := plugin.NewToolChain(dependencies.ToolMiddleware...)
	if err != nil {
		return nil, errors.Join(ErrInvalidRequest, err)
	}
	observers, err := plugin.NewObserverSet(dependencies.Observers...)
	if err != nil {
		return nil, errors.Join(ErrInvalidRequest, err)
	}
	telemetry, err := observability.NewSet(dependencies.Telemetry...)
	if err != nil {
		return nil, errors.Join(ErrInvalidRequest, err)
	}
	approvalPolicies, err := plugin.NewApprovalPolicySet(dependencies.ApprovalPolicies...)
	if err != nil {
		return nil, errors.Join(ErrInvalidRequest, err)
	}
	if !validAgentLimits(dependencies.Limits) {
		return nil, ErrInvalidRequest
	}
	if dependencies.Limits.MaxLLMCalls == 0 {
		dependencies.Limits.MaxLLMCalls = 8
	}
	if dependencies.Limits.MaxToolCalls == 0 {
		dependencies.Limits.MaxToolCalls = 16
	}
	if dependencies.Clock == nil {
		dependencies.Clock = agentClock{}
	}
	execution, err := dependencies.Execution.Resolve()
	if err != nil {
		return nil, errors.Join(ErrInvalidRequest, err)
	}
	var runExecution *RunExecution
	if dependencies.RunExecution != nil {
		if !dependencies.Runtime.SupportsExecution() {
			return nil, ErrInvalidRequest
		}
		if !validRunExecution(*dependencies.RunExecution, execution) {
			return nil, ErrInvalidRequest
		}
		copied := *dependencies.RunExecution
		runExecution = &copied
	}
	return &Runner{
		runtime: dependencies.Runtime, model: dependencies.Model, clock: dependencies.Clock, catalog: dependencies.Catalog,
		executor: dependencies.Executor, approvals: dependencies.Approvals,
		hostedTools: dependencies.HostedTools, discovery: dependencies.ToolDiscovery, observers: observers, telemetry: telemetry, approvalPolicies: approvalPolicies,
		completionPolicies:  compactCompletionPolicies(dependencies.CompletionPolicies),
		limits:              dependencies.Limits,
		execution:           execution,
		runExecution:        runExecution,
		toolOutcomeVerifier: dependencies.ToolOutcomeVerifier,
		modelLimiter:        newCallLimiter(execution.Model.MaxConcurrency),
		toolLimiter:         newCallLimiter(execution.Tool.MaxConcurrency),
		runChain:            runChain, modelChain: modelChain, toolChain: toolChain,
		prepareModelRequest: dependencies.PrepareModelRequest,
		deferResume:         dependencies.DeferResumption,
	}, nil
}

// Descriptor declares the explicit Agent capability graph.
func (runner *Runner) Descriptor() kernel.FeatureDescriptor {
	requires := []kernel.Capability{kernel.CapabilityRuntime}
	if runner != nil && runner.catalog != nil && runner.executor != nil {
		requires = append(requires, tools.CapabilityCatalog, tools.CapabilityExecutor)
	}
	return kernel.FeatureDescriptor{
		Name:     "agent",
		Requires: requires,
		Provides: []kernel.Capability{CapabilityRunner},
	}
}

// StartRun creates and drives one direct Agent Run until terminal or approval wait.
func (runner *Runner) StartRun(ctx context.Context, request StartRequest) (kernel.Snapshot, error) {
	return runner.startRunWithMiddleware(ctx, request, false)
}

// PrepareRun durably creates a Run without executing a model or Tool. A host
// using this method must compose a continuation worker for its wakeup event.
// It keeps a delegated child's lifetime outside the parent's Tool timeout.
func (runner *Runner) PrepareRun(ctx context.Context, request StartRequest) (kernel.Snapshot, error) {
	return runner.startRunWithMiddleware(ctx, request, true)
}

func (runner *Runner) startRunWithMiddleware(ctx context.Context, request StartRequest, deferred bool) (kernel.Snapshot, error) {
	if runner == nil || strings.TrimSpace(request.Goal) == "" ||
		(runner.runExecution != nil && strings.TrimSpace(request.ID) == "") {
		// Native durability cannot recover an uncertain Create when the
		// caller never knew the generated Run ID. RequestID alone is not a
		// Store-enforced unique idempotency key.
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	invocation := plugin.RunInvocation{
		Operation: plugin.RunStart, Kind: RunKind, RunID: strings.TrimSpace(request.ID),
		Actor: request.Actor, Thread: request.Thread, Goal: strings.TrimSpace(request.Goal),
	}
	return runner.runChain.Invoke(ctx, invocation, func(nextCtx context.Context) (kernel.Snapshot, error) {
		return runner.startRun(nextCtx, request, deferred)
	})
}

func (runner *Runner) startRun(ctx context.Context, request StartRequest, deferred bool) (kernel.Snapshot, error) {
	if err := runner.validateOutputCapability(ctx, request.Model, request.OutputContract); err != nil {
		return kernel.Snapshot{}, err
	}
	local, hosted, err := runner.resolveSelectedTools(ctx, request.ToolKeys, request.Model, request.DeadlineAt)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	for _, definition := range local {
		if definition.Key == ToolDiscoveryKey || definition.Name == discoveryToolName {
			return kernel.Snapshot{}, ErrToolDiscoveryInvalid
		}
	}
	for _, native := range hosted {
		if native.Key == ToolDiscoveryKey {
			// The reserved SDK control must never be shadowed by a provider
			// Hosted Tool, including an Eager Run with fewer than 16 locals.
			return kernel.Snapshot{}, ErrToolDiscoveryInvalid
		}
	}
	grants, err := normalizedHostedToolGrants(request.HostedToolGrants)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if !validHostedGrantSnapshot(grants, normalizedToolKeys(request.ToolKeys)) {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	if err = verifyHostedGrantVersions(grants, hosted); err != nil {
		return kernel.Snapshot{}, err
	}
	// Initial tools must be actual authorized local definitions, not opaque
	// Hosted grants or names supplied by the model/search adapter. Enforce
	// this for both Eager and Discovery sized runs.
	localKeys := make([]string, 0, len(local))
	for _, definition := range local {
		localKeys = append(localKeys, definition.Key)
	}
	if !toolKeysContainAll(normalizedToolKeys(localKeys), normalizedToolKeys(request.InitialToolKeys)) {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	discovery, err := runner.freezeToolDiscovery(request, local)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	limits, err := resolveRunLimits(runner.limits, request.Limits)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	allowance, err := resolvedCallAllowance(request.CallAllowance, limits)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	automaticAllowance, err := initialAutoAllowance(request.AutoAllowance, allowance, limits)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	toolKeys := normalizedToolKeys(request.ToolKeys)
	requiredToolKeys := normalizedToolKeys(request.RequiredToolKeys)
	if !toolKeysContainAll(toolKeys, requiredToolKeys) {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	state := runState{
		Messages:         []model.Message{{Role: model.RoleUser, Content: strings.TrimSpace(request.Goal)}},
		Model:            strings.TrimSpace(request.Model),
		ModelOptions:     cloneRawJSON(request.ModelOptions),
		OutputContract:   model.CloneOutputContract(request.OutputContract),
		ToolKeys:         toolKeys,
		RequiredToolKeys: requiredToolKeys,
		HostedToolGrants: grants,
		Discovery:        discovery,
		Budget:           runtimebudget.Snapshot{Limits: limits},
		CallAllowance:    allowance,
		AutoAllowance:    automaticAllowance,
	}
	if instructions := strings.TrimSpace(request.Instructions); instructions != "" {
		state.Messages = append([]model.Message{{Role: model.RoleSystem, Content: instructions}}, state.Messages...)
	}
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	snapshot, err := runner.runtime.Create(ctx, kernel.CreateRequest{
		ID: request.ID, Kind: RunKind, Actor: request.Actor, Thread: request.Thread,
		ExecutionRequired: runner.runExecution != nil,
		RequestID:         request.RequestID, Goal: request.Goal, DeadlineAt: request.DeadlineAt, State: encoded,
		Events: []kernel.EventDraft{{
			Type: "agent.started", Message: "Direct Agent loop started", Wakeup: true,
			WakeupAt: agentWakeupAt(runner.clock.Now()),
		}},
	})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if deferred {
		return snapshot, nil
	}
	// Claim the Run before publishing the started event or dispatching any
	// Model/Tool. If acquisition fails, preserve the created Run for safe
	// recovery rather than running unfenced.
	driven, driveErr := runner.withRunOwner(ctx, snapshot.Run.ID, snapshot.Run.Revision, func(driveCtx context.Context, scoped *Runner) (kernel.Snapshot, error) {
		scoped.publishRunEvent(driveCtx, snapshot, EventRunStarted, false)
		return scoped.drive(driveCtx, snapshot)
	})
	if driveErr != nil && driven.Run.ID == "" {
		return snapshot, driveErr
	}
	return driven, driveErr
}

// ResolveApproval resumes one waiting Agent Run with an explicit decision.
func (runner *Runner) ResolveApproval(
	ctx context.Context,
	runID string,
	expectedRevision uint64,
	response plugin.ApprovalResponse,
) (kernel.Snapshot, error) {
	if runner == nil || runner.approvals == nil {
		return kernel.Snapshot{}, ErrApprovalRequired
	}
	snapshot, state, err := runner.loadState(ctx, runID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if snapshot.Run.Revision != expectedRevision {
		return kernel.Snapshot{}, kernel.ErrConflict
	}
	if snapshot.Run.Status != kernel.RunStatusWaitingInput || len(state.PendingCalls) == 0 {
		return kernel.Snapshot{}, ErrApprovalRequired
	}
	invocation := runInvocation(plugin.RunResolveApproval, snapshot)
	return runner.runChain.Invoke(ctx, invocation, func(nextCtx context.Context) (kernel.Snapshot, error) {
		return runner.resolveApproval(nextCtx, snapshot, state, response)
	})
}

func (runner *Runner) resolveApproval(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	response plugin.ApprovalResponse,
) (kernel.Snapshot, error) {
	binding, ok := approvalForPendingCall(snapshot, state)
	if !ok || snapshot.Checkpoint.Status != kernel.CheckpointPending {
		return kernel.Snapshot{}, ErrApprovalRequired
	}
	state.PendingApproval = binding
	resolved, err := runner.approvals.ResolveToolApproval(snapshot.Checkpoint, response)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if !validResolvedApproval(snapshot.Checkpoint, resolved, response.Decision) {
		return kernel.Snapshot{}, ErrApprovalRequired
	}
	if response.Decision == plugin.ApprovalReject {
		// Rejection is also a resolved checkpoint, not an unproven nil.
		return runner.resumeRejected(ctx, snapshot, state, response, resolved)
	}
	return runner.resumeApproved(ctx, snapshot, state, resolved)
}

// LoadRun returns one Agent Run snapshot for parent orchestrators and recovery.
func (runner *Runner) LoadRun(ctx context.Context, runID string) (kernel.Snapshot, error) {
	if runner == nil || runner.runtime == nil {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	return runner.runtime.Load(ctx, runID)
}

// Resume continues one running Agent after a persisted approval resolution.
func (runner *Runner) Resume(ctx context.Context, runID string, expectedRevision uint64) (kernel.Snapshot, error) {
	snapshot, state, err := runner.loadState(ctx, runID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if snapshot.Run.Revision != expectedRevision {
		return kernel.Snapshot{}, kernel.ErrConflict
	}
	if snapshot.Run.Status == kernel.RunStatusWaitingInput {
		return snapshot, ErrApprovalRequired
	}
	if snapshot.Run.Status != kernel.RunStatusRunning {
		return snapshot, ErrRunTerminal
	}
	if invocation, active := activeModelInvocation(state); active && invocation.RetryAfter != nil &&
		runner.clock.Now().UTC().Before(invocation.RetryAfter.UTC()) {
		// A Queue WakeupAt is not an execution grant. Every direct or
		// Store-fenced Resume obeys the same persisted Feature due time.
		return snapshot, ErrModelInvocationBusy
	}
	if runner.runExecution != nil && state.ToolDispatchID != "" {
		// Do not acquire/execute an unresolved physical Tool under a new
		// Epoch. Distinguish an overdue provider wait without erasing its
		// persisted identity or asserting a physical COMMIT.
		if state.ToolWait != nil && state.ToolWait.DeadlineAt != nil &&
			!runner.clock.Now().Before(*state.ToolWait.DeadlineAt) {
			return snapshot, errors.Join(ErrToolOutcomeUnknown, ErrToolOutcomeOverdue)
		}
		return snapshot, ErrToolOutcomeUnknown
	}
	// Claim an execution segment BEFORE Run middleware and any Model/Tool
	// side effect. A runner without RunExecution still follows the original
	// pre-dispatch Store guard and will be rejected on fenced Runs.
	resumed, resumeErr := runner.withRunOwner(ctx, snapshot.Run.ID, snapshot.Run.Revision, func(driveCtx context.Context, scoped *Runner) (kernel.Snapshot, error) {
		invocation := runInvocation(plugin.RunResume, snapshot)
		return scoped.runChain.Invoke(driveCtx, invocation, func(nextCtx context.Context) (kernel.Snapshot, error) {
			return scoped.resumeRunning(nextCtx, snapshot, state)
		})
	})
	if resumeErr != nil && resumed.Run.ID == "" {
		return snapshot, resumeErr
	}
	return resumed, resumeErr
}

func (runner *Runner) drive(ctx context.Context, snapshot kernel.Snapshot) (kernel.Snapshot, error) {
	for snapshot.Run.Status == kernel.RunStatusRunning {
		// A bound lease can expire or be replaced between successive steps.
		if err := runner.runtime.RequireExecution(ctx, snapshot.Run.ID); err != nil {
			return snapshot, err
		}
		if err := runner.keepRunLeaseAlive(ctx); err != nil {
			return snapshot, err
		}
		next, done, err := runner.driveStep(ctx, snapshot)
		if err != nil || done {
			return next, err
		}
		snapshot = next
	}
	return snapshot, nil
}

func (runner *Runner) driveStep(ctx context.Context, snapshot kernel.Snapshot) (kernel.Snapshot, bool, error) {
	state, err := decodeState(snapshot.State)
	if err != nil {
		failed, failErr := runner.fail(ctx, snapshot, runState{}, "agent.state_invalid", err)
		return failed, true, failErr
	}
	if len(state.PendingCalls) == 0 && len(state.DeferredChildren) != 0 {
		return runner.activateDeferredChild(ctx, snapshot, state)
	}
	if len(state.PendingCalls) > 0 {
		if state.ToolWait != nil {
			waitSnapshot, ready, waitErr := runner.toolWaitReady(ctx, snapshot, state)
			if waitErr != nil || !ready {
				return waitSnapshot, true, waitErr
			}
		}
		if state.Discovery != nil && state.Discovery.RunID != snapshot.Run.ID {
			failed, failErr := runner.fail(ctx, snapshot, state, "agent.discovery_invalid", ErrToolDiscoveryInvalid)
			return failed, true, failErr
		}
		definitions, _, catalogErr := runner.resolveSelectedTools(ctx, state.ToolKeys, state.Model, snapshot.Run.DeadlineAt)
		if catalogErr == nil {
			catalogErr = validateDiscoveryCatalog(state.Discovery, definitions)
		}
		if catalogErr != nil {
			code := "agent.tool_invalid"
			if state.Discovery != nil {
				code = "agent.discovery_invalid"
			}
			failed, failErr := runner.fail(ctx, snapshot, state, code, catalogErr)
			return failed, true, failErr
		}
		if approvedPendingCall(snapshot, state) {
			executed, yielded, executeErr := runner.executePending(ctx, snapshot)
			return executed, yielded || executeErr != nil, executeErr
		}
		if state.PendingApproval != nil {
			failed, failErr := runner.fail(ctx, snapshot, state, "agent.approval_invalid", ErrApprovalRequired)
			return failed, true, failErr
		}
		prepared, prepareErr := runner.preparePendingApproval(ctx, snapshot, state, definitions)
		if prepareErr != nil {
			if code, message, recoverable := tools.RecoverableCallErrorInfo(prepareErr); recoverable {
				corrected, correctionErr := runner.recordRecoverableToolError(
					ctx, snapshot, state, code, message, tools.RecoverableCallErrorBlockedToolKeys(prepareErr), tools.RecoverableCallErrorAllowsCompletion(prepareErr),
				)
				return corrected, correctionErr != nil, correctionErr
			}
			return prepared, true, prepareErr
		}
		if prepared.Run.Status != kernel.RunStatusRunning {
			return prepared, true, prepareErr
		}
		executed, yielded, executeErr := runner.executePending(ctx, prepared)
		return executed, yielded || executeErr != nil, executeErr
	}
	if _, active := activeModelInvocation(state); !active && state.Budget.Usage.LLMCalls >= state.Budget.Limits.MaxLLMCalls {
		failed, failErr := runner.fail(ctx, snapshot, state, "agent.llm_limit", ErrCallLimit)
		return failed, true, failErr
	}
	// Never pause in the middle of a committed logical invocation: a replay
	// must first reconcile its durable receipt without reissuing the request.
	if _, active := activeModelInvocation(state); !active && allowanceExhausted(state, "model") {
		return runner.advanceCallAllowance(ctx, snapshot, state, "model")
	}
	snapshot, state, invocation, err := runner.ensureModelInvocation(ctx, snapshot, state)
	if err != nil {
		if errors.Is(err, ErrToolDiscoveryDenied) || errors.Is(err, ErrToolDiscoveryInvalid) {
			failed, failErr := runner.fail(ctx, snapshot, state, "agent.discovery_invalid", err)
			return failed, true, failErr
		}
		if code := model.ClassifiedErrorCode(err); code != "" && !model.IsRetryableError(err) {
			// A host's classified permanent preparation failure happens before
			// any intent or physical attempt. Commit the terminal cause so
			// supervising runs can observe it without spending model budget.
			failed, failErr := runner.fail(ctx, snapshot, state, code, err)
			return failed, true, failErr
		}
		return snapshot, true, err
	}
	snapshot, state, invocation, err = runner.claimModelInvocationExecution(ctx, snapshot, state, invocation)
	if err != nil {
		return snapshot, true, err
	}
	snapshot, state, err = runner.executeModelInvocation(ctx, snapshot, state, invocation)
	if err != nil {
		if errors.Is(err, ErrToolDiscoveryDenied) || errors.Is(err, ErrToolDiscoveryInvalid) {
			failed, failErr := runner.fail(ctx, snapshot, state, "agent.discovery_invalid", err)
			return failed, true, failErr
		}
		var denied modelPreDispatchDenied
		if errors.As(err, &denied) && errors.Is(denied.cause, ErrInvalidRequest) {
			// A broken, frozen request cannot become valid on retry: report
			// the permanent Host protocol violation as a terminal Run error.
			// Expired/replaced execution Claims remain recoverable instead.
			failed, failErr := runner.fail(ctx, snapshot, state, "agent.model_dispatch_invalid", err)
			return failed, true, failErr
		}
		if errors.Is(err, runtimebudget.ErrExhausted) {
			failed, failErr := runner.fail(ctx, snapshot, state, sharedBudgetErrorCode(err), err)
			return failed, true, failErr
		}
		if errors.Is(err, kernel.ErrDeadline) {
			failed, failErr := runner.fail(
				ctx, snapshot, state, "agent.deadline_exceeded", errors.Join(kernel.ErrDeadline, err),
			)
			return failed, true, failErr
		}
		if model.IsRetryableError(err) {
			if int(invocation.ExecutionAttempt) >= runner.execution.Model.MaxAttempts {
				code := model.ClassifiedErrorCode(err)
				if code == "" {
					code = "agent.model_retry_exhausted"
				}
				failed, failErr := runner.fail(
					ctx, snapshot, state, code, errors.Join(ErrModelFailure, err),
				)
				return failed, true, failErr
			}
			released, releaseErr := runner.releaseModelInvocationExecution(ctx, snapshot, state, invocation)
			if releaseErr != nil {
				return released, true, errors.Join(err, releaseErr)
			}
			return released, true, err
		}
		if errors.Is(err, ErrModelFailure) || errors.Is(err, ErrInvalidModelResponse) {
			code := model.ClassifiedErrorCode(err)
			if code == "" {
				code = "agent.model_failed"
			}
			failed, failErr := runner.fail(ctx, snapshot, state, code, err)
			return failed, true, failErr
		}
		return snapshot, true, err
	}
	consumed, ok := consumeLastModelInvocation(&state, runner.clock.Now())
	if !ok {
		failed, failErr := runner.fail(ctx, snapshot, state, "agent.model_invocation_invalid", ErrInvalidRequest)
		return failed, true, failErr
	}
	definitions := modelDefinitions(consumed)
	response := model.CloneResponse(consumed.Response)
	budgetDimension, budgetErr := chargeConsumedModelUsage(&state, consumed)
	if budgetErr != nil {
		failed, failErr := runner.fail(ctx, snapshot, state, "agent.usage_invalid", budgetErr)
		return failed, true, failErr
	}
	if budgetDimension != "" {
		failed, failErr := runner.fail(
			ctx, snapshot, state, "agent."+string(budgetDimension)+"_budget",
			fmt.Errorf("%w: %s", ErrBudgetExceeded, budgetDimension),
		)
		return failed, true, failErr
	}
	if consumed.ErrorCode != "" {
		if consumed.ErrorCode != rejectedModelResponseCode {
			failed, failErr := runner.fail(ctx, snapshot, state, consumed.ErrorCode, ErrModelFailure)
			return failed, true, failErr
		}
		corrected, correctionErr := runner.correctCompletion(ctx, snapshot, state, model.Response{}, CompletionCorrection{
			Code: consumed.ErrorCode, Message: "the completed model response violated the declared response or tool contract",
		})
		return corrected, correctionErr != nil, correctionErr
	}
	if state.CompletionRepair && (response.Continue || len(response.ToolCalls) != 0 || len(response.HostedToolCalls) != 0 || response.Content == "" && len(response.Artifacts) == 0) {
		failed, failErr := runner.fail(ctx, snapshot, state, "agent.completion_repair_exhausted", ErrInvalidModelResponse)
		return failed, true, failErr
	}
	if response.Refused && state.OutputContract != nil {
		failed, failErr := runner.fail(ctx, snapshot, state, "agent.output_refused", ErrOutputRefused)
		return failed, true, failErr
	}
	if response.Continue {
		continued, continueErr := runner.continueModel(ctx, snapshot, state, response)
		return continued, true, continueErr
	}
	if len(response.ToolCalls) == 0 {
		if missing := missingRequiredToolKeys(state); len(missing) != 0 {
			if state.CompletionRepair {
				failed, failErr := runner.fail(ctx, snapshot, state, "agent.completion_repair_exhausted", ErrInvalidModelResponse)
				return failed, true, failErr
			}
			corrected, correctionErr := runner.correctRequiredToolCompletion(ctx, snapshot, state, response, missing)
			return corrected, correctionErr != nil, correctionErr
		}
		structuredCorrection, structuredErr := structuredOutputCorrection(state.OutputContract, response)
		if structuredErr != nil {
			failed, failErr := runner.fail(ctx, snapshot, state, "agent.output_refused", structuredErr)
			return failed, true, failErr
		}
		if completionCorrectionRequired(structuredCorrection) {
			corrected, correctionErr := runner.correctCompletion(ctx, snapshot, state, response, structuredCorrection)
			return corrected, correctionErr != nil, correctionErr
		}
		correction, validationErr := runner.validateCompletion(ctx, snapshot.Run, response)
		if validationErr != nil {
			failed, failErr := runner.fail(ctx, snapshot, state, "agent.completion_validation_failed", validationErr)
			return failed, true, failErr
		}
		if completionCorrectionRequired(correction) {
			corrected, correctionErr := runner.correctCompletion(ctx, snapshot, state, response, correction)
			return corrected, correctionErr != nil, correctionErr
		}
		completed, completeErr := runner.complete(ctx, snapshot, state, response)
		return completed, true, completeErr
	}
	if state.Budget.Usage.ToolCalls+len(response.ToolCalls) > state.Budget.Limits.MaxToolCalls {
		failed, failErr := runner.fail(ctx, snapshot, state, "agent.tool_limit", ErrCallLimit)
		return failed, true, failErr
	}
	queued, err := runner.queueToolCalls(
		ctx,
		snapshot,
		state,
		definitions,
		response.Content,
		response.ToolCalls,
		response.ProviderReplay,
	)
	if err != nil {
		return queued, true, err
	}
	return queued, false, nil
}

type completedToolCall struct {
	call   tools.Call
	result string
}

// unproductiveToolKeys enforces a bounded exit from repeated empty Tool
// discovery and recoverable Tool failures. It counts only the latest
// consecutive settled outcomes; an intervening successful/different Tool
// clears the streak. Distinct call IDs and argument changes cannot reset it.
// This is a request-projection guard, rebuilt from durable transcript after
// restart, never an added Tool grant or a retry of an uncertain write.
func unproductiveToolKeys(messages []model.Message) []string {
	keysByCallID := toolCallKeysByID(messages)
	lastKey, count := "", 0
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.Role == model.RoleAssistant && len(message.ToolCalls) == 0 && strings.TrimSpace(message.Content) != "" {
			break
		}
		if message.Role != model.RoleTool {
			continue
		}
		key := keysByCallID[message.ToolCallID]
		if key == "" || key != lastKey && lastKey != "" {
			break
		}
		if key == ToolDiscoveryKey {
			var result struct {
				ToolKeys []string `json:"toolKeys"`
			}
			if json.Unmarshal([]byte(message.Content), &result) != nil || len(result.ToolKeys) != 0 || !strings.Contains(message.Content, `"toolKeys"`) {
				break
			}
		} else if !recoverableToolResult(message.Content) && !unavailableEvidenceToolResult(message.Content) {
			break
		}
		lastKey, count = key, count+1
		if count >= 3 || count >= 2 && (key == ToolDiscoveryKey || unavailableEvidenceToolResult(message.Content)) {
			return []string{key}
		}
	}
	return nil
}

func repeatedUnchangedToolKeys(messages []model.Message) []string {
	batches := completedToolBatches(messages)
	keys := make([]string, 0)
	seen := make(map[string]struct{})
	for batchIndex, batch := range batches {
		for _, current := range batch {
			if _, exists := seen[current.call.ToolKey]; exists || recoverableToolResult(current.result) {
				continue
			}
			if completedBatchesContain(batches[batchIndex+1:], current) {
				seen[current.call.ToolKey] = struct{}{}
				keys = append(keys, current.call.ToolKey)
			}
		}
	}
	return keys
}

// Explicit denial of fresh source access is final for that member. Two such
// denials of the same delegation Tool (even with different role arguments)
// are sufficient to remove it from future model declarations for this Run.
// No other failed Tool body is interpreted as a capability prohibition.
func unavailableEvidenceToolResult(content string) bool {
	var result struct {
		OK        *bool `json:"ok"`
		Retryable bool  `json:"retryable"`
		Error     struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	return json.Unmarshal([]byte(content), &result) == nil && result.OK != nil && !*result.OK &&
		!result.Retryable && result.Error.Code == "delegation.current_evidence_unavailable"
}

func completedToolBatches(messages []model.Message) [][]completedToolCall {
	batches := make([][]completedToolCall, 0)
	for end := len(messages); end > 0; {
		batch, previousEnd, ok := completedToolBatchEndingAt(messages, end)
		if !ok {
			break
		}
		batches = append(batches, batch)
		end = previousEnd
	}
	return batches
}

func completedBatchesContain(batches [][]completedToolCall, target completedToolCall) bool {
	for _, batch := range batches {
		if completedBatchContains(batch, target) {
			return true
		}
	}
	return false
}

func completedToolBatchEndingAt(messages []model.Message, end int) ([]completedToolCall, int, bool) {
	toolStart := end
	for toolStart > 0 && messages[toolStart-1].Role == model.RoleTool {
		toolStart--
	}
	if toolStart == end || toolStart == 0 {
		return nil, 0, false
	}
	assistantIndex := toolStart - 1
	completed, ok := completedToolCalls(messages[assistantIndex], messages[toolStart:end])
	return completed, assistantIndex, ok
}

func completedToolCalls(assistant model.Message, toolResults []model.Message) ([]completedToolCall, bool) {
	if assistant.Role != model.RoleAssistant || len(assistant.ToolCalls) != len(toolResults) {
		return nil, false
	}
	results := make(map[string]string, len(toolResults))
	for _, toolResult := range toolResults {
		results[toolResult.ToolCallID] = strings.TrimSpace(toolResult.Content)
	}
	completed := make([]completedToolCall, 0, len(assistant.ToolCalls))
	for _, call := range assistant.ToolCalls {
		result, ok := results[call.ID]
		if !ok || strings.TrimSpace(call.ToolKey) == "" {
			return nil, false
		}
		completed = append(completed, completedToolCall{call: call, result: result})
	}
	return completed, true
}

func completedBatchContains(batch []completedToolCall, target completedToolCall) bool {
	for _, candidate := range batch {
		if candidate.result == target.result && sameToolCall(candidate.call, target.call) {
			return true
		}
	}
	return false
}

func sameToolCall(left tools.Call, right tools.Call) bool {
	return left.ToolKey == right.ToolKey &&
		strings.TrimSpace(string(left.Arguments)) == strings.TrimSpace(string(right.Arguments))
}

func recoverableToolResult(content string) bool {
	var payload struct {
		OK        *bool `json:"ok"`
		Retryable bool  `json:"retryable"`
	}
	if !json.Valid([]byte(content)) || json.Unmarshal([]byte(content), &payload) != nil {
		return false
	}
	return payload.OK != nil && !*payload.OK && payload.Retryable
}

func definitionsWithoutKeys(definitions []tools.Definition, keys []string) []tools.Definition {
	blocked := toolKeySet(keys)
	result := make([]tools.Definition, 0, len(definitions))
	for _, definition := range definitions {
		if _, suppressed := blocked[definition.Key]; !suppressed {
			result = append(result, definition)
		}
	}
	return result
}

func hostedToolsWithoutKeys(hostedTools []model.HostedTool, keys []string) []model.HostedTool {
	blocked := toolKeySet(keys)
	result := make([]model.HostedTool, 0, len(hostedTools))
	for _, hostedTool := range hostedTools {
		if _, suppressed := blocked[hostedTool.Key]; !suppressed {
			result = append(result, hostedTool)
		}
	}
	return result
}

func toolKeySet(keys []string) map[string]struct{} {
	result := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		result[key] = struct{}{}
	}
	return result
}

func toolKeysContainAll(toolKeys, required []string) bool {
	available := toolKeySet(toolKeys)
	for _, key := range required {
		if _, ok := available[key]; !ok {
			return false
		}
	}
	return true
}

func missingRequiredToolKeys(state runState) []string {
	if len(state.RequiredToolKeys) == 0 {
		return nil
	}
	completed := completedToolKeys(state.Messages)
	missing := make([]string, 0, len(state.RequiredToolKeys))
	for _, key := range state.RequiredToolKeys {
		if _, ok := completed[key]; !ok {
			missing = append(missing, key)
		}
	}
	return missing
}

func completedToolKeys(messages []model.Message) map[string]struct{} {
	calls := toolCallKeysByID(messages)
	completed := make(map[string]struct{})
	for _, message := range messages {
		if message.Role != model.RoleTool || toolResultRejectedOrRetryable(message.Content) {
			continue
		}
		if key := calls[strings.TrimSpace(message.ToolCallID)]; key != "" {
			completed[key] = struct{}{}
		}
	}
	return completed
}

func toolCallKeysByID(messages []model.Message) map[string]string {
	calls := make(map[string]string)
	for _, message := range messages {
		if message.Role != model.RoleAssistant {
			continue
		}
		for _, call := range message.ToolCalls {
			id, key := strings.TrimSpace(call.ID), strings.TrimSpace(call.ToolKey)
			if id != "" && key != "" {
				calls[id] = key
			}
		}
	}
	return calls
}

func toolResultRejectedOrRetryable(content string) bool {
	var payload struct {
		OK        *bool  `json:"ok"`
		Retryable bool   `json:"retryable"`
		Status    string `json:"status"`
	}
	if !json.Valid([]byte(content)) || json.Unmarshal([]byte(content), &payload) != nil {
		return true
	}
	return strings.TrimSpace(payload.Status) == "rejected" ||
		(payload.OK != nil && !*payload.OK) || payload.Retryable
}

func (runner *Runner) correctRequiredToolCompletion(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	_ model.Response,
	missing []string,
) (kernel.Snapshot, error) {
	state.BlockedToolKeys = normalizedToolKeys(append(
		state.BlockedToolKeys,
		toolKeysExcept(state.ToolKeys, missing)...,
	))
	state.RequireToolCall = true
	guidance := "Completion rejected because these required Tools have not completed successfully: " +
		strings.Join(missing, ", ") + ". All other Tools are now unavailable. " +
		"Use the successful Tool results already present in the transcript and call a missing required Tool next. " +
		"The previous response was not delivered to the user; do not repeat it as the answer."
	state.Messages = withSystemGuidance(state.Messages, guidance)
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{
			Type: "agent.completion_corrected", Message: strings.Join(missing, ","), Wakeup: true,
			WakeupAt: agentWakeupAt(runner.clock.Now()),
		}},
	})
}

func toolKeysExcept(toolKeys []string, keep []string) []string {
	kept := make(map[string]struct{}, len(keep))
	for _, key := range keep {
		kept[strings.TrimSpace(key)] = struct{}{}
	}
	excluded := make([]string, 0, len(toolKeys))
	for _, key := range toolKeys {
		key = strings.TrimSpace(key)
		if _, ok := kept[key]; key != "" && !ok {
			excluded = append(excluded, key)
		}
	}
	return excluded
}

func withSystemGuidance(messages []model.Message, guidance string) []model.Message {
	for index := range messages {
		if messages[index].Role == model.RoleSystem {
			messages[index].Content = strings.TrimSpace(messages[index].Content) + "\n\n" + guidance
			return messages
		}
	}
	return append([]model.Message{{Role: model.RoleSystem, Content: guidance}}, messages...)
}

func withRepeatedToolGuidance(messages []model.Message, toolKeys []string) []model.Message {
	guidance := "These exact Tool calls have already returned the same successful results twice: " +
		strings.Join(toolKeys, ", ") + ". " +
		"Do not repeat them now; continue from the existing results, choose a different action, or answer the user."
	for index := range messages {
		if messages[index].Role == model.RoleSystem {
			messages[index].Content = strings.TrimSpace(messages[index].Content) + "\n\n" + guidance
			return messages
		}
	}
	return append([]model.Message{{Role: model.RoleSystem, Content: guidance}}, messages...)
}

func withBlockedToolGuidance(messages []model.Message, toolKeys []string) []model.Message {
	guidance := "These Tools are no longer available for this run: " +
		strings.Join(toolKeys, ", ") + ". Continue now using only the remaining Tools."
	for index := range messages {
		if messages[index].Role == model.RoleSystem {
			messages[index].Content = strings.TrimSpace(messages[index].Content) + "\n\n" + guidance
			return messages
		}
	}
	return append([]model.Message{{Role: model.RoleSystem, Content: guidance}}, messages...)
}

func (runner *Runner) generateModel(
	ctx context.Context, request model.Request, beforeProvider func(context.Context) error,
) (model.Response, error) {
	emit := func(event model.StreamEvent) error {
		runner.publishValue(ctx, request.RunID, plugin.Event{
			Type: EventModelDelta, Delta: event.Delta,
		}, event)
		return nil
	}
	// All provider-visible instructions and Tool schemas were sealed before
	// persisting the ModelInvocation. Middleware is strictly observational or
	// budget-narrowing: no late message/schema compatibility injection.
	frozen := model.CloneRequest(request)
	terminal := func(callCtx context.Context, candidate model.Request, sink model.StreamSink) (model.Response, error) {
		if !validDispatchModelRequest(frozen, candidate) {
			return model.Response{}, modelPreDispatchDenied{cause: ErrInvalidRequest}
		}
		if beforeProvider != nil {
			// The final chance to reject an expired or stolen invocation claim
			// is AFTER ModelMiddleware, before physical Provider dispatch.
			if err := beforeProvider(callCtx); err != nil {
				// This is an execution admission rejection, not a Provider
				// failure. The caller must not terminalize a recoverable Run.
				return model.Response{}, modelPreDispatchDenied{cause: err}
			}
		}
		return runner.generateProviderModel(callCtx, candidate, sink)
	}
	return runner.modelChain.Invoke(ctx, request, emit, terminal)
}

func (runner *Runner) generateProviderModel(
	ctx context.Context,
	request model.Request,
	emit model.StreamSink,
) (model.Response, error) {
	streaming, ok := runner.model.(model.StreamingClient)
	if !ok {
		return runner.model.Generate(ctx, request)
	}
	var responseID string
	var usage *model.Usage
	response, err := streaming.GenerateStream(ctx, request, func(event model.StreamEvent) error {
		if strings.TrimSpace(event.ResponseID) != "" {
			responseID = strings.TrimSpace(event.ResponseID)
		}
		if event.Usage != nil {
			usage = model.CloneUsage(event.Usage)
		}
		if emit == nil {
			return nil
		}
		return emit(event)
	})
	if err != nil {
		return response, err
	}
	if strings.TrimSpace(response.ResponseID) == "" {
		response.ResponseID = responseID
	}
	if response.Usage == nil {
		response.Usage = usage
	}
	return response, nil
}

func (runner *Runner) resolveSelectedTools(
	ctx context.Context,
	keys []string,
	modelName string,
	deadlineAt *time.Time,
) ([]tools.Definition, []model.HostedTool, error) {
	local := make([]tools.Definition, 0, len(keys))
	hosted := make([]model.HostedTool, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, rawKey := range keys {
		key := strings.TrimSpace(rawKey)
		if key == "" {
			return nil, nil, tools.ErrToolNotFound
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		definition, hostedTool, err := runner.resolveSelectedTool(ctx, key, modelName, deadlineAt)
		if err != nil {
			return nil, nil, err
		}
		if definition != nil {
			local = append(local, *definition)
		} else {
			hosted = append(hosted, *hostedTool)
		}
	}
	return local, hosted, nil
}

func (runner *Runner) resolveSelectedTool(
	ctx context.Context,
	key string,
	modelName string,
	deadlineAt *time.Time,
) (*tools.Definition, *model.HostedTool, error) {
	if runner.catalog != nil {
		if definition, ok := runner.catalog.Resolve(key); ok {
			if err := tools.ValidateDefinition(definition); err != nil {
				return nil, nil, err
			}
			return &definition, nil, nil
		}
	}
	if runner.hostedTools == nil {
		return nil, nil, fmt.Errorf("%w: %s", tools.ErrToolNotFound, key)
	}
	resolved, ok, err := runner.resolveHostedToolWithPolicy(ctx, key, strings.TrimSpace(modelName), deadlineAt)
	if err != nil {
		return nil, nil, err
	}
	if !ok || strings.TrimSpace(resolved.Key) != key {
		return nil, nil, fmt.Errorf("%w: %s", tools.ErrToolNotFound, key)
	}
	resolved.Target = cloneRawJSON(resolved.Target)
	return nil, &resolved, nil
}

func (runner *Runner) queueToolCalls(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	definitions []tools.Definition,
	content string,
	calls []tools.Call,
	providerReplay *model.ProviderReplay,
) (kernel.Snapshot, error) {
	preparedCalls := make([]tools.Call, len(calls))
	seenCallIDs := make(map[string]struct{})
	for _, message := range state.Messages {
		for _, existing := range message.ToolCalls {
			if id := strings.TrimSpace(existing.ID); id != "" {
				seenCallIDs[id] = struct{}{}
			}
		}
	}
	for index, call := range calls {
		call.ToolKey = strings.TrimSpace(call.ToolKey)
		call.ID = strings.TrimSpace(call.ID)
		if call.ID == "" {
			callID, err := runner.runtime.NewID("toolcall")
			if err != nil {
				return kernel.Snapshot{}, err
			}
			call.ID = callID
		}
		if _, duplicate := seenCallIDs[call.ID]; duplicate {
			return runner.fail(ctx, snapshot, state, "agent.tool_invalid", tools.ErrInvalidCall)
		}
		seenCallIDs[call.ID] = struct{}{}
		definition, ok := selectedDefinition(definitions, call.ToolKey)
		if !ok || !json.Valid(call.Arguments) || definition.Terminal && index != len(calls)-1 ||
			call.ToolKey == ToolDiscoveryKey && len(calls) != 1 {
			return runner.fail(ctx, snapshot, state, "agent.tool_invalid", tools.ErrInvalidCall)
		}
		// Freeze the model-visible name with the call. Future discovery or
		// catalog changes must not alter the provider transcript on replay.
		call.Name = definition.Name
		preparedCalls[index] = call
		preparedCalls[index].Arguments = append(json.RawMessage(nil), call.Arguments...)
	}
	state.Messages = append(state.Messages, model.Message{
		Role: model.RoleAssistant, Content: strings.TrimSpace(content),
		ToolCalls: cloneToolCalls(preparedCalls), ProviderReplay: model.CloneProviderReplay(providerReplay),
	})
	state.PendingCalls = cloneToolCalls(preparedCalls)
	state.RequireToolCall = false
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	next, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{
			Type: "tool.batch_requested", Message: "Tool call batch requested", Wakeup: true,
			WakeupAt: agentWakeupAt(runner.clock.Now()),
		}},
	})
	if err == nil {
		for _, call := range preparedCalls {
			runner.publishValue(ctx, next.Run.ID, plugin.Event{
				Type: EventToolRequested, Revision: next.Run.Revision, Status: string(next.Run.Status),
			}, call)
		}
	}
	return next, err
}

func (runner *Runner) preparePendingApproval(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	definitions []tools.Definition,
) (kernel.Snapshot, error) {
	call, ok := nextPendingCall(state)
	if !ok {
		return runner.fail(ctx, snapshot, state, "agent.state_invalid", ErrInvalidRequest)
	}
	definition, ok := selectedDefinition(definitions, call.ToolKey)
	if !ok && call.ToolKey == ToolDiscoveryKey && state.Discovery != nil {
		definition, ok = tools.CloneDefinition(discoveryToolDefinition), true
	}
	if !ok {
		return runner.fail(ctx, snapshot, state, "agent.tool_invalid", tools.ErrInvalidCall)
	}
	if err := tools.ValidateCall(definition, call); err != nil {
		return snapshot, err
	}
	if call.ToolKey == ToolDiscoveryKey {
		return snapshot, nil // SDK control Tool is read-only, never needs business approval
	}
	invocation := plugin.ToolInvocation{
		Run:        snapshot.Run,
		Definition: tools.CloneDefinition(definition),
		Request: tools.ExecutionRequest{
			RunID: snapshot.Run.ID,
			Call:  tools.CloneCall(call),
		},
	}
	requiresApproval, err := runner.approvalPolicies.RequiresApproval(ctx, invocation)
	if err != nil {
		return runner.fail(ctx, snapshot, state, "agent.approval_policy_failed", err)
	}
	if !requiresApproval {
		return snapshot, nil
	}
	if runner.approvals == nil {
		return runner.fail(ctx, snapshot, state, "agent.approval_unavailable", ErrApprovalRequired)
	}
	checkpoint, err := runner.approvals.PrepareToolApproval(call, definition)
	if err != nil {
		return runner.fail(ctx, snapshot, state, "agent.approval_invalid", err)
	}
	binding, err := newPendingToolApproval(checkpoint, call)
	if err != nil || checkpoint.Status != kernel.CheckpointPending {
		return runner.fail(ctx, snapshot, state, "agent.approval_invalid", ErrApprovalRequired)
	}
	state.PendingApproval = binding
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	waiting, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusWaitingInput, State: encoded, Checkpoint: checkpoint,
		Events: []kernel.EventDraft{{Type: "interaction.created", Message: "Tool approval required"}},
	})
	if err == nil {
		runner.publishRunEvent(ctx, waiting, EventRunWaitingInput, false)
		runner.publishValue(ctx, waiting.Run.ID, plugin.Event{
			Type: EventInteractionRequired, Revision: waiting.Run.Revision, Status: string(waiting.Run.Status),
		}, checkpoint)
	}
	return waiting, err
}

func (runner *Runner) executePending(ctx context.Context, snapshot kernel.Snapshot) (kernel.Snapshot, bool, error) {
	execution, failCode, err := runner.preparePendingToolExecution(snapshot)
	if err != nil {
		if _, _, recoverable := tools.RecoverableCallErrorInfo(err); recoverable {
			return runner.handlePendingToolExecutionError(ctx, snapshot, execution.state, err)
		}
		failed, failErr := runner.fail(ctx, snapshot, execution.state, failCode, err)
		return failed, false, failErr
	}
	if allowanceExhausted(execution.state, "tool") {
		return runner.advanceCallAllowance(ctx, snapshot, execution.state, "tool")
	}
	if execution.call.ToolKey == ToolDiscoveryKey {
		return runner.searchPendingTool(ctx, snapshot, execution)
	}
	if runner.runExecution != nil {
		if execution.state.ToolDispatchID != "" {
			return snapshot, true, ErrToolOutcomeUnknown
		}
		state := execution.state
		state.ToolDispatchID = execution.call.ID
		encoded, encodeErr := encodeState(state)
		if encodeErr != nil {
			return snapshot, true, encodeErr
		}
		// The CAS prepares an effect with a stable identity. A crash after
		// this commit cannot be treated as permission to execute again.
		staged, stageErr := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
			Events: []kernel.EventDraft{{Type: "tool.dispatch_intent", Message: execution.call.ID, Wakeup: true}},
		})
		if stageErr != nil {
			return snapshot, true, stageErr
		}
		snapshot = staged
		execution.state = state
	}
	result, err := runner.invokePendingTool(ctx, snapshot, execution.call, execution.definition)
	late := errors.Is(err, ErrToolLateResult)
	if err != nil && !late {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return snapshot, true, ctxErr
		}
		// For an in-flight native Tool, a failed result does not authorize
		// automatic retry after a new epoch. Keep dispatch intent durable.
		if runner.runExecution != nil {
			return snapshot, true, errors.Join(ErrToolOutcomeUnknown, err)
		}
		return runner.handlePendingToolExecutionError(ctx, snapshot, execution.state, err)
	}
	if err = tools.ValidateExecutionResult(result); err != nil {
		if runner.runExecution != nil {
			// The external handler may already have committed. Never
			// terminalize/clear its durable dispatch marker on invalid
			// receipts from the external system.
			return snapshot, true, errors.Join(ErrToolOutcomeUnknown, ErrToolReceiptMismatch, err)
		}
		failed, failErr := runner.fail(ctx, snapshot, execution.state, "agent.tool_failed", errors.Join(ErrToolFailure, err))
		return failed, false, failErr
	}
	if runner.runExecution != nil && result.Receipt.ExecutionID != execution.call.ID {
		return snapshot, true, errors.Join(ErrToolOutcomeUnknown, ErrToolReceiptMismatch)
	}
	persisted, yielded, persistErr := runner.persistPendingToolResult(ctx, snapshot, execution, tools.CloneExecutionResult(result))
	if persistErr != nil {
		return persisted, yielded, persistErr
	}
	if late {
		// A tardy known receipt is persisted, but cannot be called a normal
		// on-time success. Caller must explicitly Resume after this warning.
		return persisted, true, ErrToolLateResult
	}
	return persisted, yielded, nil
}

type pendingToolExecution struct {
	state      runState
	call       tools.Call
	definition tools.Definition
}

func (runner *Runner) preparePendingToolExecution(snapshot kernel.Snapshot) (pendingToolExecution, string, error) {
	state, err := decodeState(snapshot.State)
	call, ok := nextPendingCall(state)
	if err != nil || !ok {
		return pendingToolExecution{state: state}, "agent.state_invalid", ErrInvalidRequest
	}
	if runner.catalog == nil || runner.executor == nil {
		return pendingToolExecution{state: state}, "agent.tool_unavailable", ErrToolFailure
	}
	definition, ok := runner.catalog.Resolve(call.ToolKey)
	if call.ToolKey == ToolDiscoveryKey && state.Discovery != nil {
		definition, ok = tools.CloneDefinition(discoveryToolDefinition), true
	}
	if !ok {
		if state.Discovery != nil {
			return pendingToolExecution{state: state}, "agent.discovery_invalid", ErrToolDiscoveryDenied
		}
		return pendingToolExecution{state: state}, "agent.tool_invalid", tools.ErrToolNotFound
	}
	if state.Discovery != nil && call.ToolKey != ToolDiscoveryKey {
		if state.Discovery.RunID != snapshot.Run.ID {
			return pendingToolExecution{state: state}, "agent.discovery_invalid", ErrToolDiscoveryInvalid
		}
		if err := validateLoadedDiscoveryExecution(state.Discovery, call.ToolKey, definition); err != nil {
			return pendingToolExecution{state: state}, "agent.discovery_invalid", err
		}
	}
	if err := tools.ValidateCall(definition, call); err != nil {
		return pendingToolExecution{state: state, call: call, definition: definition}, "agent.tool_invalid", err
	}
	if state.Budget.Usage.ToolCalls >= state.Budget.Limits.MaxToolCalls {
		return pendingToolExecution{state: state}, "agent.tool_limit", ErrCallLimit
	}
	return pendingToolExecution{state: state, call: call, definition: definition}, "", nil
}

func (runner *Runner) invokePendingTool(
	ctx context.Context,
	snapshot kernel.Snapshot,
	call tools.Call,
	definition tools.Definition,
) (tools.ExecutionResult, error) {
	startedAt := runner.clock.Now().UTC()
	runner.recordTelemetry(ctx, observability.Event{
		Scope: observability.ScopeToolInvocation, Phase: observability.PhaseStarted,
		RunID: snapshot.Run.ID, RunKind: RunKind, Revision: snapshot.Run.Revision,
		Status: string(snapshot.Run.Status), OperationID: call.ID, Operation: definition.Key,
		ObservedAt: startedAt,
	})
	runner.publishValue(ctx, snapshot.Run.ID, plugin.Event{
		Type: EventToolStarted, Revision: snapshot.Run.Revision, Status: string(snapshot.Run.Status),
	}, call)
	executionRequest := tools.ExecutionRequest{RunID: snapshot.Run.ID, Call: tools.CloneCall(call)}
	invocation := plugin.ToolInvocation{
		Run: snapshot.Run, Definition: tools.CloneDefinition(definition), Request: executionRequest,
	}
	result, attempt, err := runner.executeToolWithPolicy(ctx, call.ID, snapshot.Run.DeadlineAt, func(callCtx context.Context) (tools.ExecutionResult, error) {
		return runner.toolChain.Invoke(callCtx, invocation, func(nextCtx context.Context) (tools.ExecutionResult, error) {
			// Recheck after middleware/limiter delays, immediately before the
			// executor call. A Store gate cannot itself make external effects
			// physically exactly-once; the receipt is fenced separately.
			if err := runner.runtime.RequireExecution(nextCtx, snapshot.Run.ID); err != nil {
				return tools.ExecutionResult{}, err
			}
			return runner.executor.Execute(nextCtx, executionRequest)
		})
	})
	endedAt := runner.clock.Now().UTC()
	phase := observability.PhaseCompleted
	errorCode := ""
	if err != nil {
		phase = observability.PhaseFailed
		errorCode = "tool_error"
		switch {
		case ctx.Err() != nil:
			phase = observability.PhaseCancelled
			errorCode = "cancelled"
		case errors.Is(err, ErrToolTimeout):
			errorCode = "timeout"
		case errors.Is(err, ErrToolPanic):
			errorCode = "panic"
		case errors.Is(err, kernel.ErrDeadline):
			errorCode = "deadline"
		}
	}
	runner.recordTelemetry(ctx, observability.Event{
		Scope: observability.ScopeToolInvocation, Phase: phase,
		RunID: snapshot.Run.ID, RunKind: RunKind, Revision: snapshot.Run.Revision,
		Status: string(snapshot.Run.Status), OperationID: call.ID, Operation: definition.Key,
		Attempt: attempt, ErrorCode: errorCode, ObservedAt: endedAt, Duration: endedAt.Sub(startedAt),
	})
	return result, err
}

func (runner *Runner) handlePendingToolExecutionError(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	err error,
) (kernel.Snapshot, bool, error) {
	if code, message, recoverable := tools.RecoverableCallErrorInfo(err); recoverable {
		corrected, correctionErr := runner.recordRecoverableToolError(
			ctx, snapshot, state, code, message, tools.RecoverableCallErrorBlockedToolKeys(err), tools.RecoverableCallErrorAllowsCompletion(err),
		)
		return corrected, false, correctionErr
	}
	if errors.Is(err, runtimebudget.ErrExhausted) {
		failed, failErr := runner.fail(ctx, snapshot, state, sharedBudgetErrorCode(err), err)
		return failed, false, failErr
	}
	if errors.Is(err, kernel.ErrDeadline) {
		failed, failErr := runner.fail(ctx, snapshot, state, "agent.deadline_exceeded", errors.Join(kernel.ErrDeadline, err))
		return failed, false, failErr
	}
	failed, failErr := runner.fail(ctx, snapshot, state, "agent.tool_failed", errors.Join(ErrToolFailure, err))
	return failed, false, failErr
}

func (runner *Runner) persistPendingToolResult(
	ctx context.Context, snapshot kernel.Snapshot, execution pendingToolExecution,
	result tools.ExecutionResult) (kernel.Snapshot, bool, error) {
	if strings.TrimSpace(result.Receipt.Disposition) == tools.ReceiptDispositionPending {
		// A Pending result is not a terminal external receipt. A native
		// executor MUST preserve the durable dispatch marker: no later
		// Resume may call the non-idempotent Handler again without a trusted
		// provider lookup. Legacy unleased behavior remains unchanged.
		if runner.runExecution == nil {
			execution.state.ToolDispatchID = ""
		}
		return runner.persistPendingToolYield(ctx, snapshot, execution.state, result)
	}
	// A terminal receipt and marker removal share the SAME revision CAS.
	execution.state.ToolDispatchID = ""
	return runner.persistCompletedToolCall(ctx, snapshot, execution, result)
}

func (runner *Runner) persistPendingToolYield(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	result tools.ExecutionResult,
) (kernel.Snapshot, bool, error) {
	if result.Pending != nil && result.Pending.Kind != tools.PendingScheduled {
		return runner.persistToolWait(ctx, snapshot, state, result.Pending)
	}
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, false, err
	}
	next, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{
			Type: EventToolPending, Message: tools.ReceiptDispositionPending, Wakeup: true,
			WakeupAt: agentWakeupAt(runner.clock.Now()),
		}},
	})
	if err == nil {
		runner.publishToolEvent(ctx, next, EventToolPending, result.Receipt)
	}
	return next, true, err
}

func (runner *Runner) persistCompletedToolCall(
	ctx context.Context,
	snapshot kernel.Snapshot,
	execution pendingToolExecution,
	result tools.ExecutionResult,
) (kernel.Snapshot, bool, error) {
	state := execution.state
	state.ToolWait = nil
	state.Messages = append(state.Messages, model.Message{
		Role: model.RoleTool, Content: string(result.Content), ToolCallID: execution.call.ID,
	})
	state.PendingCalls = remainingPendingCalls(state)
	state.PendingApproval = nil
	snapshot.Checkpoint = nil // Consumed approval cannot authorize a later call.
	state.Budget.Usage.ToolCalls++
	if execution.definition.Terminal {
		if len(state.PendingCalls) != 0 {
			failed, failErr := runner.fail(ctx, snapshot, state, "agent.state_invalid", ErrInvalidRequest)
			return failed, false, failErr
		}
		completed, completeErr := runner.completeWithToolResult(ctx, snapshot, state, result)
		return completed, false, completeErr
	}
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, false, err
	}
	next, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{
			Type: "tool.completed", Message: result.Receipt.Disposition, Wakeup: true,
			WakeupAt: agentWakeupAt(runner.clock.Now()),
		}},
	})
	if err == nil {
		runner.publishToolEvent(ctx, next, EventToolCompleted, toolFeedCompletion{Receipt: result.Receipt, CallID: execution.call.ID, ToolKey: execution.call.ToolKey})
	}
	return next, false, err
}

func (runner *Runner) recordRecoverableToolError(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	code string,
	message string,
	blockedToolKeys []string,
	allowCompletion bool,
) (kernel.Snapshot, error) {
	call, ok := nextPendingCall(state)
	if !ok {
		return runner.fail(ctx, snapshot, state, "agent.state_invalid", ErrInvalidRequest)
	}
	content, err := json.Marshal(struct {
		OK        bool `json:"ok"`
		Retryable bool `json:"retryable"`
		Error     struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{
		OK:        false,
		Retryable: !allowCompletion,
		Error: struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}{Code: strings.TrimSpace(code), Message: strings.TrimSpace(message)},
	})
	if err != nil {
		return runner.fail(ctx, snapshot, state, "agent.tool_error_invalid", err)
	}
	callID := call.ID
	state.Messages = append(state.Messages, model.Message{
		Role: model.RoleTool, Content: string(content), ToolCallID: callID,
	})
	state.PendingCalls = remainingPendingCalls(state)
	state.PendingApproval = nil
	snapshot.Checkpoint = nil
	state.Budget.Usage.ToolCalls++
	state.BlockedToolKeys = normalizedToolKeys(append(state.BlockedToolKeys, blockedToolKeys...))
	// A third consecutive recoverable failure (even with different Tool
	// arguments) has no productive Tool correction left. Other granted Tools
	// remain available, but the model must be allowed to report the evidence
	// gap instead of being forced into another Tool invocation.
	state.RequireToolCall = !allowCompletion && len(unproductiveToolKeys(state.Messages)) == 0
	if allowCompletion {
		state.Messages = withSystemGuidance(state.Messages,
			"The unavailable capability cannot be recovered by repeating the same Tool call. A truthful final answer reporting missing sources is allowed now; do not fabricate evidence or citations.")
	}
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	next, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{
			Type: "tool.correction_requested", Message: strings.TrimSpace(code), Wakeup: true,
			WakeupAt: agentWakeupAt(runner.clock.Now()),
		}},
	})
	if err == nil {
		runner.publishValue(ctx, next.Run.ID, plugin.Event{
			Type: EventToolCompleted, Message: strings.TrimSpace(code),
			Revision: next.Run.Revision, Status: string(next.Run.Status),
		}, call)
	}
	return next, err
}

func (runner *Runner) resumeApproved(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	checkpoint *kernel.Checkpoint,
) (kernel.Snapshot, error) {
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	proof := featurecontrol.Seal(snapshot.Run.ID, snapshot.Run.Revision,
		string(kernel.ControlApproval), snapshot.Checkpoint.ID, encoded, checkpoint)
	running, revoked, err := runner.runtime.ApplyControl(ctx, snapshot.Run.ID, snapshot.Run.Revision,
		kernel.ControlGuard{Kind: kernel.ControlApproval, CheckpointID: snapshot.Checkpoint.ID}, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: encoded, Checkpoint: checkpoint,
			Events: []kernel.EventDraft{{Type: "interaction.resolved", Message: "Tool call approved", Wakeup: true}},
		}, proof)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	// A fenced control revoked the former executor. Never dispatch an
	// external Model/Tool from this unbound Runner, regardless of whether
	// legacy DeferResumption was enabled; a new owner must be acquired.
	if runner.deferResume || revoked {
		return running, nil
	}
	return runner.resumeRunning(ctx, running, state)
}

func (runner *Runner) resumeRejected(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	response plugin.ApprovalResponse,
	resolved *kernel.Checkpoint,
) (kernel.Snapshot, error) {
	call, ok := nextPendingCall(state)
	if !ok {
		return runner.fail(ctx, snapshot, state, "agent.state_invalid", ErrInvalidRequest)
	}
	state.Messages = append(state.Messages, model.Message{
		Role: model.RoleTool, Content: rejectedToolContent(response.Comment), ToolCallID: call.ID,
	})
	state.PendingCalls = remainingPendingCalls(state)
	state.PendingApproval = nil
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	proof := featurecontrol.Seal(snapshot.Run.ID, snapshot.Run.Revision,
		string(kernel.ControlApproval), snapshot.Checkpoint.ID, encoded, resolved)
	running, revoked, err := runner.runtime.ApplyControl(ctx, snapshot.Run.ID, snapshot.Run.Revision,
		kernel.ControlGuard{Kind: kernel.ControlApproval, CheckpointID: snapshot.Checkpoint.ID}, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: encoded, Checkpoint: resolved,
			Events: []kernel.EventDraft{{Type: "tool.rejected", Message: "Tool call rejected", Wakeup: true}},
		}, proof)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	// A fenced control revoked the former executor. Never dispatch an
	// external Model/Tool from this unbound Runner, regardless of whether
	// legacy DeferResumption was enabled; a new owner must be acquired.
	if runner.deferResume || revoked {
		return running, nil
	}
	return runner.drive(ctx, running)
}

func (runner *Runner) resumeRunning(ctx context.Context, snapshot kernel.Snapshot, _ runState) (kernel.Snapshot, error) {
	// An approval is consumed only by its own pending Tool call. The normal
	// drive validates the decision and binding before every Tool execution.
	return runner.drive(ctx, snapshot)
}

func (runner *Runner) complete(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	response model.Response,
) (kernel.Snapshot, error) {
	result, err := terminalResult(response, state.OutputContract)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	state.Budget.Usage.OutputBytes = len(result.Content)
	if dimension := runtimebudget.Exceeded(state.Budget.Limits, state.Budget.Usage); dimension != "" {
		return runner.fail(
			ctx, snapshot, state, "agent."+string(dimension)+"_budget",
			fmt.Errorf("%w: %s", ErrBudgetExceeded, dimension),
		)
	}
	state.Messages = append(state.Messages, model.Message{Role: model.RoleAssistant, Content: response.Content})
	encodedState, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	completed, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: encodedState, Checkpoint: snapshot.Checkpoint,
		Result: result,
		Events: []kernel.EventDraft{{Type: "agent.completed", Message: "Direct Agent loop completed"}},
	})
	if err == nil {
		runner.publishRunEvent(ctx, completed, EventRunCompleted, true)
	}
	return completed, err
}

func terminalResult(response model.Response, contract *model.OutputContract) (*kernel.Result, error) {
	if contract != nil {
		if err := validateStructuredDocument(contract, []byte(response.Content)); err != nil {
			return nil, err
		}
		if response.Refused || len(response.HostedToolCalls) > 0 || len(response.Artifacts) > 0 || len(response.Citations) > 0 {
			return nil, ErrOutputSchema
		}
		return &kernel.Result{ContentType: "application/json", Content: json.RawMessage(response.Content)}, nil
	}
	if len(response.HostedToolCalls) == 0 && len(response.Artifacts) == 0 && len(response.Citations) == 0 {
		encodedContent, err := json.Marshal(response.Content)
		if err != nil {
			return nil, errors.Join(ErrInvalidModelResponse, err)
		}
		return &kernel.Result{ContentType: "text", Content: encodedContent}, nil
	}
	value := Result{
		Content:         response.Content,
		HostedToolCalls: model.CloneHostedToolCalls(response.HostedToolCalls),
		Artifacts:       model.CloneArtifactRefs(response.Artifacts),
		Citations:       append([]string(nil), response.Citations...),
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, errors.Join(ErrInvalidModelResponse, err)
	}
	return &kernel.Result{ContentType: "agent_result", Content: encoded}, nil
}

func (runner *Runner) fail(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	code string,
	cause error,
) (kernel.Snapshot, error) {
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, errors.Join(cause, err)
	}
	failed, transitionErr := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusFailed, State: encoded, Checkpoint: snapshot.Checkpoint,
		ErrorCode: code, ErrorDetail: cause.Error(),
		Events: []kernel.EventDraft{{Type: "agent.failed", Message: code}},
	})
	if transitionErr == nil {
		runner.publishRunEvent(ctx, failed, EventRunFailed, true)
	}
	return failed, errors.Join(cause, transitionErr)
}

func (runner *Runner) publishRunEvent(ctx context.Context, snapshot kernel.Snapshot, eventType string, terminal bool) {
	runner.publish(ctx, snapshot.Run.ID, plugin.Event{
		Type: eventType, Message: snapshot.Run.ErrorDetail, Revision: snapshot.Run.Revision,
		Status: string(snapshot.Run.Status), Terminal: terminal,
	})
	runner.recordRunTelemetry(ctx, snapshot, eventType)
}

// Keep the established executor receipt fields and attach frozen call
// identity for correlating public started/completed phases. HTTP projects
// this metadata through a separate allowlist before it leaves the server.
type toolFeedCompletion struct {
	tools.Receipt
	CallID  string `json:"callID,omitempty"`
	ToolKey string `json:"toolKey,omitempty"`
}

func publicToolCompletionReceipt(state runState, receipt tools.Receipt) toolFeedCompletion {
	value := toolFeedCompletion{Receipt: receipt}
	for i := len(state.Messages) - 1; i >= 0; i-- {
		message := state.Messages[i]
		if message.Role != model.RoleTool {
			continue
		}
		value.CallID = message.ToolCallID
		value.ToolKey = toolCallKeysByID(state.Messages)[value.CallID]
		break
	}
	return value
}

func (runner *Runner) publishToolEvent(
	ctx context.Context,
	snapshot kernel.Snapshot,
	eventType string,
	value interface{},
) {
	runner.publishValue(ctx, snapshot.Run.ID, plugin.Event{
		Type: eventType, Revision: snapshot.Run.Revision, Status: string(snapshot.Run.Status),
	}, value)
}

func (runner *Runner) publishValue(ctx context.Context, runID string, event plugin.Event, value interface{}) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	event.Data = data
	runner.publish(ctx, runID, event)
}

func (runner *Runner) publish(ctx context.Context, runID string, event plugin.Event) {
	if runner == nil || runner.observers == nil {
		return
	}
	event.RunID = strings.TrimSpace(runID)
	event.RunKind = RunKind
	publishContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	runner.observers.Observe(publishContext, event)
}

func (runner *Runner) recordRunTelemetry(ctx context.Context, snapshot kernel.Snapshot, eventType string) {
	phase := observability.Phase("")
	switch eventType {
	case EventRunStarted:
		phase = observability.PhaseStarted
	case EventRunCompleted:
		phase = observability.PhaseCompleted
	case EventRunFailed:
		phase = observability.PhaseFailed
	default:
		return
	}
	usage := runtimebudget.Usage{}
	if view, err := ViewState(snapshot); err == nil {
		usage = view.Budget.Usage
	}
	duration := time.Duration(0)
	if phase != observability.PhaseStarted && !snapshot.Run.CreatedAt.IsZero() && !snapshot.Run.UpdatedAt.Before(snapshot.Run.CreatedAt) {
		duration = snapshot.Run.UpdatedAt.Sub(snapshot.Run.CreatedAt)
	}
	runner.recordTelemetry(ctx, observability.Event{
		Scope: observability.ScopeRun, Phase: phase, RunID: snapshot.Run.ID, RunKind: RunKind,
		Revision: snapshot.Run.Revision, Status: string(snapshot.Run.Status), ErrorCode: snapshot.Run.ErrorCode,
		ObservedAt: runner.clock.Now().UTC(), Duration: duration, Usage: usage,
	})
}

func (runner *Runner) recordTelemetry(ctx context.Context, event observability.Event) {
	if runner == nil || runner.telemetry == nil {
		return
	}
	if event.ObservedAt.IsZero() {
		event.ObservedAt = runner.clock.Now().UTC()
	}
	recordContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	runner.telemetry.Record(recordContext, event)
}

func (runner *Runner) loadState(ctx context.Context, runID string) (kernel.Snapshot, runState, error) {
	snapshot, err := runner.runtime.Load(ctx, runID)
	if err != nil {
		return kernel.Snapshot{}, runState{}, err
	}
	state, err := decodeState(snapshot.State)
	return snapshot, state, err
}

func selectedDefinition(definitions []tools.Definition, key string) (tools.Definition, bool) {
	for _, definition := range definitions {
		if definition.Key == key {
			return definition, true
		}
	}
	return tools.Definition{}, false
}

func validModelResponse(response model.Response) bool {
	return validModelUsage(response.Usage) &&
		(!response.Continue || len(response.ToolCalls) == 0) &&
		(response.Continue || len(response.ToolCalls) > 0 || response.Content != "" || len(response.HostedToolCalls) > 0 || len(response.Artifacts) > 0)
}

func validModelUsage(usage *model.Usage) bool {
	if usage == nil {
		return true
	}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.CacheReadTokens < 0 ||
		usage.CacheWriteTokens < 0 || usage.CacheWrite5mTokens < 0 || usage.CacheWrite1hTokens < 0 ||
		usage.ReasoningTokens < 0 {
		return false
	}
	return usage.InputTokens <= math.MaxInt64-usage.OutputTokens
}

func nextPendingCall(state runState) (tools.Call, bool) {
	if len(state.PendingCalls) == 0 {
		return tools.Call{}, false
	}
	return state.PendingCalls[0], true
}

func remainingPendingCalls(state runState) []tools.Call {
	if len(state.PendingCalls) <= 1 {
		return nil
	}
	return cloneToolCalls(state.PendingCalls[1:])
}

func cloneToolCalls(values []tools.Call) []tools.Call {
	result := make([]tools.Call, len(values))
	for index, call := range values {
		result[index] = call
		result[index].Arguments = append(json.RawMessage(nil), call.Arguments...)
	}
	return result
}

func cloneRawJSON(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func encodeState(state runState) (json.RawMessage, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, errors.Join(ErrInvalidRequest, err)
	}
	return encoded, nil
}

func decodeState(encoded json.RawMessage) (runState, error) {
	var state runState
	if err := json.Unmarshal(encoded, &state); err != nil {
		return runState{}, errors.Join(ErrInvalidRequest, err)
	}
	if state.Budget.Limits.MaxLLMCalls <= 0 || state.Budget.Limits.MaxToolCalls <= 0 ||
		!validAgentLimits(state.Budget.Limits) || !validAgentUsage(state.Budget.Usage) ||
		!validCallAllowance(state.CallAllowance, state.Budget.Limits) ||
		!validAutoAllowanceState(state.AutoAllowance, state.CallAllowance, state.Budget.Limits) ||
		!toolKeysContainAll(state.ToolKeys, state.RequiredToolKeys) ||
		!validHostedGrantSnapshot(state.HostedToolGrants, state.ToolKeys) ||
		!validDiscoveryState(state) ||
		model.ValidateOutputContract(state.OutputContract) != nil ||
		!validToolWaitState(state) ||
		!validDeferredChildren(state) ||
		!validModelInvocations(state.ModelInvocations) {
		return runState{}, ErrInvalidRequest
	}
	return state, nil
}

func resolveRunLimits(defaults Limits, requested Limits) (Limits, error) {
	resolved, err := runtimebudget.TightenLimits(defaults, requested)
	if err != nil {
		return Limits{}, errors.Join(ErrInvalidRequest, err)
	}
	if resolved.MaxLLMCalls <= 0 || resolved.MaxToolCalls <= 0 || !validAgentLimits(resolved) {
		return Limits{}, ErrInvalidRequest
	}
	return resolved, nil
}

func sharedBudgetErrorCode(err error) string {
	if dimension, ok := runtimebudget.DeniedDimension(err); ok {
		return "agent.shared_" + string(dimension) + "_budget"
	}
	return "agent.shared_budget_exhausted"
}

func validAgentLimits(value Limits) bool {
	return runtimebudget.ValidLimits(value) && value.MaxStateBytes == 0 &&
		value.MaxChildRuns == 0 && value.MaxCostUnits == 0
}

func validAgentUsage(value runtimebudget.Usage) bool {
	return runtimebudget.ValidUsage(value) && value.StateBytes == 0 &&
		value.ChildRuns == 0 && value.CostUnits == 0
}

func chargeConsumedModelUsage(state *runState, invocation ModelInvocation) (runtimebudget.Dimension, error) {
	if state == nil {
		return "", ErrInvalidRequest
	}
	if runtimebudget.HasTokenLimits(state.Budget.Limits) && invocation.Usage == nil {
		usage, err := runtimebudget.ChargeModelCall(state.Budget.Usage, nil)
		if err != nil {
			return "", errors.Join(ErrInvalidModelResponse, err)
		}
		state.Budget.Usage = usage
		return "", ErrUsageUnavailable
	}
	var observed *runtimebudget.TokenUsage
	if invocation.Usage != nil {
		observed = &runtimebudget.TokenUsage{
			InputTokens: invocation.Usage.InputTokens, OutputTokens: invocation.Usage.OutputTokens,
			CacheReadTokens:  invocation.Usage.CacheReadTokens,
			CacheWriteTokens: invocation.Usage.CacheWriteTokens,
			ReasoningTokens:  invocation.Usage.ReasoningTokens,
		}
	}
	usage, err := runtimebudget.ChargeModelCall(state.Budget.Usage, observed)
	if err != nil {
		return "", errors.Join(ErrInvalidModelResponse, err)
	}
	state.Budget.Usage = usage
	return runtimebudget.Exceeded(state.Budget.Limits, usage), nil
}

func normalizedToolKeys(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func rejectedToolContent(comment string) string {
	encoded, err := json.Marshal(map[string]string{"status": "rejected", "comment": strings.TrimSpace(comment)})
	if err != nil {
		return `{"status":"rejected"}`
	}
	return string(encoded)
}
