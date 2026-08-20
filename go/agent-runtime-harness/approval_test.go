package harness_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge/sdk/go/agent-runtime-harness"
	interactionadapter "github.com/orz-i/Gaoge/sdk/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/model"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

const (
	approvalTestToolKey           = "approval.lookup"
	approvalTestCapabilityPerCall = "per_call"
)

func TestHarnessApprovalUsesFrozenToolPolicyAndPersistsDecisionItems(t *testing.T) {
	t.Parallel()
	harnessRunner, config := newApprovalHarness(t)
	waiting := startApprovalHarness(t, harnessRunner, config)
	assertHarnessApprovalWait(t, waiting, 1)

	// Mutating the caller-owned Config after Start must not change the durable policy.
	config.ToolPolicies[0].ApprovalMode = "never"
	waitingAgain := resolveApproval(t, harnessRunner, waiting.Turn.ID, "first approved")
	assertHarnessApprovalWait(t, waitingAgain, 2)

	completed := resolveApproval(t, harnessRunner, waitingAgain.Turn.ID, "second approved")
	if completed.Turn.Status != harness.TurnCompleted || approvalItemCount(completed.Items, harness.ItemCompleted) != 2 {
		t.Fatalf("approval lifecycle did not complete: turn=%#v items=%#v", completed.Turn, completed.Items)
	}
}

func TestFrozenApprovalPolicyFollowsChildRunRelations(t *testing.T) {
	t.Parallel()
	store := harness.NewMemoryStore()
	relations, err := runrelation.New(memory.NewRunRelationStore(), approvalTestClock{})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := harness.NewFrozenApprovalPolicy(store, relations)
	if err != nil {
		t.Fatal(err)
	}
	now := approvalTestClock{}.Now()
	config, err := harness.SealConfigSnapshot("turn-descendant-approval", harness.ConfigSnapshot{
		ToolKeys: []string{approvalTestToolKey},
		ToolPolicies: []harness.ToolPolicySnapshot{{
			Key: approvalTestToolKey, DefinitionVersion: "v1",
			ApprovalCapability: approvalTestCapabilityPerCall, ApprovalMode: "always",
		}},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutConfigSnapshot(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CreateTurn(t.Context(), harness.Turn{
		ID: config.TurnID, SessionID: "session-descendant-approval",
		HostTurn:         harness.HostRef{Kind: testContextHostKind, ID: "host-descendant-approval"},
		ConfigSnapshotID: config.ID, Status: harness.TurnRunning, Revision: 1,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"goal":"plan"}`)
	inputHash := sha256.Sum256(input)
	if _, _, err = store.CreateInvocation(t.Context(), harness.Invocation{
		ID: "invocation-descendant-approval", TurnID: config.TurnID,
		CapabilityKey: harness.CapabilityPlanExecute, DefinitionVersion: harness.RuntimeCapabilityVersion,
		ExecutionClass: harness.ExecutionPlanExecute, Input: input, InputHash: fmt.Sprintf("%x", inputHash),
		ExecutionRefID: "plan-run", Status: harness.InvocationRunning, Attempt: 1, Revision: 1,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = relations.Ensure(t.Context(), runrelation.Draft{
		ParentRunID: "plan-run", ChildRunID: "step-agent-run",
		Kind: runrelation.KindPlanStep, OwnerNodeID: "step-1",
	}); err != nil {
		t.Fatal(err)
	}
	requirement, err := policy.Approval(t.Context(), plugin.ToolInvocation{
		Run:        kernel.Run{ID: "step-agent-run"},
		Definition: tools.Definition{Key: approvalTestToolKey},
	})
	if err != nil || requirement != plugin.ApprovalRequired {
		t.Fatalf("descendant approval requirement = %q, err=%v", requirement, err)
	}
}

func newApprovalHarness(t *testing.T) (*harness.Runner, harness.ConfigSnapshot) {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	policy, err := harness.NewFrozenApprovalPolicy(store)
	if err != nil {
		t.Fatal(err)
	}
	registry := newApprovalTestRegistry(t)
	approvals, err := interaction.New(runtime)
	if err != nil {
		t.Fatal(err)
	}
	agentRunner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: &approvalTestModel{}, Catalog: registry, Executor: registry,
		Approvals: interactionadapter.New(approvals), ApprovalPolicies: []plugin.ApprovalPolicy{policy},
		Limits: agent.Limits{MaxLLMCalls: 3, MaxToolCalls: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	harnessRunner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: agentRunner, Store: store, Clock: approvalTestClock{}, Catalog: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	return harnessRunner, harness.ConfigSnapshot{
		ToolKeys: []string{approvalTestToolKey},
		ToolPolicies: []harness.ToolPolicySnapshot{{
			Key: approvalTestToolKey, DefinitionVersion: "v1",
			ApprovalCapability: approvalTestCapabilityPerCall, ApprovalMode: "always",
		}},
	}
}

func startApprovalHarness(t *testing.T, runner *harness.Runner, config harness.ConfigSnapshot) harness.Snapshot {
	t.Helper()
	waiting, err := runner.Start(t.Context(), harness.StartRequest{
		HostThread: harness.HostRef{Kind: testThreadKind, ID: "thread-approval"},
		HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "turn-approval"},
		Actor:      kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "thread-approval"},
		Goal:       "lookup twice", Config: config,
	})
	if err != nil {
		t.Fatalf("start approval Harness: %v", err)
	}
	return waiting
}

func resolveApproval(t *testing.T, runner *harness.Runner, turnID string, comment string) harness.Snapshot {
	t.Helper()
	snapshot, err := runner.ResolveApproval(t.Context(), turnID, harness.ResolveApprovalRequest{
		Decision: harness.ApprovalApprove, Comment: comment,
	})
	if err != nil {
		t.Fatalf("resolve approval: %v", err)
	}
	return snapshot
}

func assertHarnessApprovalWait(t *testing.T, snapshot harness.Snapshot, wantWaiting int) {
	t.Helper()
	invocation, ok := harness.TopLevelInvocation(snapshot)
	if snapshot.Turn.Status != harness.TurnWaitingInput || !ok || invocation.ExecutionRefID == "" {
		t.Fatalf("expected durable approval wait, got %#v", snapshot.Turn)
	}
	if got := approvalItemCount(snapshot.Items, harness.ItemWaiting); got != wantWaiting {
		t.Fatalf("waiting approval item count=%d want=%d items=%#v", got, wantWaiting, snapshot.Items)
	}
}

func approvalItemCount(items []harness.Item, status harness.ItemStatus) int {
	count := 0
	for _, item := range items {
		if item.Kind == harness.ItemApproval && item.Status == status {
			count++
		}
	}
	return count
}

func newApprovalTestRegistry(t *testing.T) *tools.Registry {
	t.Helper()
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{
			Key: approvalTestToolKey, Name: "Approval Lookup", InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			return tools.ExecutionResult{
				Content: json.RawMessage(`{"value":"ok"}`),
				Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"},
			}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

type approvalTestModel struct{ calls int }

func (client *approvalTestModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	client.calls++
	if client.calls <= 2 {
		if client.calls == 2 {
			last := request.Messages[len(request.Messages)-1]
			if last.Role != model.RoleTool || last.Content != `{"value":"ok"}` {
				return model.Response{}, agent.ErrInvalidModelResponse
			}
		}
		return model.Response{ToolCalls: []tools.Call{{
			ToolKey: approvalTestToolKey, Arguments: json.RawMessage(`{"query":"value"}`),
		}}}, nil
	}
	return model.Response{Content: "approved answer"}, nil
}

type approvalTestClock struct{}

func (approvalTestClock) Now() time.Time {
	return time.Date(2026, 8, 15, 6, 0, 0, 0, time.UTC)
}
