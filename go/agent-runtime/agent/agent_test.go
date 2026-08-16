package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	runtimemodel "github.com/orz-i/Gaoge/sdk/go/agent-runtime/model"
	"strings"
	"testing"
	"time"

	interactionadapter "github.com/orz-i/Gaoge/sdk/go/agent-runtime/adapters/interaction"
	runfeedadapter "github.com/orz-i/Gaoge/sdk/go/agent-runtime/adapters/runfeed"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runfeed"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

const (
	publishToolKey         = "story.publish_change_set"
	publishToolName        = "story_publish_change_set"
	manifestToolKey        = "story.get_manifest"
	manifestToolName       = "story_get_manifest"
	selectionToolKey       = "story.get_selection"
	selectionToolName      = "story_get_selection"
	committedDisposition   = "committed"
	defaultTenantID        = "default"
	conversationThreadKind = "conversation"
	callGood               = "call_good"
	callOne                = "call_1"
	callTwo                = "call_2"
	hostedImageToolKey     = "openai.image_generation"
)

var (
	errDomainRejected      = errors.New("domain rejected arguments")
	errDatabaseUnavailable = errors.New("database unavailable")
)

type requiredApprovalPolicy struct{ name string }

func (policy requiredApprovalPolicy) Name() string { return policy.name }

func (policy requiredApprovalPolicy) Approval(
	context.Context,
	plugin.ToolInvocation,
) (plugin.ApprovalRequirement, error) {
	return plugin.ApprovalRequired, nil
}

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

func (model *perRunLimitModel) Generate(_ context.Context, _ runtimemodel.Request) (runtimemodel.Response, error) {
	model.calls++
	if model.calls == 1 {
		return runtimemodel.Response{ToolCalls: []tools.Call{
			{ID: callOne, ToolKey: manifestToolKey, Arguments: json.RawMessage(`{}`)},
			{ID: callTwo, ToolKey: manifestToolKey, Arguments: json.RawMessage(`{}`)},
		}}, nil
	}
	return runtimemodel.Response{Content: "done"}, nil
}

func TestRunnerFreezesPerRunLimits(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	registry := mustRegistry(t, []tools.Registration{{
		Definition: tools.Definition{
			Key: manifestToolKey, Name: manifestToolName,
			InputSchema: json.RawMessage(`{"type":"object"}`),
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
		Runtime: runtime, Model: model, Catalog: registry, Executor: registry,
		Approvals: interactionadapter.New(approvals),
		Limits:    agent.Limits{MaxLLMCalls: 1, MaxToolCalls: 1},
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

type requiredPublisherModel struct {
	t     *testing.T
	calls int
}

func (model *requiredPublisherModel) Generate(
	_ context.Context,
	request runtimemodel.Request,
) (runtimemodel.Response, error) {
	model.calls++
	switch model.calls {
	case 1:
		if request.RequireToolCall {
			model.t.Fatal("required Tool choice activated before completion correction")
		}
		return runtimemodel.Response{Content: "the publisher is unavailable"}, nil
	case 2:
		guidanceFound := false
		for _, message := range request.Messages {
			if message.Role == runtimemodel.RoleSystem && strings.Contains(message.Content, "Completion rejected") {
				guidanceFound = true
			}
		}
		if !guidanceFound {
			model.t.Fatalf("required Tool completion guidance missing: %#v", request.Messages)
		}
		if len(request.Tools) != 1 || request.Tools[0].Key != publishToolKey {
			model.t.Fatalf("completion correction did not isolate required Tool: %#v", request.Tools)
		}
		if !request.RequireToolCall {
			model.t.Fatal("required Tool choice missing after completion correction")
		}
		return runtimemodel.Response{ToolCalls: []tools.Call{{
			ID: callGood, ToolKey: publishToolKey, Arguments: json.RawMessage(`{"title":"draft"}`),
		}}}, nil
	default:
		return runtimemodel.Response{Content: "published"}, nil
	}
}

func TestRunnerRejectsCompletionUntilRequiredToolSucceeds(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	registry := mustRegistry(t, []tools.Registration{
		{
			Definition: tools.Definition{
				Key: publishToolKey, Name: publishToolName,
				InputSchema: json.RawMessage(`{"type":"object"}`),
			},
			Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
				return tools.ExecutionResult{
					Content: json.RawMessage(`{"changeSetID":"change_set_1"}`),
					Receipt: tools.Receipt{ExecutionID: callGood, Disposition: committedDisposition},
				}, nil
			}),
		},
		{
			Definition: tools.Definition{
				Key: selectionToolKey, Name: selectionToolName,
				InputSchema: json.RawMessage(`{"type":"object"}`),
			},
			Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
				return tools.ExecutionResult{
					Content: json.RawMessage(`{"selection":"foundation"}`),
					Receipt: tools.Receipt{ExecutionID: callGood, Disposition: committedDisposition},
				}, nil
			}),
		},
	})
	model := &requiredPublisherModel{t: t}
	runner := mustRunner(t, runtime, approvals, model, registry)
	request := startRequest(
		"run_required_publisher",
		"request_required_publisher",
		"publish",
		publishToolKey,
		selectionToolKey,
	)
	request.RequiredToolKeys = []string{publishToolKey}

	snapshot, err := runner.StartRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Run.Status != kernel.RunStatusCompleted || model.calls != 3 {
		t.Fatalf("snapshot = %#v, model calls = %d", snapshot.Run, model.calls)
	}
	view, err := agent.ViewState(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.RequiredToolKeys) != 1 || view.RequiredToolKeys[0] != publishToolKey {
		t.Fatalf("required Tool Keys = %#v", view.RequiredToolKeys)
	}
}

type repeatedReadModel struct {
	t        *testing.T
	requests []runtimemodel.Request
}

func (model *repeatedReadModel) Generate(
	_ context.Context,
	request runtimemodel.Request,
) (runtimemodel.Response, error) {
	model.requests = append(model.requests, request)
	hasRepeatedTool := false
	for _, definition := range request.Tools {
		if definition.Key == manifestToolKey {
			hasRepeatedTool = true
			break
		}
	}
	if hasRepeatedTool {
		callIDs := [...]string{"repeat_1", "repeat_2", "repeat_3", "repeat_4", "repeat_5", "repeat_6", "repeat_7", "repeat_8"}
		return runtimemodel.Response{ToolCalls: []tools.Call{{
			ID: callIDs[len(model.requests)-1], ToolKey: manifestToolKey, Arguments: json.RawMessage(`{}`),
		}}}, nil
	}
	if len(model.requests) != 3 {
		model.t.Fatalf("tool suppressed after %d model requests", len(model.requests))
	}
	guarded := false
	for _, message := range request.Messages {
		if message.Role == runtimemodel.RoleSystem && strings.Contains(message.Content, "same successful results twice") {
			guarded = true
		}
	}
	if !guarded {
		model.t.Fatalf("repeated Tool guidance missing: %#v", request.Messages)
	}
	return runtimemodel.Response{Content: "continued without repeating the read"}, nil
}

type repeatedReadBatchModel struct {
	t        *testing.T
	requests []runtimemodel.Request
}

type alternatingRepeatedReadModel struct {
	t        *testing.T
	requests []runtimemodel.Request
}

func (model *alternatingRepeatedReadModel) Generate(
	_ context.Context,
	request runtimemodel.Request,
) (runtimemodel.Response, error) {
	model.requests = append(model.requests, request)
	available := make(map[string]bool, len(request.Tools))
	for _, definition := range request.Tools {
		available[definition.Key] = true
	}
	switch len(model.requests) {
	case 1:
		return runtimemodel.Response{ToolCalls: []tools.Call{
			{ID: "alternating_selection_1", ToolKey: selectionToolKey, Arguments: json.RawMessage(`{}`)},
			{ID: "alternating_manifest_1", ToolKey: manifestToolKey, Arguments: json.RawMessage(`{}`)},
		}}, nil
	case 2:
		return runtimemodel.Response{ToolCalls: []tools.Call{{
			ID: "alternating_selection_2", ToolKey: selectionToolKey, Arguments: json.RawMessage(`{}`),
		}}}, nil
	case 3:
		return model.alternatingManifestResponse(request, available), nil
	case 4:
		return model.alternatingCompletionResponse(request, available), nil
	default:
		model.t.Fatalf("unexpected model request count: %d", len(model.requests))
		return runtimemodel.Response{}, nil
	}
}

func (model *alternatingRepeatedReadModel) alternatingManifestResponse(
	request runtimemodel.Request,
	available map[string]bool,
) runtimemodel.Response {
	if available[selectionToolKey] || !available[manifestToolKey] {
		model.t.Fatalf("selection Tool was not isolated after repetition: %#v", request.Tools)
	}
	return runtimemodel.Response{ToolCalls: []tools.Call{{
		ID: "alternating_manifest_2", ToolKey: manifestToolKey, Arguments: json.RawMessage(`{}`),
	}}}
}

func (model *alternatingRepeatedReadModel) alternatingCompletionResponse(
	request runtimemodel.Request,
	available map[string]bool,
) runtimemodel.Response {
	if available[selectionToolKey] || available[manifestToolKey] {
		model.t.Fatalf("alternating repeated Tools remained available: %#v", request.Tools)
	}
	return runtimemodel.Response{Content: "continued after alternating reads"}
}

func (model *repeatedReadBatchModel) Generate(
	_ context.Context,
	request runtimemodel.Request,
) (runtimemodel.Response, error) {
	model.requests = append(model.requests, request)
	available := make(map[string]bool, len(request.Tools))
	for _, definition := range request.Tools {
		available[definition.Key] = true
	}
	if len(model.requests) == 1 {
		return runtimemodel.Response{ToolCalls: []tools.Call{
			{ID: "batch_selection_1", ToolKey: selectionToolKey, Arguments: json.RawMessage(`{}`)},
			{ID: "batch_manifest_1", ToolKey: manifestToolKey, Arguments: json.RawMessage(`{}`)},
		}}, nil
	}
	if available[selectionToolKey] {
		return runtimemodel.Response{ToolCalls: []tools.Call{{
			ID: "batch_selection_2", ToolKey: selectionToolKey, Arguments: json.RawMessage(`{}`),
		}}}, nil
	}
	if len(model.requests) != 3 || !available[manifestToolKey] {
		model.t.Fatalf("batch Tool suppression request = %#v", request)
	}
	return runtimemodel.Response{Content: "continued after the repeated batch subset"}, nil
}

func TestRunnerSuppressesRepeatedToolSubsetAcrossConsecutiveBatches(t *testing.T) {
	model := &repeatedReadBatchModel{t: t}
	runner, toolCalls := newRepeatedBatchRunner(t, model)
	snapshot, err := runner.StartRun(t.Context(), startRequest(
		"run_repeated_batch", "request_repeated_batch", "continue after reading",
		selectionToolKey, manifestToolKey,
	))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Run.Status != kernel.RunStatusCompleted || len(model.requests) != 3 || *toolCalls != 3 {
		t.Fatalf("run = %#v, model calls = %d, tool calls = %d", snapshot.Run, len(model.requests), *toolCalls)
	}
}

func TestRunnerSuppressesRepeatedToolKeysAcrossAlternatingBatches(t *testing.T) {
	model := &alternatingRepeatedReadModel{t: t}
	runner, toolCalls := newRepeatedBatchRunner(t, model)
	snapshot, err := runner.StartRun(t.Context(), startRequest(
		"run_alternating_repeated_batch", "request_alternating_repeated_batch", "continue after reading",
		selectionToolKey, manifestToolKey,
	))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Run.Status != kernel.RunStatusCompleted || len(model.requests) != 4 || *toolCalls != 4 {
		t.Fatalf("run = %#v, model calls = %d, tool calls = %d", snapshot.Run, len(model.requests), *toolCalls)
	}
}

func newRepeatedBatchRunner(t *testing.T, client runtimemodel.Client) (*agent.Runner, *int) {
	t.Helper()
	runtime, approvals := newTestRuntimeAndApprovals(t)
	toolCalls := 0
	readHandler := tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
		toolCalls++
		return tools.ExecutionResult{
			Content: json.RawMessage(`{"storyID":"story_1"}`),
			Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: committedDisposition},
		}, nil
	})
	registry := mustRegistry(t, []tools.Registration{
		{Definition: tools.Definition{
			Key: selectionToolKey, Name: selectionToolName, InputSchema: json.RawMessage(`{"type":"object"}`),
		}, Handler: readHandler},
		{Definition: tools.Definition{
			Key: manifestToolKey, Name: manifestToolName, InputSchema: json.RawMessage(`{"type":"object"}`),
		}, Handler: readHandler},
	})
	return mustRunner(t, runtime, approvals, client, registry), &toolCalls
}

func TestRunnerSuppressesConsecutiveUnchangedToolLoopForOneModelStep(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	toolCalls := 0
	registry := mustRegistry(t, []tools.Registration{{
		Definition: tools.Definition{
			Key: manifestToolKey, Name: manifestToolName,
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
			toolCalls++
			return tools.ExecutionResult{
				Content: json.RawMessage(`{"storyID":"story_1"}`),
				Receipt: tools.Receipt{ExecutionID: "read", Disposition: committedDisposition},
			}, nil
		}),
	}})
	model := &repeatedReadModel{t: t}
	runner := mustRunner(t, runtime, approvals, model, registry)
	snapshot, err := runner.StartRun(t.Context(), startRequest(
		"run_repeated_read", "request_repeated_read", "continue after reading", manifestToolKey,
	))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Run.Status != kernel.RunStatusCompleted || len(model.requests) != 3 || toolCalls != 2 {
		t.Fatalf("run = %#v, model calls = %d, tool calls = %d", snapshot.Run, len(model.requests), toolCalls)
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
	model runtimemodel.Client,
	registry *tools.Registry,
) *agent.Runner {
	t.Helper()
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: model, Catalog: registry, Executor: registry,
		Approvals: interactionadapter.New(approvals),
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
	requests []runtimemodel.Request
}

type terminalToolModel struct {
	t     *testing.T
	calls int
}

type hostedCatalog struct {
	tool runtimemodel.HostedTool
}

func (catalog hostedCatalog) Resolve(_ context.Context, key string, _ string) (runtimemodel.HostedTool, bool, error) {
	if key != catalog.tool.Key {
		return runtimemodel.HostedTool{}, false, nil
	}
	return catalog.tool, true, nil
}

type hostedArtifactModel struct {
	t       *testing.T
	request runtimemodel.Request
}

func (model *hostedArtifactModel) Generate(_ context.Context, request runtimemodel.Request) (runtimemodel.Response, error) {
	model.request = request
	if len(request.Tools) != 0 {
		model.t.Fatalf("hosted Tool leaked into local definitions: %#v", request.Tools)
	}
	if len(request.HostedTools) != 1 || request.HostedTools[0].Key != hostedImageToolKey {
		model.t.Fatalf("hosted Tool activation missing: %#v", request.HostedTools)
	}
	return runtimemodel.Response{
		HostedToolCalls: []runtimemodel.HostedToolCall{{
			ID: "image_call_1", ToolKey: hostedImageToolKey, Status: "completed",
			Input: json.RawMessage(`{"prompt":"cat"}`),
		}},
		Artifacts: []runtimemodel.ArtifactRef{{
			ID: "file_image_1", Kind: "image", MediaType: "image/png", Name: "generated.png", SizeBytes: 128,
		}},
		Citations: []string{"artifact:file_image_1"},
	}, nil
}

func TestRunnerCompletesHostedToolArtifactWithoutPersistingBinary(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	registry := mustRegistry(t, nil)
	model := &hostedArtifactModel{t: t}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: model, Catalog: registry, Executor: registry,
		Approvals: interactionadapter.New(approvals),
		HostedTools: hostedCatalog{tool: runtimemodel.HostedTool{
			Key:    hostedImageToolKey,
			Target: json.RawMessage(`{"variants":[{"protocol":"openai_responses","payload":{"type":"image_generation"}}]}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest("run_hosted_image", "request_hosted_image", "draw a cat", hostedImageToolKey)
	request.ModelOptions = json.RawMessage(`{"reasoning":{"effort":"high"}}`)
	snapshot, err := runner.StartRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if string(model.request.ModelOptions) != string(request.ModelOptions) {
		t.Fatalf("model options = %s, want %s", model.request.ModelOptions, request.ModelOptions)
	}
	view, err := agent.ViewState(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if string(view.ModelOptions) != string(request.ModelOptions) {
		t.Fatalf("durable model options = %s, want %s", view.ModelOptions, request.ModelOptions)
	}
	assertHostedArtifactResult(t, snapshot)
}

func assertHostedArtifactResult(t *testing.T, snapshot kernel.Snapshot) {
	t.Helper()
	if snapshot.Run.Status != kernel.RunStatusCompleted || snapshot.Result == nil {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if snapshot.Result.ContentType != "agent_result" {
		t.Fatalf("content type = %q", snapshot.Result.ContentType)
	}
	var result agent.Result
	if err := json.Unmarshal(snapshot.Result.Content, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.HostedToolCalls) != 1 || len(result.Artifacts) != 1 || result.Artifacts[0].ID != "file_image_1" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if strings.Contains(string(snapshot.State), "iVBOR") || strings.Contains(string(snapshot.Result.Content), "iVBOR") {
		t.Fatalf("binary payload leaked into durable runtime data")
	}
}

func (model *terminalToolModel) Generate(
	_ context.Context,
	_ runtimemodel.Request,
) (runtimemodel.Response, error) {
	model.calls++
	if model.calls > 1 {
		model.t.Fatalf("terminal Tool triggered an extra model call")
	}
	return runtimemodel.Response{ToolCalls: []tools.Call{{
		ID: "call_publish", ToolKey: publishToolKey,
		Arguments: json.RawMessage(`{"title":"ready"}`),
	}}}, nil
}

func TestRunnerCompletesImmediatelyAfterTerminalTool(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	feed := newAgentTestFeed(t)
	executions := 0
	registry := mustRegistry(t, []tools.Registration{{
		Definition: tools.Definition{
			Key: publishToolKey, Name: publishToolName,
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Terminal:    true,
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
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: model, Catalog: registry, Executor: registry,
		Approvals: interactionadapter.New(approvals), Observers: []plugin.Observer{runfeedadapter.New(feed)},
	})
	if err != nil {
		t.Fatal(err)
	}
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
	assertAgentFeedTypes(t, feed, "run_terminal", []string{
		runfeed.EventRunStarted,
		runfeed.EventModelStarted,
		runfeed.EventModelCompleted,
		runfeed.EventToolRequested,
		runfeed.EventToolStarted,
		runfeed.EventToolCompleted,
		runfeed.EventRunCompleted,
	})
}

type approvalModel struct {
	calls int
}

func (model *approvalModel) Generate(context.Context, runtimemodel.Request) (runtimemodel.Response, error) {
	model.calls++
	if model.calls == 1 {
		return runtimemodel.Response{ToolCalls: []tools.Call{{
			ID: "approval_call", ToolKey: publishToolKey, Arguments: json.RawMessage(`{"title":"ready"}`),
		}}}, nil
	}
	return runtimemodel.Response{Content: "approved"}, nil
}

func TestDeferredToolApprovalRequiresExplicitResume(t *testing.T) {
	t.Parallel()
	fixture := newDeferredApprovalFixture(t)
	waiting := startDeferredApproval(t, fixture)
	resolved := resolveDeferredApproval(t, fixture, waiting)
	completed, err := fixture.runner.Resume(t.Context(), resolved.Run.ID, resolved.Run.Revision)
	assertDeferredApprovalCompleted(t, fixture, completed, err)
}

type deferredApprovalFixture struct {
	runner     *agent.Runner
	model      *approvalModel
	executions *int
}

func newDeferredApprovalFixture(t *testing.T) deferredApprovalFixture {
	t.Helper()
	runtime, approvals := newTestRuntimeAndApprovals(t)
	executions := 0
	registry := mustRegistry(t, []tools.Registration{{
		Definition: tools.Definition{
			Key: publishToolKey, Name: publishToolName,
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
			executions++
			return tools.ExecutionResult{
				Content: json.RawMessage(`{"ok":true}`),
				Receipt: tools.Receipt{ExecutionID: "approved", Disposition: committedDisposition},
			}, nil
		}),
	}})
	model := &approvalModel{}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: model, Catalog: registry, Executor: registry,
		Approvals:        interactionadapter.New(approvals),
		ApprovalPolicies: []plugin.ApprovalPolicy{requiredApprovalPolicy{name: "test-required"}},
		DeferResumption:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return deferredApprovalFixture{runner: runner, model: model, executions: &executions}
}

func startDeferredApproval(t *testing.T, fixture deferredApprovalFixture) kernel.Snapshot {
	t.Helper()
	waiting, err := fixture.runner.StartRun(t.Context(), startRequest(
		"run_deferred_approval", "request_deferred_approval", "publish", publishToolKey,
	))
	if err != nil || waiting.Run.Status != kernel.RunStatusWaitingInput || *fixture.executions != 0 {
		t.Fatalf("waiting=%#v executions=%d err=%v", waiting.Run, *fixture.executions, err)
	}
	return waiting
}

func resolveDeferredApproval(
	t *testing.T,
	fixture deferredApprovalFixture,
	waiting kernel.Snapshot,
) kernel.Snapshot {
	t.Helper()
	resolved, err := fixture.runner.ResolveApproval(t.Context(), waiting.Run.ID, waiting.Run.Revision,
		plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
	if err != nil || resolved.Run.Status != kernel.RunStatusRunning || *fixture.executions != 0 {
		t.Fatalf("resolved=%#v executions=%d err=%v", resolved.Run, *fixture.executions, err)
	}
	return resolved
}

func assertDeferredApprovalCompleted(
	t *testing.T,
	fixture deferredApprovalFixture,
	completed kernel.Snapshot,
	err error,
) {
	t.Helper()
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted ||
		*fixture.executions != 1 || fixture.model.calls != 2 {
		t.Fatalf(
			"completed=%#v executions=%d calls=%d err=%v",
			completed.Run, *fixture.executions, fixture.model.calls, err,
		)
	}
}

type deltaThenFailureModel struct {
	streamCalls int
	unaryCalls  int
}

func (model *deltaThenFailureModel) Generate(context.Context, runtimemodel.Request) (runtimemodel.Response, error) {
	model.unaryCalls++
	return runtimemodel.Response{Content: "unexpected"}, nil
}

func (model *deltaThenFailureModel) GenerateStream(
	_ context.Context,
	_ runtimemodel.Request,
	onEvent func(runtimemodel.StreamEvent) error,
) (runtimemodel.Response, error) {
	model.streamCalls++
	if err := onEvent(runtimemodel.StreamEvent{Delta: "partial"}); err != nil {
		return runtimemodel.Response{}, err
	}
	return runtimemodel.Response{}, errDatabaseUnavailable
}

func TestRunnerDoesNotReplayModelAfterStreamDeltaFailure(t *testing.T) {
	t.Parallel()
	runtime, approvals := newTestRuntimeAndApprovals(t)
	feed := newAgentTestFeed(t)
	registry := mustRegistry(t, nil)
	model := &deltaThenFailureModel{}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: model, Catalog: registry, Executor: registry,
		Approvals: interactionadapter.New(approvals), Observers: []plugin.Observer{runfeedadapter.New(feed)},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runner.StartRun(t.Context(), startRequest("run_stream_failure", "request_stream_failure", "answer"))
	assertStreamFailureRun(t, snapshot, err, model)
	assertStreamFailureFeed(t, feed, snapshot.Run.ID)
}

func assertStreamFailureRun(
	t *testing.T,
	snapshot kernel.Snapshot,
	err error,
	model *deltaThenFailureModel,
) {
	t.Helper()
	if !errors.Is(err, agent.ErrModelFailure) || snapshot.Run.Status != kernel.RunStatusFailed {
		t.Fatalf("snapshot = %#v, error = %v", snapshot.Run, err)
	}
	if model.streamCalls != 1 || model.unaryCalls != 0 {
		t.Fatalf("model calls = stream %d, unary %d", model.streamCalls, model.unaryCalls)
	}
}

func assertStreamFailureFeed(t *testing.T, feed *runfeed.Feed, runID string) {
	t.Helper()
	events, replayErr := feed.Replay(t.Context(), runID, 0)
	if replayErr != nil {
		t.Fatal(replayErr)
	}
	assertAgentFeedTypes(t, feed, runID, []string{
		runfeed.EventRunStarted,
		runfeed.EventModelStarted,
		runfeed.EventModelDelta,
		runfeed.EventRunFailed,
	})
	var streamEvent runtimemodel.StreamEvent
	if len(events) != 4 || json.Unmarshal(events[2].Data, &streamEvent) != nil || streamEvent.Delta != "partial" || !events[3].Terminal {
		t.Fatalf("stream failure events = %#v", events)
	}
}

func newAgentTestFeed(t *testing.T) *runfeed.Feed {
	t.Helper()
	feed, err := runfeed.New(memory.NewRunFeedStore(), runfeed.Options{
		Retention: time.Minute, PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return feed
}

func assertAgentFeedTypes(t *testing.T, feed *runfeed.Feed, runID string, want []string) {
	t.Helper()
	events, err := feed.Replay(t.Context(), runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(events))
	for _, event := range events {
		got = append(got, event.Type)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("feed types = %#v, want %#v", got, want)
	}
}

type invalidTerminalBatchModel struct{}

func (invalidTerminalBatchModel) Generate(_ context.Context, _ runtimemodel.Request) (runtimemodel.Response, error) {
	return runtimemodel.Response{ToolCalls: []tools.Call{
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
				InputSchema: json.RawMessage(`{"type":"object"}`),
				Terminal:    true,
			},
			Handler: handler,
		},
		{
			Definition: tools.Definition{
				Key: manifestToolKey, Name: manifestToolName,
				InputSchema: json.RawMessage(`{"type":"object"}`),
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
	requests []runtimemodel.Request
}

func (model *correctionModel) Generate(
	_ context.Context,
	request runtimemodel.Request,
) (runtimemodel.Response, error) {
	model.requests = append(model.requests, request)
	switch len(model.requests) {
	case 1:
		return runtimemodel.Response{ToolCalls: []tools.Call{{
			ID: "call_bad", ToolKey: publishToolKey,
			Arguments: json.RawMessage(`{"unexpected":true}`),
		}}}, nil
	case 2:
		return correctionRetryResponse(model.t, request)
	case 3:
		return correctionCompletionResponse(model.t, request)
	default:
		model.t.Fatalf("unexpected correction model call %d", len(model.requests))
		return runtimemodel.Response{}, nil
	}
}

func correctionRetryResponse(t *testing.T, request runtimemodel.Request) (runtimemodel.Response, error) {
	t.Helper()
	if len(request.Messages) != 3 {
		t.Fatalf("correction transcript length = %d, want 3", len(request.Messages))
	}
	toolResult := request.Messages[2]
	if toolResult.Role != runtimemodel.RoleTool || toolResult.ToolCallID != "call_bad" {
		t.Fatalf("correction tool result = %#v", toolResult)
	}
	assertRecoverableCorrectionPayload(t, toolResult.Content)
	return runtimemodel.Response{ToolCalls: []tools.Call{{
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

func correctionCompletionResponse(t *testing.T, request runtimemodel.Request) (runtimemodel.Response, error) {
	t.Helper()
	if len(request.Messages) != 5 || request.Messages[4].Role != runtimemodel.RoleTool || request.Messages[4].ToolCallID != callGood {
		t.Fatalf("corrected transcript = %#v", request.Messages)
	}
	return runtimemodel.Response{Content: "completed after correction"}, nil
}

func TestRunnerLetsModelCorrectExplicitRecoverableToolError(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	toolCalls := 0
	registry := mustRegistry(t, []tools.Registration{{
		Definition: tools.Definition{
			Key: publishToolKey, Name: publishToolName,
			InputSchema: json.RawMessage(`{"type":"object"}`),
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

type blockedCorrectionModel struct {
	t     *testing.T
	calls int
}

func (model *blockedCorrectionModel) Generate(
	_ context.Context,
	request runtimemodel.Request,
) (runtimemodel.Response, error) {
	model.calls++
	switch model.calls {
	case 1:
		assertModelToolAvailability(
			model.t,
			request,
			[]string{manifestToolKey, selectionToolKey, publishToolKey},
			nil,
		)
		return runtimemodel.Response{ToolCalls: []tools.Call{{
			ID: "call_read_budget", ToolKey: manifestToolKey, Arguments: json.RawMessage(`{}`),
		}}}, nil
	case 2:
		assertModelToolAvailability(
			model.t,
			request,
			[]string{publishToolKey},
			[]string{manifestToolKey, selectionToolKey},
		)
		if !request.RequireToolCall {
			model.t.Fatal("recoverable Tool correction did not require the next Tool call")
		}
		return runtimemodel.Response{ToolCalls: []tools.Call{{
			ID: callGood, ToolKey: publishToolKey, Arguments: json.RawMessage(`{"title":"ready"}`),
		}}}, nil
	default:
		model.t.Fatalf("unexpected blocked correction model call %d", model.calls)
		return runtimemodel.Response{}, nil
	}
}

func assertModelToolAvailability(
	t *testing.T,
	request runtimemodel.Request,
	want []string,
	blocked []string,
) {
	t.Helper()
	available := make(map[string]struct{}, len(request.Tools))
	for _, definition := range request.Tools {
		available[definition.Key] = struct{}{}
	}
	for _, key := range want {
		if _, ok := available[key]; !ok {
			t.Fatalf("model Tool %q unavailable in %#v", key, request.Tools)
		}
	}
	for _, key := range blocked {
		if _, ok := available[key]; ok {
			t.Fatalf("blocked model Tool %q still available in %#v", key, request.Tools)
		}
	}
}

func TestRunnerStopsOfferingToolsBlockedByRecoverableError(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	readCalls := 0
	publishCalls := 0
	handler := tools.HandlerFunc(func(
		_ context.Context,
		request tools.ExecutionRequest,
	) (tools.ExecutionResult, error) {
		if request.Call.ToolKey != publishToolKey {
			readCalls++
			return tools.ExecutionResult{}, tools.NewRecoverableCallErrorWithBlockedTools(
				"story.read_budget_exhausted",
				"publish now",
				errDomainRejected,
				manifestToolKey,
				selectionToolKey,
			)
		}
		publishCalls++
		return tools.ExecutionResult{
			Content: json.RawMessage(`{"changeSetID":"change_ready"}`),
			Receipt: tools.Receipt{ExecutionID: "change_ready", Disposition: committedDisposition},
		}, nil
	})
	registry := mustRegistry(t, []tools.Registration{
		{
			Definition: tools.Definition{
				Key: manifestToolKey, Name: manifestToolName,
				InputSchema: json.RawMessage(`{"type":"object"}`),
			},
			Handler: handler,
		},
		{
			Definition: tools.Definition{
				Key: selectionToolKey, Name: selectionToolName,
				InputSchema: json.RawMessage(`{"type":"object"}`),
			},
			Handler: handler,
		},
		{
			Definition: tools.Definition{
				Key: publishToolKey, Name: publishToolName,
				InputSchema: json.RawMessage(`{"type":"object"}`), Terminal: true,
			},
			Handler: handler,
		},
	})
	model := &blockedCorrectionModel{t: t}
	runner := mustRunner(t, runtime, approvals, model, registry)
	snapshot, err := runner.StartRun(t.Context(), startRequest(
		"run_blocked_correction",
		"request_blocked_correction",
		"publish after the read budget",
		manifestToolKey,
		selectionToolKey,
		publishToolKey,
	))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Run.Status != kernel.RunStatusCompleted || model.calls != 2 || readCalls != 1 || publishCalls != 1 {
		t.Fatalf(
			"run = %#v, model calls = %d, read calls = %d, publish calls = %d",
			snapshot.Run,
			model.calls,
			readCalls,
			publishCalls,
		)
	}
}

type fatalToolModel struct{}

func (fatalToolModel) Generate(_ context.Context, _ runtimemodel.Request) (runtimemodel.Response, error) {
	return runtimemodel.Response{ToolCalls: []tools.Call{{
		ID: "call_fatal", ToolKey: publishToolKey, Arguments: json.RawMessage(`{}`),
	}}}, nil
}

func TestRunnerStillFailsUnmarkedToolErrors(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	registry := mustRegistry(t, []tools.Registration{{
		Definition: tools.Definition{
			Key: publishToolKey, Name: publishToolName,
			InputSchema: json.RawMessage(`{"type":"object"}`),
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
	request runtimemodel.Request,
) (runtimemodel.Response, error) {
	model.requests = append(model.requests, request)
	switch len(model.requests) {
	case 1:
		return transcriptInitialResponse(model.t, request)
	case 2:
		return transcriptCompletionResponse(model.t, request)
	default:
		model.t.Fatalf("unexpected model call %d", len(model.requests))
		return runtimemodel.Response{}, nil
	}
}

func transcriptInitialResponse(t *testing.T, request runtimemodel.Request) (runtimemodel.Response, error) {
	t.Helper()
	if len(request.Messages) != 1 || request.Messages[0].Role != runtimemodel.RoleUser {
		t.Fatalf("first transcript = %#v", request.Messages)
	}
	return runtimemodel.Response{
		Content: "I will inspect the frozen manifest and current units first.",
		ToolCalls: []tools.Call{
			{ID: callOne, ToolKey: manifestToolKey, Arguments: json.RawMessage(`{}`)},
			{ID: callTwo, ToolKey: manifestToolKey, Arguments: json.RawMessage(`{}`)},
		},
	}, nil
}

func transcriptCompletionResponse(t *testing.T, request runtimemodel.Request) (runtimemodel.Response, error) {
	t.Helper()
	if len(request.Messages) != 4 {
		t.Fatalf("second transcript length = %d, want 4", len(request.Messages))
	}
	assertAssistantToolBatch(t, request.Messages[1])
	assertOrderedToolResults(t, request.Messages[2:])
	return runtimemodel.Response{Content: "completed"}, nil
}

func assertAssistantToolBatch(t *testing.T, assistant runtimemodel.Message) {
	t.Helper()
	if assistant.Role != runtimemodel.RoleAssistant ||
		assistant.Content != "I will inspect the frozen manifest and current units first." ||
		len(assistant.ToolCalls) != 2 ||
		assistant.ToolCalls[0].ID != callOne || assistant.ToolCalls[0].ToolKey != manifestToolKey ||
		assistant.ToolCalls[1].ID != callTwo || assistant.ToolCalls[1].ToolKey != manifestToolKey {
		t.Fatalf("assistant tool turn = %#v", assistant)
	}
}

func assertOrderedToolResults(t *testing.T, messages []runtimemodel.Message) {
	t.Helper()
	for index, callID := range []string{callOne, callTwo} {
		toolResult := messages[index]
		if toolResult.Role != runtimemodel.RoleTool || toolResult.ToolCallID != callID ||
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
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
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
