package agent_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	runtimemodel "github.com/orz-i/Gaoge/sdk/go/agent-runtime/model"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

const pendingToolKey = "test.pending"

func TestPendingToolReceiptYieldsWithoutConsumingCallAndResumesIdempotently(t *testing.T) {
	fixture := newPendingToolFixture(t)
	started, err := fixture.runner.StartRun(
		t.Context(), startRequest("run_pending_tool", "request_pending_tool", "use the pending tool", pendingToolKey),
	)
	if err != nil {
		t.Fatal(err)
	}
	assertPendingToolYield(t, started, fixture)

	completed, err := fixture.runner.Resume(t.Context(), started.Run.ID, started.Run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	assertPendingToolCompletion(t, completed, fixture)
}

type pendingToolFixture struct {
	runner  *agent.Runner
	model   *pendingToolModel
	handler *pendingToolHandler
}

func newPendingToolFixture(t *testing.T) pendingToolFixture {
	t.Helper()
	runtime, approvals := newTestRuntimeAndApprovals(t)
	handler := &pendingToolHandler{}
	registry := mustRegistry(t, []tools.Registration{{
		Definition: tools.Definition{
			Key: pendingToolKey, Name: "pending_tool", InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		Handler: handler,
	}})
	model := &pendingToolModel{t: t}
	return pendingToolFixture{runner: mustRunner(t, runtime, approvals, model, registry), model: model, handler: handler}
}

func assertPendingToolYield(t *testing.T, snapshot kernel.Snapshot, fixture pendingToolFixture) {
	t.Helper()
	if snapshot.Run.Status != kernel.RunStatusRunning || fixture.handler.executions != 1 || fixture.model.calls != 1 {
		t.Fatalf("pending start = %#v executions=%d modelCalls=%d", snapshot, fixture.handler.executions, fixture.model.calls)
	}
}

func assertPendingToolCompletion(t *testing.T, snapshot kernel.Snapshot, fixture pendingToolFixture) {
	t.Helper()
	if snapshot.Run.Status != kernel.RunStatusCompleted || fixture.handler.executions != 2 || fixture.model.calls != 2 || snapshot.Result == nil {
		t.Fatalf("resumed completion = %#v executions=%d modelCalls=%d", snapshot, fixture.handler.executions, fixture.model.calls)
	}
}

type pendingToolHandler struct{ executions int }

func (handler *pendingToolHandler) Execute(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
	handler.executions++
	if handler.executions == 1 {
		return tools.ExecutionResult{
			Content: json.RawMessage(`{"status":"pending"}`),
			Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: tools.ReceiptDispositionPending},
		}, nil
	}
	return tools.ExecutionResult{
		Content: json.RawMessage(`{"ok":true}`),
		Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"},
	}, nil
}

type pendingToolModel struct {
	t     *testing.T
	calls int
}

func (model *pendingToolModel) Generate(_ context.Context, request runtimemodel.Request) (runtimemodel.Response, error) {
	model.calls++
	if model.calls == 1 {
		return runtimemodel.Response{ToolCalls: []tools.Call{{
			ID: "call_pending", ToolKey: pendingToolKey, Arguments: json.RawMessage(`{}`),
		}}}, nil
	}
	if len(request.Messages) == 0 {
		model.t.Fatal("resumed request has no transcript")
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role != runtimemodel.RoleTool || last.ToolCallID != "call_pending" || last.Content != `{"ok":true}` {
		model.t.Fatalf("resumed Tool transcript = %#v", last)
	}
	return runtimemodel.Response{Content: "done"}, nil
}

var _ runtimemodel.Client = (*pendingToolModel)(nil)
var _ tools.Handler = (*pendingToolHandler)(nil)
