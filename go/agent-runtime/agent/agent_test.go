package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

const (
	publishToolKey         = "story.publish_change_set"
	publishToolName        = "story_publish_change_set"
	manifestToolKey        = "story.get_manifest"
	manifestToolName       = "story_get_manifest"
	committedDisposition   = "committed"
	defaultTenantID        = "default"
	conversationThreadKind = "conversation"
	callGood               = "call_good"
	callOne                = "call_1"
	callTwo                = "call_2"
)

var (
	errDomainRejected      = errors.New("domain rejected arguments")
	errDatabaseUnavailable = errors.New("database unavailable")
)

func newTestRuntimeAndApprovals(t *testing.T) (*kernel.Runtime, *interaction.Approvals) {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	approvals, err := interaction.New(runtime)
	if err != nil {
		t.Fatal(err)
	}
	return runtime, approvals
}

type perRunLimitModel struct {
	calls int
}

func (model *perRunLimitModel) Generate(_ context.Context, _ agent.ModelRequest) (agent.ModelResponse, error) {
	model.calls++
	if model.calls == 1 {
		return agent.ModelResponse{ToolCalls: []tools.Call{
			{ID: callOne, ToolKey: manifestToolKey, Arguments: json.RawMessage(`{}`)},
			{ID: callTwo, ToolKey: manifestToolKey, Arguments: json.RawMessage(`{}`)},
		}}, nil
	}
	return agent.ModelResponse{Content: "done"}, nil
}

func TestRunnerFreezesPerRunLimits(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	registry := mustRegistry(t, []tools.Registration{{
		Definition: tools.Definition{
			Key: manifestToolKey, Name: manifestToolName,
			InputSchema: json.RawMessage(`{"type":"object"}`), ApprovalMode: tools.ApprovalNever,
		},
		Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
			return tools.ExecutionResult{
				Content: json.RawMessage(`{"ok":true}`),
				Receipt: tools.Receipt{ExecutionID: "read", Disposition: committedDisposition},
			}, nil
		}),
	}})
	model := &perRunLimitModel{}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: model, Catalog: registry, Executor: registry, Approvals: approvals,
		Limits: agent.Limits{MaxLLMCalls: 1, MaxToolCalls: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest("run_per_limits", "request_per_limits", "read twice", manifestToolKey)
	request.Limits = agent.Limits{MaxLLMCalls: 2, MaxToolCalls: 2}
	snapshot, err := runner.StartRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Run.Status != kernel.RunStatusCompleted || model.calls != 2 {
		t.Fatalf("snapshot = %#v, model calls = %d", snapshot.Run, model.calls)
	}
	if !strings.Contains(string(snapshot.State), `"limits":{"maxLLMCalls":2,"maxToolCalls":2}`) {
		t.Fatalf("per-run limits were not frozen in state: %s", snapshot.State)
	}
}

func mustRegistry(t *testing.T, registrations []tools.Registration) *tools.Registry {
	t.Helper()
	registry, err := tools.NewRegistry(registrations)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func mustRunner(
	t *testing.T,
	runtime *kernel.Runtime,
	approvals *interaction.Approvals,
	model agent.Model,
	registry *tools.Registry,
) *agent.Runner {
	t.Helper()
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: model, Catalog: registry, Executor: registry, Approvals: approvals,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func startRequest(id, requestID, goal string, toolKeys ...string) agent.StartRequest {
	return agent.StartRequest{
		ID: id, Actor: kernel.ActorRef{TenantID: defaultTenantID, ActorID: "1"},
		Thread:    kernel.ThreadRef{Kind: conversationThreadKind, ID: "conversation_1"},
		RequestID: requestID, Goal: goal, ToolKeys: toolKeys,
	}
}

type transcriptModel struct {
	t        *testing.T
	requests []agent.ModelRequest
}

type terminalToolModel struct {
	t     *testing.T
	calls int
}

func (model *terminalToolModel) Generate(
	_ context.Context,
	_ agent.ModelRequest,
) (agent.ModelResponse, error) {
	model.calls++
	if model.calls > 1 {
		model.t.Fatalf("terminal Tool triggered an extra model call")
	}
	return agent.ModelResponse{ToolCalls: []tools.Call{{
		ID: "call_publish", ToolKey: publishToolKey,
		Arguments: json.RawMessage(`{"title":"ready"}`),
	}}}, nil
}

func TestRunnerCompletesImmediatelyAfterTerminalTool(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	executions := 0
	registry := mustRegistry(t, []tools.Registration{{
		Definition: tools.Definition{
			Key: publishToolKey, Name: publishToolName,
			InputSchema:  json.RawMessage(`{"type":"object"}`),
			ApprovalMode: tools.ApprovalNever, Terminal: true,
		},
		Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
			executions++
			return tools.ExecutionResult{
				Content: json.RawMessage(`{"changeSetID":"change_1"}`),
				Receipt: tools.Receipt{ExecutionID: "change_1", Disposition: committedDisposition},
			}, nil
		}),
	}})
	model := &terminalToolModel{t: t}
	runner := mustRunner(t, runtime, approvals, model, registry)
	snapshot, err := runner.StartRun(t.Context(), startRequest(
		"run_terminal", "request_terminal", "publish", publishToolKey,
	))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Run.Status != kernel.RunStatusCompleted || model.calls != 1 || executions != 1 || snapshot.Result == nil {
		t.Fatalf("snapshot = %#v, model calls = %d, executions = %d", snapshot.Run, model.calls, executions)
	}
	if snapshot.Result.ContentType != "application/json" || string(snapshot.Result.Content) != `{"changeSetID":"change_1"}` {
		t.Fatalf("terminal result = %#v", snapshot.Result)
	}
}

type invalidTerminalBatchModel struct{}

func (invalidTerminalBatchModel) Generate(_ context.Context, _ agent.ModelRequest) (agent.ModelResponse, error) {
	return agent.ModelResponse{ToolCalls: []tools.Call{
		{ID: "call_publish", ToolKey: publishToolKey, Arguments: json.RawMessage(`{}`)},
		{ID: "call_read", ToolKey: manifestToolKey, Arguments: json.RawMessage(`{}`)},
	}}, nil
}

func TestRunnerRejectsTerminalToolBeforeBatchEnd(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	executions := 0
	handler := tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
		executions++
		return tools.ExecutionResult{
			Content: json.RawMessage(`{}`),
			Receipt: tools.Receipt{ExecutionID: "receipt", Disposition: committedDisposition},
		}, nil
	})
	registry := mustRegistry(t, []tools.Registration{
		{
			Definition: tools.Definition{
				Key: publishToolKey, Name: publishToolName,
				InputSchema:  json.RawMessage(`{"type":"object"}`),
				ApprovalMode: tools.ApprovalNever, Terminal: true,
			},
			Handler: handler,
		},
		{
			Definition: tools.Definition{
				Key: manifestToolKey, Name: manifestToolName,
				InputSchema: json.RawMessage(`{"type":"object"}`), ApprovalMode: tools.ApprovalNever,
			},
			Handler: handler,
		},
	})
	runner := mustRunner(t, runtime, approvals, invalidTerminalBatchModel{}, registry)
	snapshot, err := runner.StartRun(t.Context(), startRequest(
		"run_invalid_terminal_batch", "request_invalid_terminal_batch", "publish then read",
		publishToolKey, manifestToolKey,
	))
	if !errors.Is(err, tools.ErrInvalidCall) || snapshot.Run.Status != kernel.RunStatusFailed || executions != 0 {
		t.Fatalf("snapshot = %#v, error = %v, executions = %d", snapshot.Run, err, executions)
	}
}

type correctionModel struct {
	t        *testing.T
	requests []agent.ModelRequest
}

func (model *correctionModel) Generate(
	_ context.Context,
	request agent.ModelRequest,
) (agent.ModelResponse, error) {
	model.requests = append(model.requests, request)
	switch len(model.requests) {
	case 1:
		return agent.ModelResponse{ToolCalls: []tools.Call{{
			ID: "call_bad", ToolKey: publishToolKey,
			Arguments: json.RawMessage(`{"unexpected":true}`),
		}}}, nil
	case 2:
		return correctionRetryResponse(model.t, request)
	case 3:
		return correctionCompletionResponse(model.t, request)
	default:
		model.t.Fatalf("unexpected correction model call %d", len(model.requests))
		return agent.ModelResponse{}, nil
	}
}

func correctionRetryResponse(t *testing.T, request agent.ModelRequest) (agent.ModelResponse, error) {
	t.Helper()
	if len(request.Messages) != 3 {
		t.Fatalf("correction transcript length = %d, want 3", len(request.Messages))
	}
	toolResult := request.Messages[2]
	if toolResult.Role != agent.RoleTool || toolResult.ToolCallID != "call_bad" {
		t.Fatalf("correction tool result = %#v", toolResult)
	}
	assertRecoverableCorrectionPayload(t, toolResult.Content)
	return agent.ModelResponse{ToolCalls: []tools.Call{{
		ID: callGood, ToolKey: publishToolKey,
		Arguments: json.RawMessage(`{"title":"fixed"}`),
	}}}, nil
}

func assertRecoverableCorrectionPayload(t *testing.T, content string) {
	t.Helper()
	var payload struct {
		OK        bool `json:"ok"`
		Retryable bool `json:"retryable"`
		Error     struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.OK || !payload.Retryable || payload.Error.Code != "story.invalid_arguments" || payload.Error.Message != "remove unexpected" {
		t.Fatalf("correction payload = %#v", payload)
	}
}

func correctionCompletionResponse(t *testing.T, request agent.ModelRequest) (agent.ModelResponse, error) {
	t.Helper()
	if len(request.Messages) != 5 || request.Messages[4].Role != agent.RoleTool || request.Messages[4].ToolCallID != callGood {
		t.Fatalf("corrected transcript = %#v", request.Messages)
	}
	return agent.ModelResponse{Content: "completed after correction"}, nil
}

func TestRunnerLetsModelCorrectExplicitRecoverableToolError(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	toolCalls := 0
	registry := mustRegistry(t, []tools.Registration{{
		Definition: tools.Definition{
			Key: publishToolKey, Name: publishToolName,
			InputSchema:  json.RawMessage(`{"type":"object"}`),
			ApprovalMode: tools.ApprovalNever,
		},
		Handler: tools.HandlerFunc(func(
			_ context.Context,
			request tools.ExecutionRequest,
		) (tools.ExecutionResult, error) {
			toolCalls++
			if toolCalls == 1 {
				return tools.ExecutionResult{}, tools.NewRecoverableCallError(
					"story.invalid_arguments",
					"remove unexpected",
					errDomainRejected,
				)
			}
			if request.Call.ID != callGood {
				t.Fatalf("corrected tool call ID = %q", request.Call.ID)
			}
			return tools.ExecutionResult{
				Content: json.RawMessage(`{"changeSetID":"change_1"}`),
				Receipt: tools.Receipt{ExecutionID: "receipt_good", Disposition: committedDisposition},
			}, nil
		}),
	}})
	model := &correctionModel{t: t}
	runner := mustRunner(t, runtime, approvals, model, registry)
	snapshot, err := runner.StartRun(t.Context(), startRequest(
		"run_correction", "request_correction", "publish a change set", publishToolKey,
	))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Run.Status != kernel.RunStatusCompleted || len(model.requests) != 3 || toolCalls != 2 {
		t.Fatalf("run = %#v, model calls = %d, tool calls = %d", snapshot.Run, len(model.requests), toolCalls)
	}
}

type fatalToolModel struct{}

func (fatalToolModel) Generate(_ context.Context, _ agent.ModelRequest) (agent.ModelResponse, error) {
	return agent.ModelResponse{ToolCalls: []tools.Call{{
		ID: "call_fatal", ToolKey: publishToolKey, Arguments: json.RawMessage(`{}`),
	}}}, nil
}

func TestRunnerStillFailsUnmarkedToolErrors(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	registry := mustRegistry(t, []tools.Registration{{
		Definition: tools.Definition{
			Key: publishToolKey, Name: publishToolName,
			InputSchema:  json.RawMessage(`{"type":"object"}`),
			ApprovalMode: tools.ApprovalNever,
		},
		Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
			return tools.ExecutionResult{}, errDatabaseUnavailable
		}),
	}})
	runner := mustRunner(t, runtime, approvals, fatalToolModel{}, registry)
	snapshot, err := runner.StartRun(t.Context(), startRequest(
		"run_fatal", "request_fatal", "publish a change set", publishToolKey,
	))
	if !errors.Is(err, agent.ErrToolFailure) || snapshot.Run.Status != kernel.RunStatusFailed || snapshot.Run.ErrorCode != "agent.tool_failed" {
		t.Fatalf("snapshot = %#v, error = %v", snapshot.Run, err)
	}
}

func (model *transcriptModel) Generate(
	_ context.Context,
	request agent.ModelRequest,
) (agent.ModelResponse, error) {
	model.requests = append(model.requests, request)
	switch len(model.requests) {
	case 1:
		return transcriptInitialResponse(model.t, request)
	case 2:
		return transcriptCompletionResponse(model.t, request)
	default:
		model.t.Fatalf("unexpected model call %d", len(model.requests))
		return agent.ModelResponse{}, nil
	}
}

func transcriptInitialResponse(t *testing.T, request agent.ModelRequest) (agent.ModelResponse, error) {
	t.Helper()
	if len(request.Messages) != 1 || request.Messages[0].Role != agent.RoleUser {
		t.Fatalf("first transcript = %#v", request.Messages)
	}
	return agent.ModelResponse{
		Content: "I will inspect the frozen manifest and current units first.",
		ToolCalls: []tools.Call{
			{ID: callOne, ToolKey: manifestToolKey, Arguments: json.RawMessage(`{}`)},
			{ID: callTwo, ToolKey: manifestToolKey, Arguments: json.RawMessage(`{}`)},
		},
	}, nil
}

func transcriptCompletionResponse(t *testing.T, request agent.ModelRequest) (agent.ModelResponse, error) {
	t.Helper()
	if len(request.Messages) != 4 {
		t.Fatalf("second transcript length = %d, want 4", len(request.Messages))
	}
	assertAssistantToolBatch(t, request.Messages[1])
	assertOrderedToolResults(t, request.Messages[2:])
	return agent.ModelResponse{Content: "completed"}, nil
}

func assertAssistantToolBatch(t *testing.T, assistant agent.Message) {
	t.Helper()
	if assistant.Role != agent.RoleAssistant ||
		assistant.Content != "I will inspect the frozen manifest and current units first." ||
		len(assistant.ToolCalls) != 2 ||
		assistant.ToolCalls[0].ID != callOne || assistant.ToolCalls[0].ToolKey != manifestToolKey ||
		assistant.ToolCalls[1].ID != callTwo || assistant.ToolCalls[1].ToolKey != manifestToolKey {
		t.Fatalf("assistant tool turn = %#v", assistant)
	}
}

func assertOrderedToolResults(t *testing.T, messages []agent.Message) {
	t.Helper()
	for index, callID := range []string{callOne, callTwo} {
		toolResult := messages[index]
		if toolResult.Role != agent.RoleTool || toolResult.ToolCallID != callID ||
			toolResult.Content != `{"storyID":"story_1"}` {
			t.Fatalf("tool result turn %d = %#v", index, toolResult)
		}
	}
}

func TestRunnerPreservesAssistantToolCallBatchBeforeOrderedResults(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	registry := mustRegistry(t, []tools.Registration{{
		Definition: tools.Definition{
			Key: manifestToolKey, Name: manifestToolName,
			InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false}`),
			ApprovalMode: tools.ApprovalNever,
		},
		Handler: tools.HandlerFunc(func(
			_ context.Context,
			request tools.ExecutionRequest,
		) (tools.ExecutionResult, error) {
			if request.Call.ID != callOne && request.Call.ID != callTwo {
				t.Fatalf("tool call ID = %q", request.Call.ID)
			}
			return tools.ExecutionResult{
				Content: json.RawMessage(`{"storyID":"story_1"}`),
				Receipt: tools.Receipt{ExecutionID: "receipt_1", Disposition: committedDisposition},
			}, nil
		}),
	}})
	model := &transcriptModel{t: t}
	runner := mustRunner(t, runtime, approvals, model, registry)
	snapshot, err := runner.StartRun(t.Context(), startRequest(
		"run_1", "request_1", "read the manifest", manifestToolKey,
	))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Run.Status != kernel.RunStatusCompleted || len(model.requests) != 2 {
		t.Fatalf("run = %#v, model calls = %d", snapshot.Run, len(model.requests))
	}
	view, err := agent.ViewState(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Messages) != 5 || view.LLMCalls != 2 || view.ToolCalls != 2 {
		t.Fatalf("view = %#v", view)
	}
	if view.Messages[1].ToolCalls[0].ID != callOne || view.Messages[2].ToolCallID != callOne {
		t.Fatalf("view transcript = %#v", view.Messages)
	}
	view.Messages[1].ToolCalls[0].ID = "mutated"
	view.ToolKeys[0] = "mutated"
	reloaded, err := agent.ViewState(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Messages[1].ToolCalls[0].ID != callOne || reloaded.ToolKeys[0] != manifestToolKey {
		t.Fatalf("view mutated persisted state = %#v", reloaded)
	}
}
