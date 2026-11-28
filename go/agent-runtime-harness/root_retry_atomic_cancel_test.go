package harness_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
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

// The Host Store selects an immutable retry Attempt and discards only its ACK.
// Native admission owns the budget generation. Restarted callers must never
// reconstruct a second Attempt or execute an unadmitted external effect.
type rootRetryLostACKStore struct {
	harness.Store
	failOnce      bool
	cancelAckOnce bool
}

func (s *rootRetryLostACKStore) UpdateTurn(ctx context.Context, turn harness.Turn, revision uint64) (harness.Turn, error) {
	updated, err := s.Store.UpdateTurn(ctx, turn, revision)
	if err == nil && turn.Status == harness.TurnCancelled && s.cancelAckOnce {
		s.cancelAckOnce = false
		return harness.Turn{}, errRootRetryCancelAckLost
	}
	return updated, err
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
	store := &rootRetryLostACKStore{Store: raw, failOnce: true}
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &roleBudgetModel{}
	shared, err := harness.NewBudgetAuthority(harness.BudgetAuthorityDependencies{
		Runtime: rt, Store: store, Relations: relations, Meter: client, Clock: fixedClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	bounded, err := shared.Adapter()
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
		BoundedBudget: bounded,
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
	// The first and every retried native Run must receive the original
	// per-Invocation allowance, not a nil (unbounded) grant.
	request.CallAllowance = &agent.CallAllowance{LLMCalls: 4, ToolCalls: 4}
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
			ParentItemID: "original-parent-item", ExecutionRefID: "old-child-run",
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
	beforeRetry, err := rt.BudgetStore().LoadBudget(t.Context(), first.Turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.RetryInvocation(t.Context(), root.ID)
	if !errors.Is(err, errRootRetryCommittedAckLost) {
		t.Fatalf("no committed-ACK loss: %v", err)
	}
	inv, err := store.GetInvocation(t.Context(), root.ID)
	if err != nil || inv.Attempt != 2 || inv.Status != harness.InvocationAccepted {
		t.Fatalf("rotated Invocation missing: %+v %v", inv, err)
	}
	ledger, err := rt.BudgetStore().LoadBudget(t.Context(), first.Turn.ID)
	if err != nil || !reflect.DeepEqual(ledger, beforeRetry) {
		t.Fatalf("Host retry selection changed native accounting before admission: %+v %v", ledger, err)
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
	if err != nil || harness.ActiveCancellationFence(before) != historicalTerminalChild {
		t.Fatalf("only independent child cleanup may retain a Host fence: %v", err)
	}
	decision, err := rt.LoadCreationDecision(t.Context(), inv.ExecutionRefID)
	if err != nil || decision.Status != kernel.RunStatusCancelled {
		t.Fatalf("lost projection ACK lost permanent Runtime cancellation: %+v %v", decision, err)
	}
	stopped, err := runner.Cancel(t.Context(), first.Turn.ID, "resume identical lost-ACK cancellation")
	if err != nil || stopped.Turn.Status != harness.TurnCancelled {
		t.Fatalf("no-Run Cancel: %+v %v", stopped.Turn, err)
	}
	items, err := store.ListItems(t.Context(), first.Turn.ID, 0, 1000)
	if err != nil || harness.ActiveCancellationFence(items) {
		t.Fatalf("original cancellation fence not settled: %v", err)
	}
	ledger, err = rt.BudgetStore().LoadBudget(t.Context(), first.Turn.ID)
	grant, enrolled := ledger.RootRetries[inv.ExecutionRefID]
	if err != nil || !enrolled || grant.InvocationID != inv.ID || grant.OriginalRunID != root.ExecutionRefID || grant.Attempt != inv.Attempt || !grant.Active || !ledger.CancelledRuns[inv.ExecutionRefID] {
		t.Fatalf("native negative admission did not enroll and close the selected generation: grant=%+v cancelled=%+v err=%v", grant, ledger.CancelledRuns, err)
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
	if !reflect.DeepEqual(retriedView.CallAllowance, &agent.CallAllowance{LLMCalls: 4, ToolCalls: 4}) {
		t.Fatalf("retry widened/forgot the original per-Run authorization: %+v", retriedView.CallAllowance)
	}
	if resumed.Budget == nil || resumed.Budget.Usage.LLMCalls != 2 || resumed.Budget.Cancelled {
		t.Fatalf("Turn accounting lost or remained globally cancelled: %+v", resumed.Budget)
	}
}
