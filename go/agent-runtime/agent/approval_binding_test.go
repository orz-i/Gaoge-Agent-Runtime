package agent_test

import (
	"context"
	"encoding/json"
	"testing"

	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	runtimemodel "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type batchApprovalModel struct{ calls int }

func (model *batchApprovalModel) Generate(context.Context, runtimemodel.Request) (runtimemodel.Response, error) {
	model.calls++
	if model.calls == 1 {
		return runtimemodel.Response{ToolCalls: []tools.Call{
			{ID: "batch-first", ToolKey: publishToolKey, Arguments: json.RawMessage(`{"order":1}`)},
			{ID: "batch-second", ToolKey: publishToolKey, Arguments: json.RawMessage(`{"order":2}`)},
		}}, nil
	}
	return runtimemodel.Response{Content: "batch finished"}, nil
}

type countingApprovalPolicy struct{ checks *int }

func (countingApprovalPolicy) Name() string { return "counted" }
func (policy countingApprovalPolicy) Approval(context.Context, plugin.ToolInvocation) (plugin.ApprovalRequirement, error) {
	*policy.checks++
	return plugin.ApprovalRequired, nil
}

// A decision for one Tool call cannot silently authorize another call in the same batch.
func TestDeferredBatchApprovalMustBeResolvedIndividually(t *testing.T) {
	for _, firstDecision := range []plugin.ApprovalDecision{plugin.ApprovalApprove, plugin.ApprovalReject} {
		t.Run(string(firstDecision), func(t *testing.T) {
			runtime, approvals := newTestRuntimeAndApprovals(t)
			checks := 0
			executed := []string{}
			registry := mustRegistry(t, []tools.Registration{{
				Definition: tools.Definition{
					Key: publishToolKey, Name: publishToolName,
					InputSchema: json.RawMessage(`{"type":"object"}`),
				},
				Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
					executed = append(executed, request.Call.ID)
					return tools.ExecutionResult{
						Content: json.RawMessage(`{"ok":true}`),
						Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: committedDisposition},
					}, nil
				}),
			}})
			model := &batchApprovalModel{}
			runner, err := agent.NewRunner(agent.Dependencies{
				Runtime: runtime, Model: model, Catalog: registry, Executor: registry,
				Approvals:        interactionadapter.New(approvals),
				ApprovalPolicies: []plugin.ApprovalPolicy{countingApprovalPolicy{checks: &checks}},
				DeferResumption:  true,
			})
			if err != nil {
				t.Fatal(err)
			}
			waiting, err := runner.StartRun(t.Context(), startRequest(
				"batch_"+string(firstDecision), "req_"+string(firstDecision), "two approvals", publishToolKey,
			))
			if err != nil || waiting.Run.Status != kernel.RunStatusWaitingInput || checks != 1 || len(executed) != 0 {
				t.Fatalf("first approval: run=%+v checks=%d executed=%v err=%v", waiting.Run, checks, executed, err)
			}
			resolved, err := runner.ResolveApproval(t.Context(), waiting.Run.ID, waiting.Run.Revision, plugin.ApprovalResponse{Decision: firstDecision})
			if err != nil || resolved.Run.Status != kernel.RunStatusRunning || len(executed) != 0 {
				t.Fatalf("first decision: run=%+v executed=%v err=%v", resolved.Run, executed, err)
			}
			secondWaiting, err := runner.Resume(t.Context(), resolved.Run.ID, resolved.Run.Revision)
			firstExecutions := 0
			if firstDecision == plugin.ApprovalApprove {
				firstExecutions = 1
			}
			if err != nil || secondWaiting.Run.Status != kernel.RunStatusWaitingInput ||
				checks != 2 || len(executed) != firstExecutions {
				t.Fatalf("second approval bypassed: run=%+v checks=%d executed=%v err=%v", secondWaiting.Run, checks, executed, err)
			}
			if firstExecutions == 1 && executed[0] != "batch-first" {
				t.Fatalf("first approval authorized wrong call: %v", executed)
			}
			secondResolved, err := runner.ResolveApproval(t.Context(), secondWaiting.Run.ID, secondWaiting.Run.Revision,
				plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
			if err != nil || secondResolved.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("second decision: run=%+v err=%v", secondResolved.Run, err)
			}
			finished, err := runner.Resume(t.Context(), secondResolved.Run.ID, secondResolved.Run.Revision)
			if err != nil || finished.Run.Status != kernel.RunStatusCompleted ||
				len(executed) != firstExecutions+1 || executed[len(executed)-1] != "batch-second" ||
				model.calls != 2 || finished.Checkpoint != nil {
				t.Fatalf("approved batch: run=%+v checkpoint=%+v executed=%v model=%d err=%v",
					finished.Run, finished.Checkpoint, executed, model.calls, err)
			}
		})
	}
}
