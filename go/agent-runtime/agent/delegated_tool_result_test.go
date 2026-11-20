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

type readToolApproval struct{}

func (readToolApproval) Name() string { return "test-authorize-read" }
func (readToolApproval) Approval(context.Context, plugin.ToolInvocation) (plugin.ApprovalRequirement, error) {
	return plugin.ApprovalNotRequired, nil
}

func TestDeferredToolResultRequiresOriginalCallbackAndOwnerReceipt(t *testing.T) {
	for _, tc := range []struct {
		name       string
		witness    bool
		checkpoint bool
	}{{"original_receipt", true, false}, {"opaque_history", false, false}, {"approved_checkpoint", true, true}, {"opaque_checkpoint", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			rt, approvals := newTestRuntimeAndApprovals(t)
			result := tools.ExecutionResult{Content: json.RawMessage(`{"answer":42}`), Receipt: tools.Receipt{ExecutionID: "call_publish", Disposition: "committed", OwnerReceiptDigest: kernel.ManagedDigest("original-owner-receipt")}}
			executed := 0
			registry := mustRegistry(t, []tools.Registration{{Definition: tools.Definition{Key: publishToolKey, Name: publishToolName, InputSchema: json.RawMessage(`{"type":"object"}`), Terminal: true}, Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
				executed++
				return tools.CloneExecutionResult(result), nil
			})}})
			input := startRequest("delegated-owner-read", "original", "read", publishToolKey)
			input.Model = "fixture"
			available := false
			readBack := tools.CloneExecutionResult(result)
			reader := agent.ToolOutcomeVerifierFunc(func(_ context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
				if r.RunID != input.ID || r.Call.ToolKey != publishToolKey || r.Call.ID != result.Receipt.ExecutionID || string(r.Call.Arguments) != `{"title":"ready"}` {
					t.Fatalf("different original call: %#v", r)
				}
				if !available {
					return tools.ExecutionResult{}, agent.ErrToolOutcomeUnknown
				}
				return tools.CloneExecutionResult(readBack), nil
			})
			deps := agent.Dependencies{Runtime: rt, Model: &terminalToolModel{t: t}, Catalog: registry, Executor: registry, ApprovalPolicies: []plugin.ApprovalPolicy{readToolApproval{}}}
			if tc.checkpoint {
				deps.Approvals = interactionadapter.New(approvals)
				deps.ApprovalPolicies = []plugin.ApprovalPolicy{requiredApprovalPolicy{name: "approve-original-call"}}
			}
			if tc.witness {
				deps.DelegatedToolResults = reader
			}
			original, err := agent.NewRunner(deps)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := original.PrepareRun(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			done, err := original.Resume(t.Context(), prepared.Run.ID, prepared.Run.Revision)
			if tc.checkpoint {
				if err != nil || done.Run.Status != kernel.RunStatusWaitingInput || done.Checkpoint == nil || executed != 0 {
					t.Fatalf("before approval: status=%s executions=%d err=%v", done.Run.Status, executed, err)
				}
				// A replacement worker can consume only the exact persisted approval.
				deps.DelegatedToolResults = reader
				restarted, err := agent.NewRunner(deps)
				if err != nil {
					t.Fatal(err)
				}
				done, err = restarted.ResolveApproval(t.Context(), done.Run.ID, done.Run.Revision, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
				if err != nil {
					t.Fatal(err)
				}
				if done.Run.Status == kernel.RunStatusRunning {
					done, err = restarted.Resume(t.Context(), done.Run.ID, done.Run.Revision)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if err != nil || done.Run.Status != kernel.RunStatusCompleted {
				t.Fatalf("child %s: %v", done.Run.Status, err)
			}
			deps.DelegatedToolResults = reader
			observer, err := agent.NewRunner(deps)
			if err != nil {
				t.Fatal(err)
			}
			if observer.VerifyDeferredToolResults(t.Context(), rt, done) {
				t.Fatal("missing owner receipt was accepted")
			}
			available = true
			readBack.Content = json.RawMessage(`{"answer":0}`)
			if observer.VerifyDeferredToolResults(t.Context(), rt, done) {
				t.Fatal("changed owner receipt was accepted")
			}
			readBack = result
			readBack.Receipt.OwnerReceiptDigest = kernel.ManagedDigest("other-owner-receipt")
			if observer.VerifyDeferredToolResults(t.Context(), rt, done) {
				t.Fatal("different owner receipt supplied the same result")
			}
			readBack = result
			if got := observer.VerifyDeferredToolResults(t.Context(), rt, done); got != tc.witness {
				t.Fatalf("observed=%t original witness=%t", got, tc.witness)
			}
			if !errors.Is(rt.VerifyOwnedRootSettlement(t.Context(), done), kernel.ErrExecutionConflict) || observer.VerifiedDeferredRootControl(t.Context(), rt, done) {
				t.Fatal("result observation supplied physical custody")
			}
			if executed != 1 {
				t.Fatalf("tool executions=%d", executed)
			}
		})
	}
}
