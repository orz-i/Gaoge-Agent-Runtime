package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	runtimemodel "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const pendingToolKey = "test.pending"

func TestPendingToolReceiptRequiresVerifiedCOMMITInsteadOfReexecution(t *testing.T) {
	fixture := newPendingToolFixture(t)
	started, err := fixture.runner.StartRun(
		t.Context(), startRequest("run_pending_tool", "request_pending_tool", "use the pending tool", pendingToolKey),
	)
	if err != nil {
		t.Fatal(err)
	}
	assertPendingToolYield(t, started, fixture)
	_, err = fixture.runner.Resume(t.Context(), started.Run.ID, started.Run.Revision)
	if !errors.Is(err, agent.ErrToolOutcomeUnknown) || fixture.handler.executions != 1 {
		t.Fatalf("pending Tool was reexecuted without provider receipt: err=%v effects=%d", err, fixture.handler.executions)
	}
	fixture.handler.ready = true // Verified outcome exists in independent provider store.
	reconciled, err := fixture.runner.ReconcileToolOutcome(t.Context(), started.Run.ID, started.Run.Revision, "call_pending")
	if err != nil || reconciled.Run.Revision <= started.Run.Revision {
		t.Fatalf("exact read-only COMMIT was not accepted: run=%+v err=%v", reconciled.Run, err)
	}
	completed, err := fixture.runner.Resume(t.Context(), started.Run.ID, reconciled.Run.Revision)
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
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: model, Catalog: registry, Executor: registry,
		Approvals: interactionadapter.New(approvals),
		ToolOutcomeVerifier: agent.ToolOutcomeVerifierFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			handler.verifiedReads++
			if !handler.ready {
				return tools.ExecutionResult{}, errors.New("provider does not yet confirm terminal Tool outcome")
			}
			return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`),
				Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return pendingToolFixture{runner: runner, model: model, handler: handler}
}

func assertPendingToolYield(t *testing.T, snapshot kernel.Snapshot, fixture pendingToolFixture) {
	t.Helper()
	if snapshot.Run.Status != kernel.RunStatusRunning || fixture.handler.executions != 1 || fixture.model.calls != 1 {
		t.Fatalf("pending start = %#v executions=%d modelCalls=%d", snapshot, fixture.handler.executions, fixture.model.calls)
	}
}

func assertPendingToolCompletion(t *testing.T, snapshot kernel.Snapshot, fixture pendingToolFixture) {
	t.Helper()
	if snapshot.Run.Status != kernel.RunStatusCompleted || fixture.handler.executions != 1 || fixture.handler.verifiedReads != 1 || fixture.model.calls != 2 || snapshot.Result == nil {
		t.Fatalf("resumed completion = %#v executions=%d modelCalls=%d", snapshot, fixture.handler.executions, fixture.model.calls)
	}
}

type pendingToolHandler struct {
	executions, verifiedReads int
	ready                     bool
}

func (handler *pendingToolHandler) Execute(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
	handler.executions++
	if handler.executions == 1 {
		return tools.ExecutionResult{
			Content: json.RawMessage(`{"status":"pending"}`),
			Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: tools.ReceiptDispositionPending},
		}, nil
	}
	return tools.ExecutionResult{}, errors.New("pending Tool was physically retried instead of verified")
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
