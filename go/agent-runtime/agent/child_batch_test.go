package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestChildBatchStartsAllChildrenBeforeWaitingAndSurvivesRestart(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	children := map[string]kernel.Snapshot{}
	starts := 0
	handler := tools.HandlerFunc(func(ctx context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
		child, found := children[request.Call.ID]
		if !found {
			var err error
			child, err = runtime.Create(ctx, kernel.CreateRequest{
				ID: "child_" + request.Call.ID, Kind: "test.batch-child", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
				Thread: kernel.ThreadRef{Kind: "test", ID: "batch"}, Goal: "research", State: json.RawMessage(`{}`),
			})
			if err != nil {
				return tools.ExecutionResult{}, err
			}
			children[request.Call.ID] = child
			starts++
		}
		result := tools.ExecutionResult{Content: json.RawMessage(fmt.Sprintf(`{"status":%q}`, child.Run.Status)), Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}
		if child.Run.Status == kernel.RunStatusRunning {
			result.Receipt.Disposition = tools.ReceiptDispositionPending
			result.Pending = &tools.PendingExecution{Kind: tools.PendingChildTerminal, OperationID: request.Call.ID, ChildRunID: child.Run.ID}
		}
		return result, nil
	})
	registry := mustRegistry(t, []tools.Registration{{Definition: tools.Definition{Key: pendingToolKey, Name: "batch_children", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: handler}})
	client := &childBatchModel{t: t}
	runner := mustRunner(t, runtime, approvals, client, registry)
	started, err := runner.StartRun(t.Context(), startRequest("batch_root", "batch_request", "research concurrently", pendingToolKey))
	if err != nil || started.Run.Status != kernel.RunStatusRunning || starts != 3 {
		t.Fatalf("all children must be prepared before the first join: starts=%d status=%s err=%v", starts, started.Run.Status, err)
	}
	// Recreate the parent runner while all three children are still in flight.
	runner = mustRunner(t, runtime, approvals, client, registry)
	for callID, child := range children {
		status := kernel.RunStatusCompleted
		code := ""
		result := &kernel.Result{ContentType: "application/json", Content: json.RawMessage(`{"ok":true}`)}
		if callID == "batch_1" {
			status = kernel.RunStatusFailed
			code = "test.child_failure"
			result = nil
		}
		children[callID], err = runtime.Apply(t.Context(), child.Run.ID, child.Run.Revision, kernel.Mutation{Status: status, State: child.State, ErrorCode: code, Result: result})
		if err != nil {
			t.Fatal(err)
		}
	}
	completed, err := runner.Resume(t.Context(), started.Run.ID, started.Run.Revision)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted || starts != 3 {
		t.Fatalf("batch did not join after restart: starts=%d status=%s err=%v", starts, completed.Run.Status, err)
	}
	view, err := agent.ViewState(completed)
	if err != nil || view.Budget.Usage.ToolCalls != 3 {
		t.Fatalf("receipts must be charged once: %#v %v", view.Budget, err)
	}
}

type childBatchModel struct {
	t     *testing.T
	calls int
}

func (client *childBatchModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	client.calls++
	if client.calls == 1 {
		calls := make([]tools.Call, 3)
		for i := range calls {
			calls[i] = tools.Call{ID: fmt.Sprintf("batch_%d", i), ToolKey: pendingToolKey, Arguments: json.RawMessage(`{}`)}
		}
		return model.Response{ToolCalls: calls}, nil
	}
	results, failed := 0, false
	for _, message := range request.Messages {
		if message.Role == model.RoleTool {
			results++
			failed = failed || message.Content == `{"status":"failed"}`
		}
	}
	if results != 3 || !failed {
		client.t.Fatalf("parent lost child receipts: results=%d failed=%t", results, failed)
	}
	return model.Response{Content: "parent completed despite child failure"}, nil
}
