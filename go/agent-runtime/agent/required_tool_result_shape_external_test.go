package agent_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestRequiredListToolResultCompletesWithoutWastingModelBudget(t *testing.T) {
	rt, _ := newTestRuntimeAndApprovals(t)
	registry := progressRegistry(t, tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
		return tools.ExecutionResult{Content: json.RawMessage(`["D-01","D-02"]`),
			Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}, nil
	}))
	calls := 0
	client := progressModelFunc(func(context.Context, model.Request) (model.Response, error) {
		calls++
		if calls == 1 {
			return model.Response{ToolCalls: []tools.Call{{ID: "list-1", ToolKey: "lookup", Arguments: json.RawMessage(`{}`)}}}, nil
		}
		if calls > 2 {
			t.Error("valid list result did not satisfy the already-executed required Tool")
		}
		return model.Response{Content: "Two original document IDs were read."}, nil
	})
	loop, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: client, Catalog: registry, Executor: registry})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest("required-list", "request", "List document IDs", "lookup")
	request.RequiredToolKeys = []string{"lookup"}
	result, err := loop.StartRun(t.Context(), request)
	if err != nil || result.Run.Status != kernel.RunStatusCompleted || calls != 2 {
		t.Fatalf("required list Tool did not complete normally: status=%s calls=%d err=%v", result.Run.Status, calls, err)
	}
}
