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
	Role       Role   `json:"role"`
	Content    string `json:"content"`
	ToolCallID string `json:"toolCallID,omitempty"`
}

// ModelRequest is one direct Text loop model call.
type ModelRequest struct {
	RunID    string
	Messages []Message
	Tools    []tools.Definition
}

// ModelResponse returns either final content or one Tool call.
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
	MaxLLMCalls  int
	MaxToolCalls int
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
	ToolKeys  []string
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
	Messages  []Message   `json:"messages"`
	ToolKeys  []string    `json:"toolKeys"`
	Pending   *tools.Call `json:"pending,omitempty"`
	LLMCalls  int         `json:"llmCalls"`
	ToolCalls int         `json:"toolCalls"`
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
	state := runState{
		Messages: []Message{{Role: RoleUser, Content: strings.TrimSpace(request.Goal)}},
		ToolKeys: normalizedToolKeys(request.ToolKeys),
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
	if snapshot.Run.Status != kernel.RunStatusWaitingInput || state.Pending == nil {
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
	if state.Pending != nil {
		return snapshot, true, ErrApprovalRequired
	}
	if state.LLMCalls >= runner.limits.MaxLLMCalls {
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
	prepared, err := runner.prepareToolCall(ctx, snapshot, state, definitions, response.ToolCalls[0])
	if err != nil || prepared.Run.Status != kernel.RunStatusRunning {
		return prepared, true, err
	}
	executed, err := runner.executePending(ctx, prepared)
	return executed, err != nil, err
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
		RunID: snapshot.Run.ID, Messages: cloneMessages(state.Messages), Tools: definitions,
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

func (runner *Runner) prepareToolCall(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	definitions []tools.Definition,
	call tools.Call,
) (kernel.Snapshot, error) {
	call.ToolKey = strings.TrimSpace(call.ToolKey)
	if strings.TrimSpace(call.ID) == "" {
		callID, err := runner.runtime.NewID("toolcall")
		if err != nil {
			return kernel.Snapshot{}, err
		}
		call.ID = callID
	}
	definition, ok := selectedDefinition(definitions, call.ToolKey)
	if !ok || !json.Valid(call.Arguments) {
		return runner.fail(ctx, snapshot, state, "text.tool_invalid", tools.ErrInvalidCall)
	}
	state.Pending = &call
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	intent, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{Type: "tool.requested", Message: definition.Name}},
	})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if definition.ApprovalMode == tools.ApprovalNever {
		return intent, nil
	}
	checkpoint, err := runner.approvals.PrepareToolApproval(call, definition)
	if err != nil {
		return runner.fail(ctx, intent, state, "text.approval_invalid", err)
	}
	waiting, err := runner.runtime.Apply(ctx, intent.Run.ID, intent.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusWaitingInput, State: encoded, Checkpoint: checkpoint,
		Events: []kernel.EventDraft{{Type: "interaction.created", Message: "Tool approval required"}},
	})
	return waiting, err
}

func (runner *Runner) executePending(ctx context.Context, snapshot kernel.Snapshot) (kernel.Snapshot, error) {
	state, err := decodeState(snapshot.State)
	if err != nil || state.Pending == nil {
		return runner.fail(ctx, snapshot, state, "text.state_invalid", ErrInvalidRequest)
	}
	if state.ToolCalls >= runner.limits.MaxToolCalls {
		return runner.fail(ctx, snapshot, state, "text.tool_limit", ErrCallLimit)
	}
	result, err := runner.executor.Execute(ctx, tools.ExecutionRequest{RunID: snapshot.Run.ID, Call: *state.Pending})
	if err != nil {
		return runner.fail(ctx, snapshot, state, "text.tool_failed", errors.Join(ErrToolFailure, err))
	}
	state.Messages = append(state.Messages, Message{
		Role: RoleTool, Content: string(result.Content), ToolCallID: state.Pending.ID,
	})
	state.Pending = nil
	state.ToolCalls++
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
	state.Messages = append(state.Messages, Message{
		Role: RoleTool, Content: rejectedToolContent(response.Comment), ToolCallID: state.Pending.ID,
	})
	state.Pending = nil
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
	if len(response.ToolCalls) > 1 {
		return false
	}
	if len(response.ToolCalls) == 1 {
		return response.Content == ""
	}
	return response.Content != ""
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
	return state, nil
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
	return append([]Message(nil), values...)
}

func rejectedToolContent(comment string) string {
	encoded, err := json.Marshal(map[string]string{"status": "rejected", "comment": strings.TrimSpace(comment)})
	if err != nil {
		return `{"status":"rejected"}`
	}
	return string(encoded)
}
