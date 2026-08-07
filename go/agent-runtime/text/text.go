package text

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

const CapabilityRunner kernel.Capability = "text.runner"

var (
	ErrInvalidRequest       = errors.New("invalid text run request")
	ErrInvalidModelResponse = errors.New("invalid text model response")
	ErrModelFailure         = errors.New("text model failure")
	ErrToolFailure          = errors.New("text tool failure")
	ErrCallLimit            = errors.New("text run call limit exceeded")
	ErrApprovalRequired     = errors.New("text run approval is required")
)

// Role identifies one direct Text loop message role.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is the provider-neutral transcript consumed by a Text model.
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
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: encodedState, Checkpoint: snapshot.Checkpoint,
		Result: &kernel.Result{
			ContentType: "application/json",
			Content:     append(json.RawMessage(nil), result.Content...),
		},
		Events: []kernel.EventDraft{
			{Type: "tool.completed", Message: result.Receipt.Disposition},
			{Type: "text.completed", Message: "Terminal Tool completed the Text loop"},
		},
	})
}

// ModelRequest is one direct Text loop model call.
type ModelRequest struct {
	RunID    string
	Model    string
	Messages []Message
	Tools    []tools.Definition
}

// ModelResponse returns final content, a bounded Tool call batch, or both.
type ModelResponse struct {
	Content   string
	ToolCalls []tools.Call
}

// Model is the only model dependency of the Text feature.
type Model interface {
	Generate(context.Context, ModelRequest) (ModelResponse, error)
}

// Limits are hard ceilings for one direct Text loop.
type Limits struct {
	MaxLLMCalls  int `json:"maxLLMCalls"`
	MaxToolCalls int `json:"maxToolCalls"`
}

// Dependencies explicitly provide the direct Text loop capabilities.
type Dependencies struct {
	Runtime   *kernel.Runtime
	Model     Model
	Catalog   tools.Catalog
	Executor  tools.Executor
	Approvals *interaction.Approvals
	Limits    Limits
}

// StartRequest starts one direct Text Agent Run.
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

// Runner owns only the direct Text model and Tool loop.
type Runner struct {
	runtime   *kernel.Runtime
	model     Model
	catalog   tools.Catalog
	executor  tools.Executor
	approvals *interaction.Approvals
	limits    Limits
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

// NewRunner constructs a direct Text feature without planning or automatic routing.
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
		executor: dependencies.Executor, approvals: dependencies.Approvals, limits: dependencies.Limits,
	}, nil
}

// Descriptor declares the explicit Text capability graph.
func (runner *Runner) Descriptor() kernel.FeatureDescriptor {
	return kernel.FeatureDescriptor{
		Name: "text",
		Requires: []kernel.Capability{
			kernel.CapabilityRuntime, tools.CapabilityCatalog, tools.CapabilityExecutor, interaction.CapabilityApproval,
		},
		Provides: []kernel.Capability{CapabilityRunner},
	}
}

// StartRun creates and drives one direct Text Run until terminal or approval wait.
func (runner *Runner) StartRun(ctx context.Context, request StartRequest) (kernel.Snapshot, error) {
	if runner == nil || strings.TrimSpace(request.Goal) == "" {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	if _, err := runner.catalog.List(request.ToolKeys); err != nil {
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
		ID: request.ID, Kind: kernel.RunKindText, Actor: request.Actor, Thread: request.Thread,
		RequestID: request.RequestID, Goal: request.Goal, State: encoded,
		Events: []kernel.EventDraft{{Type: "text.started", Message: "Direct Text loop started"}},
	})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.drive(ctx, snapshot)
}

// ResolveApproval resumes one waiting Text Run with an explicit decision.
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

// LoadRun returns one Text Run snapshot for parent orchestrators and recovery.
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
		failed, failErr := runner.fail(ctx, snapshot, runState{}, "text.state_invalid", err)
		return failed, true, failErr
	}
	if len(state.PendingCalls) > 0 {
		definitions, catalogErr := runner.catalog.List(state.ToolKeys)
		if catalogErr != nil {
			failed, failErr := runner.fail(ctx, snapshot, state, "text.tool_invalid", catalogErr)
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
		failed, failErr := runner.fail(ctx, snapshot, state, "text.llm_limit", ErrCallLimit)
		return failed, true, failErr
	}
	definitions, response, err := runner.callModel(ctx, snapshot, state)
	if err != nil {
		failed, failErr := runner.fail(ctx, snapshot, state, "text.model_failed", err)
		return failed, true, failErr
	}
	state.LLMCalls++
	if len(response.ToolCalls) == 0 {
		completed, completeErr := runner.complete(ctx, snapshot, state, response.Content)
		return completed, true, completeErr
	}
	if state.ToolCalls+len(response.ToolCalls) > state.Limits.MaxToolCalls {
		failed, failErr := runner.fail(ctx, snapshot, state, "text.tool_limit", ErrCallLimit)
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
	definitions, err := runner.catalog.List(state.ToolKeys)
	if err != nil {
		return nil, ModelResponse{}, err
	}
	response, err := runner.model.Generate(ctx, ModelRequest{
		RunID: snapshot.Run.ID, Model: state.Model,
		Messages: cloneMessages(state.Messages), Tools: definitions,
	})
	if err != nil {
		return nil, ModelResponse{}, errors.Join(ErrModelFailure, err)
	}
	response.Content = strings.TrimSpace(response.Content)
	if !validModelResponse(response) {
		return nil, ModelResponse{}, ErrInvalidModelResponse
	}
	return definitions, response, nil
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
			return runner.fail(ctx, snapshot, state, "text.tool_invalid", tools.ErrInvalidCall)
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
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{Type: "tool.batch_requested", Message: "Tool call batch requested"}},
	})
}

func (runner *Runner) preparePendingApproval(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	definitions []tools.Definition,
) (kernel.Snapshot, error) {
	call, ok := nextPendingCall(state)
	if !ok {
		return runner.fail(ctx, snapshot, state, "text.state_invalid", ErrInvalidRequest)
	}
	definition, ok := selectedDefinition(definitions, call.ToolKey)
	if !ok {
		return runner.fail(ctx, snapshot, state, "text.tool_invalid", tools.ErrInvalidCall)
	}
	if definition.ApprovalMode == tools.ApprovalNever {
		return snapshot, nil
	}
	checkpoint, err := runner.approvals.PrepareToolApproval(call, definition)
	if err != nil {
		return runner.fail(ctx, snapshot, state, "text.approval_invalid", err)
	}
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	waiting, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusWaitingInput, State: encoded, Checkpoint: checkpoint,
		Events: []kernel.EventDraft{{Type: "interaction.created", Message: "Tool approval required"}},
	})
	return waiting, err
}

func (runner *Runner) executePending(ctx context.Context, snapshot kernel.Snapshot) (kernel.Snapshot, error) {
	state, err := decodeState(snapshot.State)
	call, ok := nextPendingCall(state)
	if err != nil || !ok {
		return runner.fail(ctx, snapshot, state, "text.state_invalid", ErrInvalidRequest)
	}
	definition, ok := runner.catalog.Resolve(call.ToolKey)
	if !ok {
		return runner.fail(ctx, snapshot, state, "text.tool_invalid", tools.ErrToolNotFound)
	}
	if state.ToolCalls >= state.Limits.MaxToolCalls {
		return runner.fail(ctx, snapshot, state, "text.tool_limit", ErrCallLimit)
	}
	result, err := runner.executor.Execute(ctx, tools.ExecutionRequest{RunID: snapshot.Run.ID, Call: call})
	if err != nil {
		if code, message, recoverable := tools.RecoverableCallErrorInfo(err); recoverable {
			return runner.recordRecoverableToolError(ctx, snapshot, state, code, message)
		}
		return runner.fail(ctx, snapshot, state, "text.tool_failed", errors.Join(ErrToolFailure, err))
	}
	state.Messages = append(state.Messages, Message{
		Role: RoleTool, Content: string(result.Content), ToolCallID: call.ID,
	})
	state.PendingCalls = remainingPendingCalls(state)
	state.ToolCalls++
	if definition.Terminal {
		if len(state.PendingCalls) != 0 {
			return runner.fail(ctx, snapshot, state, "text.state_invalid", ErrInvalidRequest)
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
		return runner.fail(ctx, snapshot, state, "text.state_invalid", ErrInvalidRequest)
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
		return runner.fail(ctx, snapshot, state, "text.tool_error_invalid", err)
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
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{Type: "tool.correction_requested", Message: strings.TrimSpace(code)}},
	})
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
		return runner.fail(ctx, snapshot, state, "text.state_invalid", ErrInvalidRequest)
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
	content string,
) (kernel.Snapshot, error) {
	state.Messages = append(state.Messages, Message{Role: RoleAssistant, Content: content})
	encodedState, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	encodedContent, err := json.Marshal(content)
	if err != nil {
		return kernel.Snapshot{}, errors.Join(ErrInvalidModelResponse, err)
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: encodedState, Checkpoint: snapshot.Checkpoint,
		Result: &kernel.Result{ContentType: "text", Content: encodedContent},
		Events: []kernel.EventDraft{{Type: "text.completed", Message: "Direct Text loop completed"}},
	})
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
		Events: []kernel.EventDraft{{Type: "text.failed", Message: code}},
	})
	return failed, errors.Join(cause, transitionErr)
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
	return len(response.ToolCalls) > 0 || response.Content != ""
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
