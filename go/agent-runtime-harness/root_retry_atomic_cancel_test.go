package harness_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

var (
	errRootRetryCommittedAckLost = errors.New("test original Retry Store response lost after commit")
	errRootRetryCancelAckLost    = errors.New("test unstarted Retry cancellation Store response lost after commit")
)

// Original Store itself performs the atomic budget+Invocation transition,
// and the adapter discards only the ACK. Restarted callers must never
// reconstruct a second Attempt or execute an unadmitted external effect.
type rootRetryLostACKStore struct {
	harness.Store
	budget.LedgerStore
	failOnce      bool
	cancelAckOnce bool
}

func (s *rootRetryLostACKStore) CancelUndispatchedRoot(ctx context.Context, inv harness.Invocation, turn harness.Turn) (harness.Invocation, harness.Turn, error) {
	updated, terminal, err := s.Store.CancelUndispatchedRoot(ctx, inv, turn)
	if err == nil && s.cancelAckOnce {
		s.cancelAckOnce = false
		return harness.Invocation{}, harness.Turn{}, errRootRetryCancelAckLost
	}
	return updated, terminal, err
}

func (s *rootRetryLostACKStore) RetryInvocation(
	ctx context.Context, id string, revision uint64, next string, now time.Time,
) (harness.Invocation, error) {
	saved, err := s.Store.RetryInvocation(ctx, id, revision, next, now)
	if err == nil && s.failOnce {
		s.failOnce = false
		return harness.Invocation{}, errRootRetryCommittedAckLost
	}
	return saved, err
}

func TestRootRetryCommittedAckLossCancelsUnstartedAttemptAndRecovers(t *testing.T) {
	testRootRetryCommittedAckLoss(t, false)
}

func TestRootRetryCommittedAckLossWithHistoricalTerminalChild(t *testing.T) {
	testRootRetryCommittedAckLoss(t, true)
}

func testRootRetryCommittedAckLoss(t *testing.T, historicalTerminalChild bool) {
	t.Helper()
	raw := harness.NewMemoryStore()
	store := &rootRetryLostACKStore{Store: raw, LedgerStore: raw, failOnce: true}
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &roleBudgetModel{}
	shared, err := harness.NewBudgetMiddleware(harness.BudgetMiddlewareDependencies{
		Store: store, Ledgers: store, Relations: relations, Meter: client, Clock: fixedClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	bounded, err := harness.NewBoundedBudgetAdapter(harness.BudgetMiddlewareDependencies{
		Store: store, Ledgers: store, Relations: relations, Meter: client, Clock: fixedClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: roleEvidenceTool, Name: "read_evidence", InputSchema: []byte("{\"type\":\"object\"}")},
		Handler:    roleEvidenceHandler{},
	}})
	if err != nil {
		t.Fatal(err)
	}
	policies, err := harness.NewFrozenApprovalPolicy(store, relations)
	if err != nil {
		t.Fatal(err)
	}
	approvals, err := interaction.New(rt)
	if err != nil {
		t.Fatal(err)
	}
	original, err := agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: client, Catalog: registry, Executor: registry,
		Approvals: interactionadapter.New(approvals), ApprovalPolicies: []plugin.ApprovalPolicy{policies},
		RunMiddleware: []plugin.RunMiddleware{bounded}, ModelMiddleware: []plugin.ModelMiddleware{bounded},
		ToolMiddleware: []plugin.ToolMiddleware{bounded},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: original, Store: store, Clock: fixedClock{}, Budget: shared, Relations: relations})
	if err != nil {
		t.Fatal(err)
	}
	request := testStartRequest()
	request.Config.Model = "specialist-model"
	request.Config.MaxModelOutputTokens = 256
	request.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 4, MaxTotalTokens: 3000}
	request.Config.ToolKeys = []string{roleEvidenceTool}
	request.Config.ToolPolicies = []harness.ToolPolicySnapshot{{
		Key: roleEvidenceTool, DefinitionVersion: "v1", ExecutionMode: "local_dispatch",
		ApprovalCapability: "per_call", ApprovalMode: "always",
	}}
	first, err := runner.StartAgentTurn(t.Context(), request)
	if err != nil || first.Turn.Status != harness.TurnWaitingInput {
		t.Fatalf("original input wait: %+v %v", first.Turn, err)
	}
	if historicalTerminalChild {
		rootBefore, rootExists := harness.TopLevelInvocation(first)
		if !rootExists {
			t.Fatal("missing original root")
		}
		childInput := []byte(`{"goal":"finished during original Attempt"}`)
		digest := sha256.Sum256(childInput)
		now := fixedClock{}.Now()
		child := harness.Invocation{
			ID: "old-terminal-child", TurnID: rootBefore.TurnID,
			ParentItemID: "original-parent-item", CapabilityKey: harness.CapabilityAgent,
			ExecutionClass: harness.ExecutionAgent, ExecutionRefID: "old-child-run",
			Input: childInput, InputHash: hex.EncodeToString(digest[:]),
			Status: harness.InvocationCompleted, Attempt: 1, Revision: 1,
			CreatedAt: now, UpdatedAt: now, OutputRefs: []harness.HostRef{},
		}
		if _, fresh, createErr := raw.CreateInvocation(t.Context(), child); createErr != nil || !fresh {
			t.Fatalf("historical terminal child: %v fresh=%t", createErr, fresh)
		}
	}
	cancelled, err := runner.Cancel(t.Context(), first.Turn.ID, "stop before tool")
	if err != nil || cancelled.Turn.Status != harness.TurnCancelled {
		t.Fatalf("original Cancel: %+v %v", cancelled.Turn, err)
	}
	root, ok := harness.TopLevelInvocation(cancelled)
	if !ok {
		t.Fatal("root invocation missing")
	}
	_, err = runner.RetryInvocation(t.Context(), root.ID)
	if !errors.Is(err, errRootRetryCommittedAckLost) {
		t.Fatalf("no committed-ACK loss: %v", err)
	}
	inv, err := store.GetInvocation(t.Context(), root.ID)
	if err != nil || inv.Attempt != 2 || inv.Status != harness.InvocationAccepted {
		t.Fatalf("rotated Invocation missing: %+v %v", inv, err)
	}
	ledger, err := store.LoadBudget(t.Context(), first.Turn.ID)
	if err != nil || !ledger.RootRetries[inv.ExecutionRefID].Active {
		t.Fatalf("atomic replacement budget absent: %+v %v", ledger.RootRetries, err)
	}
	if _, err = rt.Load(t.Context(), inv.ExecutionRefID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("physical effect started before ACK: %v", err)
	}
	store.cancelAckOnce = true
	_, err = runner.Cancel(t.Context(), first.Turn.ID, "stop unstarted new Attempt")
	if !errors.Is(err, errRootRetryCancelAckLost) {
		t.Fatalf("atomic cancellation must report the dropped ACK: %v", err)
	}
	committed, err := store.GetTurn(t.Context(), first.Turn.ID)
	if err != nil || committed.Status != harness.TurnCancelled {
		t.Fatalf("partial cancellation CAS after lost ACK: %+v %v", committed, err)
	}
	before, err := store.ListItems(t.Context(), first.Turn.ID, 0, 1000)
	if err != nil || !harness.ActiveCancellationFence(before) {
		t.Fatalf("unknown ACK must retain original cancellation fence: %v", err)
	}
	stopped, err := runner.Cancel(t.Context(), first.Turn.ID, "resume identical lost-ACK cancellation")
	if err != nil || stopped.Turn.Status != harness.TurnCancelled {
		t.Fatalf("no-Run Cancel: %+v %v", stopped.Turn, err)
	}
	items, err := store.ListItems(t.Context(), first.Turn.ID, 0, 1000)
	if err != nil || harness.ActiveCancellationFence(items) {
		t.Fatalf("original cancellation fence not settled: %v", err)
	}
	ledger, err = store.LoadBudget(t.Context(), first.Turn.ID)
	if err != nil || !ledger.CancelledRuns[inv.ExecutionRefID] {
		t.Fatalf("new generation remained active: %+v %v", ledger.CancelledRuns, err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("unstarted Retry dispatched Model: %d", len(client.requests))
	}
	resumed, err := runner.RetryInvocation(t.Context(), root.ID)
	if err != nil || resumed.Turn.Status != harness.TurnWaitingInput {
		t.Fatalf("next admitted Attempt: %+v %v", resumed.Turn, err)
	}
	next, ok := harness.TopLevelInvocation(resumed)
	if !ok || next.Attempt != 3 || len(client.requests) != 2 {
		t.Fatalf("extra original physical dispatch: %+v modelCalls=%d", next, len(client.requests))
	}
	for _, request := range client.requests {
		if request.MaxOutputTokens <= 0 || request.MaxOutputTokens > 256 {
			t.Fatalf("root retry lost the original per-call output ceiling: %d", request.MaxOutputTokens)
		}
	}
	retriedRun, err := rt.Load(t.Context(), next.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	retriedView, err := agent.ViewState(retriedRun)
	if err != nil || retriedView.MaxModelOutputTokens != 256 {
		t.Fatalf("retry did not freeze the original ceiling: %d err=%v", retriedView.MaxModelOutputTokens, err)
	}
	if resumed.Budget == nil || resumed.Budget.Usage.LLMCalls != 2 || resumed.Budget.Cancelled {
		t.Fatalf("Turn accounting lost or remained globally cancelled: %+v", resumed.Budget)
	}
}
