package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

type terminalApplicationTurnCommitFailure struct {
	harness.Store
	failed atomic.Bool
}

func (s *terminalApplicationTurnCommitFailure) UpdateTurn(ctx context.Context, turn harness.Turn, revision uint64) (harness.Turn, error) {
	if turn.Status == harness.TurnCompleted && s.failed.CompareAndSwap(false, true) {
		return harness.Turn{}, errors.New("injected lost terminal Turn commit")
	}
	return s.Store.UpdateTurn(ctx, turn, revision)
}

type countOriginalHostResponse struct{ calls atomic.Int32 }

func (h *countOriginalHostResponse) HandleInteractionResponse(ctx context.Context, value harness.InteractionResponseContext) (harness.InteractionResponseResult, error) {
	h.calls.Add(1)
	return applicationInteractionHandlerFixture{}.HandleInteractionResponse(ctx, value)
}

func runOriginalApplicationCallbackToLostTerminalTurn(t *testing.T, backing harness.Store) (
	*harness.Runner, harness.Interaction, string, *applicationCapabilityExecutorFixture, *countOriginalHostResponse,
) {
	t.Helper()
	runtime := newFeatureInvocationRuntime(t)
	executor := &applicationCapabilityExecutorFixture{interaction: &harness.ApplicationInteractionRequest{
		ApplicationRef: &harness.HostRef{Kind: testApplicationStoryRefKind, ID: "repair_story"},
		ArtifactRefs:   []harness.HostRef{{Kind: testApplicationPortfolioKind, ID: "repair_portfolio"}},
		Key:            testApplicationCapabilityKey, Kind: harness.InteractionChoice,
		Schema: json.RawMessage(`{"type":"object"}`),
	}}
	handler := &countOriginalHostResponse{}
	faulted := &terminalApplicationTurnCommitFailure{Store: backing}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
		Store: faulted, Clock: featureInvocationClock{}, Applications: executor, Interactions: handler,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := harness.ApplicationTurnRequest{
		StartRequest: harness.StartRequest{
			HostThread: harness.HostRef{Kind: testThreadKind, ID: "app-terminal-recovery-thread"},
			HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "app-terminal-recovery-turn"},
			Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
			Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "app-terminal-recovery-thread"},
			RequestID:  "application-terminal-recovery", Goal: "recover original business response",
		},
		CapabilityKey:     testApplicationCapabilityKey,
		DefinitionVersion: testApplicationVersion,
		Input:             json.RawMessage(`{}`),
	}
	waiting, err := runner.StartApplicationTurn(t.Context(), request)
	if err != nil || len(waiting.Interactions) != 1 {
		t.Fatalf("failed to create waiting Application: %v %#v", err, waiting.Interactions)
	}
	interaction := waiting.Interactions[0]
	_, err = runner.ResolveInteraction(t.Context(), waiting.Turn.ID, interaction.ID, harness.ResolveInteractionRequest{
		Response: json.RawMessage(`{"outcome":"select","selectedCandidateIDs":["candidate_1"]}`),
	})
	if err == nil || !faulted.failed.Load() {
		t.Fatalf("terminal CAS failure was not injected: %v", err)
	}
	stored, err := backing.GetTurn(t.Context(), waiting.Turn.ID)
	if err != nil || stored.Status == harness.TurnCompleted {
		t.Fatalf("terminal Turn wrongly committed: %v %+v", err, stored)
	}
	owners, err := backing.ListInvocations(t.Context(), waiting.Turn.ID)
	if err != nil || len(owners) != 1 || owners[0].Status != harness.InvocationCompleted {
		t.Fatalf("original Application completion not committed: %v %#v", err, owners)
	}
	items, err := backing.ListItems(t.Context(), waiting.Turn.ID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	receipt, continuation := 0, 0
	for _, item := range items {
		if strings.HasPrefix(item.ID, "hbizverified_") {
			receipt++
		}
		if strings.HasPrefix(item.ID, "hcontclaim_") {
			continuation++
		}
	}
	if receipt != 1 || continuation != 0 || handler.calls.Load() != 1 {
		t.Fatalf("incomplete original Host proof: receipt=%d continuation=%d calls=%d", receipt, continuation, handler.calls.Load())
	}
	// Return the original Runner's underlying runtime by closing over it through a new Runner.
	reopened, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
		Store: backing, Clock: featureInvocationClock{}, Applications: executor, Interactions: handler,
	})
	if err != nil {
		t.Fatal(err)
	}
	return reopened, interaction, waiting.Turn.ID, executor, handler
}

func TestCompletedApplicationHostCallbackRepairsLostTurnCASInMemory(t *testing.T) {
	backing := harness.NewMemoryStore()
	restarted, interaction, turnID, executor, handler := runOriginalApplicationCallbackToLostTerminalTurn(t, backing)
	repaired, err := restarted.Refresh(t.Context(), turnID)
	if err != nil || repaired.Turn.Status != harness.TurnCompleted {
		t.Fatalf("committed Host callback failed terminal recovery: turn=%s err=%v", repaired.Turn.Status, err)
	}
	again, err := restarted.ReconcileInteraction(t.Context(), turnID, interaction.ID)
	if err != nil || again.Turn.Status != harness.TurnCompleted {
		t.Fatalf("idempotent reconciliation failed: turn=%s err=%v", again.Turn.Status, err)
	}
	_, err = restarted.ResolveInteraction(t.Context(), turnID, interaction.ID, harness.ResolveInteractionRequest{
		Response: json.RawMessage(`{"outcome":"select","selectedCandidateIDs":["candidate_1"]}`),
	})
	if err != nil || executor.calls != 1 || handler.calls.Load() != 1 {
		t.Fatalf("business callback was replayed or original result lost: err=%v executor=%d handler=%d", err, executor.calls, handler.calls.Load())
	}
}

// A Host retry may be the FIRST recovery request after the callback and
// InvocationCommitted write. It must not try to load an Application Kernel
// Run (there is none), and must never repeat the business response handler.
func TestCompletedApplicationRootResponseReplayBeforeRefreshRepairsTurnInMemory(t *testing.T) {
	for _, entry := range []string{"resolve-first", "reconcile-first"} {
		t.Run(entry, func(t *testing.T) {
			backing := harness.NewMemoryStore()
			restarted, interaction, turnID, executor, handler := runOriginalApplicationCallbackToLostTerminalTurn(t, backing)
			var recovered harness.Snapshot
			var err error
			if entry == "resolve-first" {
				recovered, err = restarted.ResolveInteraction(t.Context(), turnID, interaction.ID, harness.ResolveInteractionRequest{
					Response: json.RawMessage(`{"outcome":"select","selectedCandidateIDs":["candidate_1"]}`),
				})
			} else {
				recovered, err = restarted.ReconcileInteraction(t.Context(), turnID, interaction.ID)
			}
			if err != nil || recovered.Turn.Status != harness.TurnCompleted {
				t.Fatalf("first recovery %s lost committed Application result: turn=%s err=%v", entry, recovered.Turn.Status, err)
			}
			if executor.calls != 1 || handler.calls.Load() != 1 {
				t.Fatalf("first recovery repeated business effect: executor=%d handler=%d", executor.calls, handler.calls.Load())
			}
		})
	}
}

func TestApplicationTerminalRepairRejectsInvalidOriginalReceiptInMemory(t *testing.T) {
	backing := harness.NewMemoryStore()
	_, interaction, turnID, _, _ := runOriginalApplicationCallbackToLostTerminalTurn(t, backing)
	items, err := backing.ListItems(t.Context(), turnID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	owners, err := backing.ListInvocations(t.Context(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := backing.GetInteraction(t.Context(), interaction.ID)
	if err != nil {
		t.Fatal(err)
	}
	original := append([]harness.Item(nil), items...)
	if !harness.ValidTerminalProjectionProof(context.Background(), turnID, "", []harness.Interaction{resolved}, owners, original) {
		t.Fatal("valid original Application Host proof rejected")
	}
	corrupt := json.RawMessage(`{"interactionID":"forged","boundary":"host_response_verified"}`)
	found := false
	for index := range items {
		if strings.HasPrefix(items[index].ID, "hbizverified_") {
			items[index].Payload = corrupt
			found = true
		}
	}
	if !found {
		t.Fatal("missing original receipt")
	}
	if harness.ValidTerminalProjectionProof(context.Background(), turnID, "", []harness.Interaction{resolved}, owners, items) {
		t.Fatal("corrupt original Host proof authorized terminal Turn")
	}
}
