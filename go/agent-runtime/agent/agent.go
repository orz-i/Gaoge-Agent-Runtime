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
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/observability"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const (
	RunKind          kernel.RunKind    = "agent"
	CapabilityRunner kernel.Capability = "agent.runner"
)

var (
	ErrInvalidRequest       = errors.New("invalid agent run request")
	ErrInvalidModelResponse = errors.New("invalid agent model response")
	ErrModelFailure         = errors.New("agent model failure")
	ErrModelInvocationBusy  = errors.New("agent model invocation execution is leased")
	ErrToolFailure          = errors.New("agent tool failure")
	ErrCallLimit            = errors.New("agent run call limit exceeded")
	ErrBudgetExceeded       = errors.New("agent run budget exceeded")
	ErrUsageUnavailable     = errors.New("agent model usage unavailable for configured token budget")
	ErrApprovalRequired     = errors.New("agent run approval is required")
	ErrRunTerminal          = errors.New("agent run is terminal")
)

func (runner *Runner) completeWithToolResult(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	result tools.ExecutionResult,
) (kernel.Snapshot, error) {
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
		runner.publishToolEvent(ctx, completed, EventToolCompleted, result.Receipt)
		runner.publishRunEvent(ctx, completed, EventRunCompleted, true)
	}
	return completed, err
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
	state.Messages = append(state.Messages, model.Message{
		Role: model.RoleAssistant, Content: strings.TrimSpace(response.Content),
	})
	state.Messages = withSystemGuidance(
		state.Messages,
		"Completion rejected by the Runtime: "+correction.Message+
			" The previous response was not delivered to the user. Repair only the terminal answer using the successful results already present in this transcript.",
	)
	state.BlockedToolKeys = normalizedToolKeys(append(state.BlockedToolKeys, correction.BlockedToolKeys...))
	state.RequireToolCall = false
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{Type: "agent.completion_corrected", Message: correction.Code}},
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

// Dependencies explicitly provide the direct Agent loop capabilities.
type Dependencies struct {
	Runtime            *kernel.Runtime
	Model              model.Client
	Clock              kernel.Clock
	Catalog            tools.Catalog
	Executor           tools.Executor
	Approvals          plugin.ApprovalHandler
	ApprovalPolicies   []plugin.ApprovalPolicy
	HostedTools        HostedToolCatalog
	Observers          []plugin.Observer
	Telemetry          []observability.Recorder
	RunMiddleware      []plugin.RunMiddleware
	ModelMiddleware    []plugin.ModelMiddleware
	ToolMiddleware     []plugin.ToolMiddleware
	CompletionPolicies []CompletionPolicy
	Limits             Limits
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
	Model            string
	ModelOptions     json.RawMessage
	ToolKeys         []string
	RequiredToolKeys []string
	Limits           Limits
}

// Runner owns only the direct Agent model and Tool loop.
type Runner struct {
	runtime            *kernel.Runtime
	model              model.Client
	clock              kernel.Clock
	catalog            tools.Catalog
	executor           tools.Executor
	approvals          plugin.ApprovalHandler
	approvalPolicies   *plugin.ApprovalPolicySet
	hostedTools        HostedToolCatalog
	observers          *plugin.ObserverSet
	telemetry          *observability.Set
	runChain           *plugin.RunChain
	modelChain         *plugin.ModelChain
	toolChain          *plugin.ToolChain
	completionPolicies []CompletionPolicy
	limits             Limits
	deferResume        bool
}

type runState struct {
	Messages         []model.Message        `json:"messages"`
	Model            string                 `json:"model,omitempty"`
	ModelOptions     json.RawMessage        `json:"modelOptions,omitempty"`
	ToolKeys         []string               `json:"toolKeys"`
	RequiredToolKeys []string               `json:"requiredToolKeys,omitempty"`
	BlockedToolKeys  []string               `json:"blockedToolKeys,omitempty"`
	RequireToolCall  bool                   `json:"requireToolCall,omitempty"`
	Budget           runtimebudget.Snapshot `json:"budget"`
	PendingCalls     []tools.Call           `json:"pendingCalls,omitempty"`
	ModelInvocations []ModelInvocation      `json:"modelInvocations,omitempty"`
}

// View is an isolated public projection of one persisted Agent Run. It exposes
// the durable transcript and bounded usage state without leaking the private
// execution representation or allowing callers to mutate Kernel state.
type View struct {
	Messages         []model.Message
	Model            string
	ModelOptions     json.RawMessage
	ToolKeys         []string
	RequiredToolKeys []string
	Budget           runtimebudget.Snapshot
	ModelInvocations []ModelInvocation
}

// ViewState decodes an isolated public view from a Kernel Agent snapshot.
func ViewState(snapshot kernel.Snapshot) (View, error) {
	state, err := decodeState(snapshot.State)
	if err != nil {
		return View{}, err
	}
	return View{
		Messages:         model.CloneMessages(state.Messages),
		Model:            state.Model,
		ModelOptions:     cloneRawJSON(state.ModelOptions),
		ToolKeys:         append([]string(nil), state.ToolKeys...),
		RequiredToolKeys: append([]string(nil), state.RequiredToolKeys...),
		Budget:           state.Budget,
		ModelInvocations: cloneModelInvocations(state.ModelInvocations),
	}, nil
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
	return &Runner{
		runtime: dependencies.Runtime, model: dependencies.Model, clock: dependencies.Clock, catalog: dependencies.Catalog,
		executor: dependencies.Executor, approvals: dependencies.Approvals,
		hostedTools: dependencies.HostedTools, observers: observers, telemetry: telemetry, approvalPolicies: approvalPolicies,
		completionPolicies: compactCompletionPolicies(dependencies.CompletionPolicies),
		limits:             dependencies.Limits,
		runChain:           runChain, modelChain: modelChain, toolChain: toolChain,
		deferResume: dependencies.DeferResumption,
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
	if runner == nil || strings.TrimSpace(request.Goal) == "" {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	invocation := plugin.RunInvocation{
		Operation: plugin.RunStart, Kind: RunKind, RunID: strings.TrimSpace(request.ID),
		Actor: request.Actor, Thread: request.Thread, Goal: strings.TrimSpace(request.Goal),
	}
	return runner.runChain.Invoke(ctx, invocation, func(nextCtx context.Context) (kernel.Snapshot, error) {
		return runner.startRun(nextCtx, request)
	})
}

func (runner *Runner) startRun(ctx context.Context, request StartRequest) (kernel.Snapshot, error) {
	if _, _, err := runner.resolveSelectedTools(ctx, request.ToolKeys, request.Model); err != nil {
		return kernel.Snapshot{}, err
	}
	limits, err := resolveRunLimits(runner.limits, request.Limits)
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
		ToolKeys:         toolKeys,
		RequiredToolKeys: requiredToolKeys,
		Budget:           runtimebudget.Snapshot{Limits: limits},
	}
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	snapshot, err := runner.runtime.Create(ctx, kernel.CreateRequest{
		ID: request.ID, Kind: RunKind, Actor: request.Actor, Thread: request.Thread,
		RequestID: request.RequestID, Goal: request.Goal, State: encoded,
		Events: []kernel.EventDraft{{Type: "agent.started", Message: "Direct Agent loop started"}},
	})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	runner.publishRunEvent(ctx, snapshot, EventRunStarted, false)
	return runner.drive(ctx, snapshot)
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
	resolved, err := runner.approvals.ResolveToolApproval(snapshot.Checkpoint, response)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if response.Decision == plugin.ApprovalReject {
		return runner.resumeRejected(ctx, snapshot, state, resolved, response)
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
	invocation := runInvocation(plugin.RunResume, snapshot)
	return runner.runChain.Invoke(ctx, invocation, func(nextCtx context.Context) (kernel.Snapshot, error) {
		return runner.resumeRunning(nextCtx, snapshot, state)
	})
}

func (runner *Runner) drive(ctx context.Context, snapshot kernel.Snapshot) (kernel.Snapshot, error) {
	for snapshot.Run.Status == kernel.RunStatusRunning {
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
	if len(state.PendingCalls) > 0 {
		definitions, _, catalogErr := runner.resolveSelectedTools(ctx, state.ToolKeys, state.Model)
		if catalogErr != nil {
			failed, failErr := runner.fail(ctx, snapshot, state, "agent.tool_invalid", catalogErr)
			return failed, true, failErr
		}
		prepared, prepareErr := runner.preparePendingApproval(ctx, snapshot, state, definitions)
		if prepareErr != nil {
			if code, message, recoverable := tools.RecoverableCallErrorInfo(prepareErr); recoverable {
				corrected, correctionErr := runner.recordRecoverableToolError(
					ctx, snapshot, state, code, message, tools.RecoverableCallErrorBlockedToolKeys(prepareErr),
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
	if state.Budget.Usage.LLMCalls >= state.Budget.Limits.MaxLLMCalls {
		failed, failErr := runner.fail(ctx, snapshot, state, "agent.llm_limit", ErrCallLimit)
		return failed, true, failErr
	}
	snapshot, state, invocation, err := runner.ensureModelInvocation(ctx, snapshot, state)
	if err != nil {
		return snapshot, true, err
	}
	snapshot, state, invocation, err = runner.claimModelInvocationExecution(ctx, snapshot, state, invocation)
	if err != nil {
		return snapshot, true, err
	}
	snapshot, state, err = runner.executeModelInvocation(ctx, snapshot, state, invocation)
	if err != nil {
		if model.IsRetryableError(err) {
			released, releaseErr := runner.releaseModelInvocationExecution(ctx, snapshot, state, invocation)
			if releaseErr != nil {
				return released, true, errors.Join(err, releaseErr)
			}
			return released, true, err
		}
		if errors.Is(err, ErrModelFailure) || errors.Is(err, ErrInvalidModelResponse) {
			failed, failErr := runner.fail(ctx, snapshot, state, "agent.model_failed", err)
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
	if len(response.ToolCalls) == 0 {
		if missing := missingRequiredToolKeys(state); len(missing) != 0 {
			corrected, correctionErr := runner.correctRequiredToolCompletion(ctx, snapshot, state, response, missing)
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
		Events: []kernel.EventDraft{{Type: "agent.completion_corrected", Message: strings.Join(missing, ",")}},
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

func (runner *Runner) generateModel(ctx context.Context, request model.Request) (model.Response, error) {
	emit := func(event model.StreamEvent) error {
		runner.publishValue(ctx, request.RunID, plugin.Event{
			Type: EventModelDelta, Delta: event.Delta,
		}, event)
		return nil
	}
	return runner.modelChain.Invoke(ctx, request, emit, runner.generateProviderModel)
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
		return model.Response{}, err
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
		definition, hostedTool, err := runner.resolveSelectedTool(ctx, key, modelName)
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
		return nil, nil, tools.ErrToolNotFound
	}
	resolved, ok, err := runner.hostedTools.Resolve(ctx, key, strings.TrimSpace(modelName))
	if err != nil {
		return nil, nil, err
	}
	if !ok || strings.TrimSpace(resolved.Key) != key {
		return nil, nil, tools.ErrToolNotFound
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
		if !ok || !json.Valid(call.Arguments) || definition.Terminal && index != len(calls)-1 {
			return runner.fail(ctx, snapshot, state, "agent.tool_invalid", tools.ErrInvalidCall)
		}
		preparedCalls[index] = call
		preparedCalls[index].Arguments = append(json.RawMessage(nil), call.Arguments...)
	}
	state.Messages = append(state.Messages, model.Message{
		Role: model.RoleAssistant, Content: strings.TrimSpace(content),
		ToolCalls: cloneToolCalls(preparedCalls),
	})
	state.PendingCalls = cloneToolCalls(preparedCalls)
	state.RequireToolCall = false
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	next, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{Type: "tool.batch_requested", Message: "Tool call batch requested"}},
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
	if !ok {
		return runner.fail(ctx, snapshot, state, "agent.tool_invalid", tools.ErrInvalidCall)
	}
	if err := tools.ValidateCall(definition, call); err != nil {
		return snapshot, err
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
	result, err := runner.invokePendingTool(ctx, snapshot, execution.call, execution.definition)
	if err != nil {
		return runner.handlePendingToolExecutionError(ctx, snapshot, execution.state, err)
	}
	if err = tools.ValidateExecutionResult(result); err != nil {
		failed, failErr := runner.fail(ctx, snapshot, execution.state, "agent.tool_failed", errors.Join(ErrToolFailure, err))
		return failed, false, failErr
	}
	return runner.persistPendingToolResult(ctx, snapshot, execution, tools.CloneExecutionResult(result))
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
	if !ok {
		return pendingToolExecution{state: state}, "agent.tool_invalid", tools.ErrToolNotFound
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
	result, err := runner.toolChain.Invoke(ctx, invocation, func(nextCtx context.Context) (tools.ExecutionResult, error) {
		return runner.executor.Execute(nextCtx, executionRequest)
	})
	endedAt := runner.clock.Now().UTC()
	phase := observability.PhaseCompleted
	errorCode := ""
	if err != nil {
		phase = observability.PhaseFailed
		errorCode = "tool_error"
	}
	runner.recordTelemetry(ctx, observability.Event{
		Scope: observability.ScopeToolInvocation, Phase: phase,
		RunID: snapshot.Run.ID, RunKind: RunKind, Revision: snapshot.Run.Revision,
		Status: string(snapshot.Run.Status), OperationID: call.ID, Operation: definition.Key,
		ErrorCode: errorCode, ObservedAt: endedAt, Duration: endedAt.Sub(startedAt),
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
			ctx, snapshot, state, code, message, tools.RecoverableCallErrorBlockedToolKeys(err),
		)
		return corrected, false, correctionErr
	}
	failed, failErr := runner.fail(ctx, snapshot, state, "agent.tool_failed", errors.Join(ErrToolFailure, err))
	return failed, false, failErr
}

func (runner *Runner) persistPendingToolResult(
	ctx context.Context,
	snapshot kernel.Snapshot,
	execution pendingToolExecution,
	result tools.ExecutionResult,
) (kernel.Snapshot, bool, error) {
	if strings.TrimSpace(result.Receipt.Disposition) == tools.ReceiptDispositionPending {
		return runner.persistPendingToolYield(ctx, snapshot, execution.state, result)
	}
	return runner.persistCompletedToolCall(ctx, snapshot, execution, result)
}

func (runner *Runner) persistPendingToolYield(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	result tools.ExecutionResult,
) (kernel.Snapshot, bool, error) {
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, false, err
	}
	next, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{Type: EventToolPending, Message: tools.ReceiptDispositionPending}},
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
	state.Messages = append(state.Messages, model.Message{
		Role: model.RoleTool, Content: string(result.Content), ToolCallID: execution.call.ID,
	})
	state.PendingCalls = remainingPendingCalls(state)
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
		Events: []kernel.EventDraft{{Type: "tool.completed", Message: result.Receipt.Disposition}},
	})
	if err == nil {
		runner.publishToolEvent(ctx, next, EventToolCompleted, result.Receipt)
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
		Retryable: true,
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
	state.Budget.Usage.ToolCalls++
	state.BlockedToolKeys = normalizedToolKeys(append(state.BlockedToolKeys, blockedToolKeys...))
	state.RequireToolCall = true
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	next, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{Type: "tool.correction_requested", Message: strings.TrimSpace(code)}},
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
	running, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: checkpoint,
		Events: []kernel.EventDraft{{Type: "interaction.resolved", Message: "Tool call approved", Wakeup: true}},
	})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if runner.deferResume {
		return running, nil
	}
	return runner.resumeRunning(ctx, running, state)
}

func (runner *Runner) resumeRejected(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	checkpoint *kernel.Checkpoint,
	response plugin.ApprovalResponse,
) (kernel.Snapshot, error) {
	call, ok := nextPendingCall(state)
	if !ok {
		return runner.fail(ctx, snapshot, state, "agent.state_invalid", ErrInvalidRequest)
	}
	state.Messages = append(state.Messages, model.Message{
		Role: model.RoleTool, Content: rejectedToolContent(response.Comment), ToolCallID: call.ID,
	})
	state.PendingCalls = remainingPendingCalls(state)
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	running, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: checkpoint,
		Events: []kernel.EventDraft{{Type: "tool.rejected", Message: "Tool call rejected", Wakeup: true}},
	})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if runner.deferResume {
		return running, nil
	}
	return runner.drive(ctx, running)
}

func (runner *Runner) resumeRunning(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
) (kernel.Snapshot, error) {
	if len(state.PendingCalls) > 0 && snapshot.Checkpoint != nil &&
		snapshot.Checkpoint.Status == kernel.CheckpointResolved {
		executed, yielded, err := runner.executePending(ctx, snapshot)
		if err != nil || yielded {
			return executed, err
		}
		return runner.drive(ctx, executed)
	}
	return runner.drive(ctx, snapshot)
}

func (runner *Runner) complete(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	response model.Response,
) (kernel.Snapshot, error) {
	result, err := terminalResult(response)
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

func terminalResult(response model.Response) (*kernel.Result, error) {
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
		(len(response.ToolCalls) > 0 || response.Content != "" || len(response.HostedToolCalls) > 0 || len(response.Artifacts) > 0)
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
		!toolKeysContainAll(state.ToolKeys, state.RequiredToolKeys) ||
		!validModelInvocations(state.ModelInvocations) {
		return runState{}, ErrInvalidRequest
	}
	return state, nil
}

func resolveRunLimits(defaults Limits, requested Limits) (Limits, error) {
	resolved, err := runtimebudget.ResolveLimits(defaults, requested)
	if err != nil {
		return Limits{}, errors.Join(ErrInvalidRequest, err)
	}
	if resolved.MaxLLMCalls <= 0 || resolved.MaxToolCalls <= 0 || !validAgentLimits(resolved) {
		return Limits{}, ErrInvalidRequest
	}
	return resolved, nil
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
