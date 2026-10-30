package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type localExternalDelegationModel struct{ calls int }

type localWaitClock struct{ now time.Time }

func (clock *localWaitClock) Now() time.Time { return clock.now }

func (client *localExternalDelegationModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	client.calls++
	last := request.Messages[len(request.Messages)-1]
	if last.Role == model.RoleTool {
		return model.Response{Content: "child result received"}, nil
	}
	if last.Content == "compute child" {
		return model.Response{ToolCalls: []tools.Call{{ID: "child-external-call", ToolKey: "test.local_external", Arguments: json.RawMessage(`{}`)}}}, nil
	}
	return model.Response{ToolCalls: []tools.Call{{ID: "local-delegation-call", ToolKey: harness.DelegationToolKey, Arguments: json.RawMessage(`{"roleID":"researcher","goal":"compute child"}`)}}}, nil
}

func TestLocalDelegationExternalWaitDoesNotPollAndResumesBothCalls(t *testing.T) {
	clock := &localWaitClock{now: time.Now().UTC()}
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	client := &localExternalDelegationModel{}
	handler := harness.NewDelegationToolHandler()
	ready, executions := false, 0
	registry, err := tools.NewRegistry([]tools.Registration{harness.DelegationToolRegistration(handler), {
		Definition: tools.Definition{Key: "test.local_external", Name: "local_external", InputSchema: json.RawMessage(`{"type":"object"}`), Terminal: true},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			executions++
			if executions > 1 {
				return tools.ExecutionResult{}, errors.New("external Tool polled again instead of verifying provider COMMIT")
			}
			deadline := clock.Now().Add(2 * time.Minute)
			return tools.ExecutionResult{Content: json.RawMessage(`null`), Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: tools.ReceiptDispositionPending},
				Pending: &tools.PendingExecution{Kind: tools.PendingExternalResult, OperationID: "local-operation", InputDigest: strings.Repeat("e", 64), DeadlineAt: &deadline}}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: client, Catalog: registry, Executor: registry, Clock: clock,
		ToolOutcomeVerifier: agent.ToolOutcomeVerifierFunc(func(ctx context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			if request.Call.ToolKey == harness.DelegationToolKey {
				return handler.VerifyCommittedTool(ctx, request)
			}
			if request.Call.ToolKey != "test.local_external" || !ready ||
				request.Call.ID != "child-external-call" {
				return tools.ExecutionResult{}, errors.New("local external provider has no trusted COMMIT")
			}
			return tools.ExecutionResult{Content: json.RawMessage(`42`),
				Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := handoff.New(direct)
	if err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), delegationTestClock{})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(delegationHarnessDependencies(runtime, direct, store, registry, coordinator, relations, false))
	if err != nil {
		t.Fatal(err)
	}
	if err = handler.Bind(runner); err != nil {
		t.Fatal(err)
	}
	started, err := runner.StartAgentTurn(t.Context(), harness.StartRequest{
		HostThread: harness.HostRef{Kind: testThreadKind, ID: "local-external-thread"}, HostTurn: harness.HostRef{Kind: testContextHostKind, ID: "local-external-turn"},
		Actor: kernel.ActorRef{TenantID: testTenant, ActorID: testActor}, Thread: kernel.ThreadRef{Kind: testThreadKind, ID: "local-external-thread"}, Goal: "delegate calculation",
		Config: harness.ConfigSnapshot{
			Model: "fixture", ToolKeys: []string{harness.DelegationToolKey, "test.local_external"}, ToolPolicies: []harness.ToolPolicySnapshot{harness.DelegationToolPolicySnapshot()},
			Roles: []harness.RoleSnapshot{{ID: "researcher", Revision: 1, Name: "Researcher", Model: "fixture", ToolKeys: []string{"test.local_external"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rootID := snapshotExecutionRef(t, started)
	root, err := runtime.Load(t.Context(), rootID)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := direct.QueryPendingTool(t.Context(), rootID, "local-delegation-call")
	if err != nil || wait.Kind != tools.PendingChildTerminal {
		t.Fatalf("parent wait=%#v, %v", wait, err)
	}
	for range 60 {
		clock.now = clock.now.Add(time.Second)
		unchanged, resumeErr := direct.Resume(t.Context(), rootID, root.Run.Revision)
		if !errors.Is(resumeErr, agent.ErrToolOutcomeUnknown) || unchanged.Run.Revision != root.Run.Revision || executions != 1 || client.calls != 2 {
			t.Fatalf("parent polled: %#v %v executions=%d model=%d", unchanged.Run, resumeErr, executions, client.calls)
		}
	}
	ready = true
	child, err := direct.NotifyExternalToolResultReady(t.Context(), agent.ExternalToolResultSignal{RunID: wait.ChildRunID, CallID: "child-external-call", OperationID: "local-operation", InputDigest: strings.Repeat("e", 64), SignalID: "local-receipt"})
	if err != nil {
		t.Fatal(err)
	}
	verifiedChild, err := direct.ReconcileToolOutcome(t.Context(), child.Run.ID, child.Run.Revision, "child-external-call")
	if err != nil {
		t.Fatal(err)
	}
	child = verifiedChild
	if child.Run.Status != kernel.RunStatusCompleted {
		child, err = direct.Resume(t.Context(), verifiedChild.Run.ID, verifiedChild.Run.Revision)
	}
	if err != nil || child.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("child=%#v, %v", child.Run, err)
	}
	verifiedParent := reconcileDelegationRoot(t, direct, root)
	completed, err := direct.Resume(t.Context(), rootID, verifiedParent.Run.Revision)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted || client.calls != 3 || executions != 1 {
		t.Fatalf("parent=%#v, %v calls=%d/%d", completed.Run, err, client.calls, executions)
	}
	view, err := agent.ViewState(completed)
	if err != nil || view.Budget.Usage.ToolCalls != 1 {
		t.Fatalf("parent counted call twice: %#v, %v", view, err)
	}
}
