package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runfeed"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

const CapabilityRunner kernel.Capability = "agent.runner"

var (
	ErrInvalidRequest       = errors.New("invalid agent run request")
	ErrInvalidModelResponse = errors.New("invalid agent model response")
	ErrModelFailure         = errors.New("agent model failure")
	ErrToolFailure          = errors.New("agent tool failure")
	ErrCallLimit            = errors.New("agent run call limit exceeded")
	ErrApprovalRequired     = errors.New("agent run approval is required")
)

// Role identifies one direct Agent loop message role.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is the provider-neutral transcript consumed by an Agent model.
type Message struct {
	Role       Role         `json:"role"`
	Content    string       `json:"content"`
	ToolCalls  []tools.Call `json:"toolCalls,omitempty"`
	ToolCallID string       `json:"toolCallID,omitempty"`
}

func (runner *Runner) completeWithToolResult(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	result tools.ExecutionResult,
) (kernel.Snapshot, error) {
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
		runner.publishToolEvent(ctx, completed, runfeed.EventToolCompleted, result.Receipt)
		runner.publishRunEvent(ctx, completed, runfeed.EventRunCompleted, true)
	}
	return completed, err
}

// ModelRequest is one direct Agent loop model call.
type ModelRequest struct {
	RunID       string
	Model       string
	Messages    []Message
	Tools       []tools.Definition
	HostedTools []HostedTool
}

// HostedTool is one provider-hosted Tool activation resolved by the host.
// Target is opaque host metadata; Agent Runtime does not interpret provider protocols or payloads.
type HostedTool struct {
	Key    string          `json:"key"`
	Target json.RawMessage `json:"target,omitempty"`
}

// HostedToolCall records one provider-executed Tool fact. It never enters the local Tool executor.
type HostedToolCall struct {
	ID      string          `json:"id,omitempty"`
	ToolKey string          `json:"toolKey"`
	Status  string          `json:"status,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	Output  json.RawMessage `json:"output,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

// ArtifactRef is a durable host-owned artifact reference. Binary payloads must not enter Runtime state or result.
type ArtifactRef struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	MediaType string          `json:"mediaType,omitempty"`
	Name      string          `json:"name,omitempty"`
	SizeBytes int64           `json:"sizeBytes,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}

// Result is the structured terminal Agent output when hosted Tool facts or artifacts are present.
type Result struct {
	Content         string           `json:"content,omitempty"`
	HostedToolCalls []HostedToolCall `json:"hostedToolCalls,omitempty"`
	Artifacts       []ArtifactRef    `json:"artifacts,omitempty"`
	Citations       []string         `json:"citations,omitempty"`
}

// ModelResponse returns final content, local Tool calls, provider-hosted Tool facts, and durable artifact refs.
type ModelResponse struct {
	Content         string
	ToolCalls       []tools.Call
	HostedToolCalls []HostedToolCall
	Artifacts       []ArtifactRef
	Citations       []string
}

// ModelReasoningDelta is one provider-neutral reasoning progress observation.
type ModelReasoningDelta struct {
	EventType string `json:"eventType,omitempty"`
	ItemID    string `json:"itemID,omitempty"`
	Status    string `json:"status,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Text      string `json:"text,omitempty"`
}

// ModelUsage is one cumulative provider-neutral token usage observation.
type ModelUsage struct {
	InputTokens        int64  `json:"inputTokens,omitempty"`
	OutputTokens       int64  `json:"outputTokens,omitempty"`
	CacheReadTokens    int64  `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens   int64  `json:"cacheWriteTokens,omitempty"`
	CacheWrite5mTokens int64  `json:"cacheWrite5mTokens,omitempty"`
	CacheWrite1hTokens int64  `json:"cacheWrite1hTokens,omitempty"`
	ReasoningTokens    int64  `json:"reasoningTokens,omitempty"`
	Speed              string `json:"speed,omitempty"`
	ServiceTier        string `json:"serviceTier,omitempty"`
	BillingRateClass   string `json:"billingRateClass,omitempty"`
}

// ModelStreamEvent is the provider-neutral live model event consumed by Agent Runtime.
type ModelStreamEvent struct {
	Delta          string               `json:"delta,omitempty"`
	Reasoning      *ModelReasoningDelta `json:"reasoning,omitempty"`
	Usage          *ModelUsage          `json:"usage,omitempty"`
	HostedToolCall *HostedToolCall      `json:"hostedToolCall,omitempty"`
	ResponseID     string               `json:"responseID,omitempty"`
}

// Model is the only model dependency of the Agent feature.
type Model interface {
	Generate(context.Context, ModelRequest) (ModelResponse, error)
}

// StreamingModel optionally exposes real provider stream events while preserving the final ModelResponse contract.
type StreamingModel interface {
	GenerateStream(context.Context, ModelRequest, func(ModelStreamEvent) error) (ModelResponse, error)
}

// HostedToolCatalog resolves provider-hosted Tool activations by canonical Tool Key.
type HostedToolCatalog interface {
	Resolve(context.Context, string, string) (HostedTool, bool, error)
}

// Limits are hard ceilings for one direct Agent loop.
type Limits struct {
	MaxLLMCalls  int `json:"maxLLMCalls"`
	MaxToolCalls int `json:"maxToolCalls"`
}

// Dependencies explicitly provide the direct Agent loop capabilities.
type Dependencies struct {
	Runtime     *kernel.Runtime
	Model       Model
	Catalog     tools.Catalog
	Executor    tools.Executor
	Approvals   *interaction.Approvals
	HostedTools HostedToolCatalog
	Feed        runfeed.Publisher
	Limits      Limits
}

// StartRequest starts one direct Agent Run.
type StartRequest struct {
	ID        string
	Actor     kernel.ActorRef
	Thread    kernel.ThreadRef
	RequestID string
	Goal      string
	Model     string
	ToolKeys  []string
	Limits    Limits
}

// Runner owns only the direct Agent model and Tool loop.
type Runner struct {
	runtime     *kernel.Runtime
	model       Model
	catalog     tools.Catalog
	executor    tools.Executor
	approvals   *interaction.Approvals
	hostedTools HostedToolCatalog
	feed        runfeed.Publisher
	limits      Limits
}

type runState struct {
	Messages     []Message    `json:"messages"`
	Model        string       `json:"model,omitempty"`
	ToolKeys     []string     `json:"toolKeys"`
	Limits       Limits       `json:"limits"`
	PendingCalls []tools.Call `json:"pendingCalls,omitempty"`
	LLMCalls     int          `json:"llmCalls"`
	ToolCalls    int          `json:"toolCalls"`
}

// View is an isolated public projection of one persisted Agent Run. It exposes
// the durable transcript and bounded usage state without leaking the private
// execution representation or allowing callers to mutate Kernel state.
type View struct {
	Messages  []Message
	Model     string
	ToolKeys  []string
	Limits    Limits
	LLMCalls  int
	ToolCalls int
}

// ViewState decodes an isolated public view from a Kernel Agent snapshot.
func ViewState(snapshot kernel.Snapshot) (View, error) {
	state, err := decodeState(snapshot.State)
	if err != nil {
		return View{}, err
	}
	return View{
		Messages:  cloneMessages(state.Messages),
		Model:     state.Model,
		ToolKeys:  append([]string(nil), state.ToolKeys...),
		Limits:    state.Limits,
		LLMCalls:  state.LLMCalls,
		ToolCalls: state.ToolCalls,
	}, nil
}

// NewRunner constructs a direct Agent feature without planning or automatic routing.
func NewRunner(dependencies Dependencies) (*Runner, error) {
	if dependencies.Runtime == nil || dependencies.Model == nil || dependencies.Catalog == nil ||
		dependencies.Executor == nil || dependencies.Approvals == nil {
		return nil, ErrInvalidRequest
	}
	if dependencies.Limits.MaxLLMCalls <= 0 {
		dependencies.Limits.MaxLLMCalls = 8
	}
	if dependencies.Limits.MaxToolCalls <= 0 {
		dependencies.Limits.MaxToolCalls = 16
	}
	return &Runner{
		runtime: dependencies.Runtime, model: dependencies.Model, catalog: dependencies.Catalog,
		executor: dependencies.Executor, approvals: dependencies.Approvals,
		hostedTools: dependencies.HostedTools, feed: dependencies.Feed, limits: dependencies.Limits,
	}, nil
}

// Descriptor declares the explicit Agent capability graph.
func (runner *Runner) Descriptor() kernel.FeatureDescriptor {
	requires := []kernel.Capability{
		kernel.CapabilityRuntime, tools.CapabilityCatalog, tools.CapabilityExecutor, interaction.CapabilityApproval,
	}
	if runner != nil && runner.feed != nil {
		requires = append(requires, runfeed.CapabilityFeed)
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
	if _, _, err := runner.resolveSelectedTools(ctx, request.ToolKeys, request.Model); err != nil {
		return kernel.Snapshot{}, err
	}
	limits, err := resolveRunLimits(runner.limits, request.Limits)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	state := runState{
		Messages: []Message{{Role: RoleUser, Content: strings.TrimSpace(request.Goal)}},
		Model:    strings.TrimSpace(request.Model),
		ToolKeys: normalizedToolKeys(request.ToolKeys),
		Limits:   limits,
	}
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	snapshot, err := runner.runtime.Create(ctx, kernel.CreateRequest{
		ID: request.ID, Kind: kernel.RunKindAgent, Actor: request.Actor, Thread: request.Thread,
		RequestID: request.RequestID, Goal: request.Goal, State: encoded,
		Events: []kernel.EventDraft{{Type: "agent.started", Message: "Direct Agent loop started"}},
	})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	runner.publishRunEvent(ctx, snapshot, runfeed.EventRunStarted, false)
	return runner.drive(ctx, snapshot)
}

// ResolveApproval resumes one waiting Agent Run with an explicit decision.
func (runner *Runner) ResolveApproval(
	ctx context.Context,
	runID string,
	expectedRevision uint64,
	response interaction.ApprovalResponse,
) (kernel.Snapshot, error) {
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
	resolved, err := runner.approvals.Resolve(snapshot.Checkpoint, response)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if response.Decision == interaction.DecisionReject {
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
		if prepareErr != nil || prepared.Run.Status != kernel.RunStatusRunning {
			return prepared, true, prepareErr
		}
		executed, executeErr := runner.executePending(ctx, prepared)
		return executed, executeErr != nil, executeErr
	}
	if state.LLMCalls >= state.Limits.MaxLLMCalls {
		failed, failErr := runner.fail(ctx, snapshot, state, "agent.llm_limit", ErrCallLimit)
		return failed, true, failErr
	}
	definitions, response, err := runner.callModel(ctx, snapshot, state)
	if err != nil {
		failed, failErr := runner.fail(ctx, snapshot, state, "agent.model_failed", err)
		return failed, true, failErr
	}
	state.LLMCalls++
	if len(response.ToolCalls) == 0 {
		completed, completeErr := runner.complete(ctx, snapshot, state, response)
		return completed, true, completeErr
	}
	if state.ToolCalls+len(response.ToolCalls) > state.Limits.MaxToolCalls {
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

func (runner *Runner) callModel(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
) ([]tools.Definition, ModelResponse, error) {
	definitions, hostedTools, err := runner.resolveSelectedTools(ctx, state.ToolKeys, state.Model)
	if err != nil {
		return nil, ModelResponse{}, err
	}
	request := ModelRequest{
		RunID: snapshot.Run.ID, Model: state.Model,
		Messages: cloneMessages(state.Messages), Tools: definitions, HostedTools: cloneHostedTools(hostedTools),
	}
	runner.publish(ctx, snapshot.Run.ID, runfeed.Draft{
		Type: runfeed.EventModelStarted, Revision: snapshot.Run.Revision, Status: string(snapshot.Run.Status),
	})
	response, err := runner.generateModel(ctx, request)
	if err != nil {
		return nil, ModelResponse{}, errors.Join(ErrModelFailure, err)
	}
	response.Content = strings.TrimSpace(response.Content)
	if !validModelResponse(response) {
		return nil, ModelResponse{}, ErrInvalidModelResponse
	}
	runner.publish(ctx, snapshot.Run.ID, runfeed.Draft{
		Type: runfeed.EventModelCompleted, Revision: snapshot.Run.Revision, Status: string(snapshot.Run.Status),
	})
	return definitions, response, nil
}

func (runner *Runner) generateModel(ctx context.Context, request ModelRequest) (ModelResponse, error) {
	streaming, ok := runner.model.(StreamingModel)
	if !ok {
		return runner.model.Generate(ctx, request)
	}
	return streaming.GenerateStream(ctx, request, func(event ModelStreamEvent) error {
		runner.publishValue(ctx, request.RunID, runfeed.Draft{
			Type: runfeed.EventModelDelta, Delta: event.Delta,
		}, event)
		return nil
	})
}

func (runner *Runner) resolveSelectedTools(
	ctx context.Context,
	keys []string,
	model string,
) ([]tools.Definition, []HostedTool, error) {
	local := make([]tools.Definition, 0, len(keys))
	hosted := make([]HostedTool, 0, len(keys))
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
		definition, hostedTool, err := runner.resolveSelectedTool(ctx, key, model)
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
	model string,
) (*tools.Definition, *HostedTool, error) {
	if definition, ok := runner.catalog.Resolve(key); ok {
		return &definition, nil, nil
	}
	if runner.hostedTools == nil {
		return nil, nil, tools.ErrToolNotFound
	}
	resolved, ok, err := runner.hostedTools.Resolve(ctx, key, strings.TrimSpace(model))
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
	for index, call := range calls {
		call.ToolKey = strings.TrimSpace(call.ToolKey)
		if strings.TrimSpace(call.ID) == "" {
			callID, err := runner.runtime.NewID("toolcall")
			if err != nil {
				return kernel.Snapshot{}, err
			}
			call.ID = callID
		}
		definition, ok := selectedDefinition(definitions, call.ToolKey)
		if !ok || !json.Valid(call.Arguments) || definition.Terminal && index != len(calls)-1 {
			return runner.fail(ctx, snapshot, state, "agent.tool_invalid", tools.ErrInvalidCall)
		}
		preparedCalls[index] = call
		preparedCalls[index].Arguments = append(json.RawMessage(nil), call.Arguments...)
	}
	state.Messages = append(state.Messages, Message{
		Role: RoleAssistant, Content: strings.TrimSpace(content),
		ToolCalls: cloneToolCalls(preparedCalls),
	})
	state.PendingCalls = cloneToolCalls(preparedCalls)
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
			runner.publishValue(ctx, next.Run.ID, runfeed.Draft{
				Type: runfeed.EventToolRequested, Revision: next.Run.Revision, Status: string(next.Run.Status),
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
	if definition.ApprovalMode == tools.ApprovalNever {
		return snapshot, nil
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
		runner.publishRunEvent(ctx, waiting, runfeed.EventRunWaitingInput, false)
		runner.publishValue(ctx, waiting.Run.ID, runfeed.Draft{
			Type: runfeed.EventInteractionRequired, Revision: waiting.Run.Revision, Status: string(waiting.Run.Status),
		}, checkpoint)
	}
	return waiting, err
}

func (runner *Runner) executePending(ctx context.Context, snapshot kernel.Snapshot) (kernel.Snapshot, error) {
	state, err := decodeState(snapshot.State)
	call, ok := nextPendingCall(state)
	if err != nil || !ok {
		return runner.fail(ctx, snapshot, state, "agent.state_invalid", ErrInvalidRequest)
	}
	definition, ok := runner.catalog.Resolve(call.ToolKey)
	if !ok {
		return runner.fail(ctx, snapshot, state, "agent.tool_invalid", tools.ErrToolNotFound)
	}
	if state.ToolCalls >= state.Limits.MaxToolCalls {
		return runner.fail(ctx, snapshot, state, "agent.tool_limit", ErrCallLimit)
	}
	runner.publishValue(ctx, snapshot.Run.ID, runfeed.Draft{
		Type: runfeed.EventToolStarted, Revision: snapshot.Run.Revision, Status: string(snapshot.Run.Status),
	}, call)
	result, err := runner.executor.Execute(ctx, tools.ExecutionRequest{RunID: snapshot.Run.ID, Call: call})
	if err != nil {
		if code, message, recoverable := tools.RecoverableCallErrorInfo(err); recoverable {
			return runner.recordRecoverableToolError(ctx, snapshot, state, code, message)
		}
		return runner.fail(ctx, snapshot, state, "agent.tool_failed", errors.Join(ErrToolFailure, err))
	}
	state.Messages = append(state.Messages, Message{
		Role: RoleTool, Content: string(result.Content), ToolCallID: call.ID,
	})
	state.PendingCalls = remainingPendingCalls(state)
	state.ToolCalls++
	if definition.Terminal {
		if len(state.PendingCalls) != 0 {
			return runner.fail(ctx, snapshot, state, "agent.state_invalid", ErrInvalidRequest)
		}
		return runner.completeWithToolResult(ctx, snapshot, state, result)
	}
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	next, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{Type: "tool.completed", Message: result.Receipt.Disposition}},
	})
	if err == nil {
		runner.publishToolEvent(ctx, next, runfeed.EventToolCompleted, result.Receipt)
	}
	return next, err
}

func (runner *Runner) recordRecoverableToolError(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	code string,
	message string,
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
	state.Messages = append(state.Messages, Message{
		Role: RoleTool, Content: string(content), ToolCallID: callID,
	})
	state.PendingCalls = remainingPendingCalls(state)
	state.ToolCalls++
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	next, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{Type: "tool.correction_requested", Message: strings.TrimSpace(code)}},
	})
	if err == nil {
		runner.publishValue(ctx, next.Run.ID, runfeed.Draft{
			Type: runfeed.EventToolCompleted, Message: strings.TrimSpace(code),
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
		Events: []kernel.EventDraft{{Type: "interaction.resolved", Message: "Tool call approved"}},
	})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	running, err = runner.executePending(ctx, running)
	if err != nil {
		return running, err
	}
	return runner.drive(ctx, running)
}

func (runner *Runner) resumeRejected(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	checkpoint *kernel.Checkpoint,
	response interaction.ApprovalResponse,
) (kernel.Snapshot, error) {
	call, ok := nextPendingCall(state)
	if !ok {
		return runner.fail(ctx, snapshot, state, "agent.state_invalid", ErrInvalidRequest)
	}
	state.Messages = append(state.Messages, Message{
		Role: RoleTool, Content: rejectedToolContent(response.Comment), ToolCallID: call.ID,
	})
	state.PendingCalls = remainingPendingCalls(state)
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	running, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: checkpoint,
		Events: []kernel.EventDraft{{Type: "tool.rejected", Message: "Tool call rejected"}},
	})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.drive(ctx, running)
}

func (runner *Runner) complete(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	response ModelResponse,
) (kernel.Snapshot, error) {
	state.Messages = append(state.Messages, Message{Role: RoleAssistant, Content: response.Content})
	encodedState, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	result, err := terminalResult(response)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	completed, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: encodedState, Checkpoint: snapshot.Checkpoint,
		Result: result,
		Events: []kernel.EventDraft{{Type: "agent.completed", Message: "Direct Agent loop completed"}},
	})
	if err == nil {
		runner.publishRunEvent(ctx, completed, runfeed.EventRunCompleted, true)
	}
	return completed, err
}

func terminalResult(response ModelResponse) (*kernel.Result, error) {
	if len(response.HostedToolCalls) == 0 && len(response.Artifacts) == 0 && len(response.Citations) == 0 {
		encodedContent, err := json.Marshal(response.Content)
		if err != nil {
			return nil, errors.Join(ErrInvalidModelResponse, err)
		}
		return &kernel.Result{ContentType: "text", Content: encodedContent}, nil
	}
	value := Result{
		Content:         response.Content,
		HostedToolCalls: cloneHostedToolCalls(response.HostedToolCalls),
		Artifacts:       cloneArtifactRefs(response.Artifacts),
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
		runner.publishRunEvent(ctx, failed, runfeed.EventRunFailed, true)
	}
	return failed, errors.Join(cause, transitionErr)
}

func (runner *Runner) publishRunEvent(ctx context.Context, snapshot kernel.Snapshot, eventType string, terminal bool) {
	runner.publish(ctx, snapshot.Run.ID, runfeed.Draft{
		Type: eventType, Message: snapshot.Run.ErrorDetail, Revision: snapshot.Run.Revision,
		Status: string(snapshot.Run.Status), Terminal: terminal,
	})
}

func (runner *Runner) publishToolEvent(
	ctx context.Context,
	snapshot kernel.Snapshot,
	eventType string,
	value interface{},
) {
	runner.publishValue(ctx, snapshot.Run.ID, runfeed.Draft{
		Type: eventType, Revision: snapshot.Run.Revision, Status: string(snapshot.Run.Status),
	}, value)
}

func (runner *Runner) publishValue(ctx context.Context, runID string, draft runfeed.Draft, value interface{}) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	draft.Data = data
	runner.publish(ctx, runID, draft)
}

func (runner *Runner) publish(ctx context.Context, runID string, draft runfeed.Draft) {
	if runner == nil || runner.feed == nil {
		return
	}
	publishContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_, _ = runner.feed.Publish(publishContext, runID, draft)
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

func validModelResponse(response ModelResponse) bool {
	return len(response.ToolCalls) > 0 || response.Content != "" || len(response.HostedToolCalls) > 0 || len(response.Artifacts) > 0
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

func cloneHostedTools(values []HostedTool) []HostedTool {
	result := make([]HostedTool, len(values))
	for index, item := range values {
		result[index] = item
		result[index].Target = cloneRawJSON(item.Target)
	}
	return result
}

func cloneHostedToolCalls(values []HostedToolCall) []HostedToolCall {
	result := make([]HostedToolCall, len(values))
	for index, item := range values {
		result[index] = item
		result[index].Input = cloneRawJSON(item.Input)
		result[index].Output = cloneRawJSON(item.Output)
		result[index].Error = cloneRawJSON(item.Error)
	}
	return result
}

func cloneArtifactRefs(values []ArtifactRef) []ArtifactRef {
	result := make([]ArtifactRef, len(values))
	for index, item := range values {
		result[index] = item
		result[index].Metadata = cloneRawJSON(item.Metadata)
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
	if state.Limits.MaxLLMCalls <= 0 || state.Limits.MaxToolCalls <= 0 {
		return runState{}, ErrInvalidRequest
	}
	return state, nil
}

func resolveRunLimits(defaults Limits, requested Limits) (Limits, error) {
	if requested.MaxLLMCalls < 0 || requested.MaxToolCalls < 0 {
		return Limits{}, ErrInvalidRequest
	}
	resolved := requested
	if resolved.MaxLLMCalls == 0 {
		resolved.MaxLLMCalls = defaults.MaxLLMCalls
	}
	if resolved.MaxToolCalls == 0 {
		resolved.MaxToolCalls = defaults.MaxToolCalls
	}
	if resolved.MaxLLMCalls <= 0 || resolved.MaxToolCalls <= 0 {
		return Limits{}, ErrInvalidRequest
	}
	return resolved, nil
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

func cloneMessages(values []Message) []Message {
	result := make([]Message, len(values))
	for index, message := range values {
		result[index] = message
		result[index].ToolCalls = cloneToolCalls(message.ToolCalls)
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
