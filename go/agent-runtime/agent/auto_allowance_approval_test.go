package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestAutoAllowanceNeverOverridesToolApprovalOrCancellation(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	model := &allowanceModel{batches: [][]tools.Call{{allowanceTool("needs-human")}}}
	executed := 0
	registry := mustRegistry(t, []tools.Registration{{
		Definition: tools.Definition{
			Key: manifestToolKey, Name: manifestToolName,
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
			executed++
			return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`),
				Receipt: tools.Receipt{ExecutionID: "approved", Disposition: "committed"}}, nil
		}),
	}})
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: model, Catalog: registry, Executor: registry,
		Approvals:        interactionadapter.New(approvals),
		ApprovalPolicies: []plugin.ApprovalPolicy{requiredApprovalPolicy{name: "human"}},
		Limits:           agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest("auto-approval", "auto-approval-request", "read with approval", manifestToolKey)
	request.CallAllowance = &agent.CallAllowance{LLMCalls: 1, ToolCalls: 1}
	request.AutoAllowance = &agent.AutoAllowancePolicy{
		ModelStep: 1, ToolStep: 1,
		MaxModelRenewals: 2, MaxToolRenewals: 2, MaxNoProgressWindow: 1,
	}
	waiting, err := runner.StartRun(t.Context(), request)
	if err != nil || waiting.Run.Status != kernel.RunStatusWaitingInput ||
		executed != 0 || model.calls != 1 {
		t.Fatalf("approval unexpectedly bypassed by automatic renewals: %#v calls=%d Tool=%d err=%v",
			waiting.Run, model.calls, executed, err)
	}
	view, err := agent.ViewState(waiting)
	if err != nil || view.AutoAllowance.ModelRenewals != 0 || view.AutoAllowance.ToolRenewals != 0 {
		t.Fatalf("approval consumed autonomous budget unexpectedly: %#v err=%v", view, err)
	}
	if _, err = runner.GrantCallAllowance(t.Context(), waiting.Run.ID, waiting.Run.Revision,
		agent.CallAllowance{LLMCalls: 1}); !errors.Is(err, agent.ErrRunNotBudgetPaused) {
		t.Fatalf("waiting approval cannot be converted to automatic continuation: %v", err)
	}
	cancelled, err := runtime.Cancel(t.Context(), waiting.Run.ID, waiting.Run.Revision, "cancelled by owner")
	if err != nil || cancelled.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("cancel approval: %#v err=%v", cancelled.Run, err)
	}
	if _, err = runner.GrantCallAllowance(t.Context(), cancelled.Run.ID, cancelled.Run.Revision,
		agent.CallAllowance{LLMCalls: 1}); !errors.Is(err, agent.ErrRunNotBudgetPaused) {
		t.Fatalf("cancelled Run must not be revived: %v", err)
	}
	if executed != 0 {
		t.Fatalf("cancelled Tool must not execute: %d", executed)
	}
}
