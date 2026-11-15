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

type auditLoseVerifiedHostReceiptACK struct {
	harness.Store
	failed atomic.Bool
}

func (s *auditLoseVerifiedHostReceiptACK) AppendItem(ctx context.Context, item harness.Item) (harness.Item, bool, error) {
	saved, fresh, err := s.Store.AppendItem(ctx, item)
	if err == nil && fresh && strings.HasPrefix(item.ID, "hbizverified_") && strings.Contains(string(item.Payload), "host_response_verified") && s.failed.CompareAndSwap(false, true) {
		return harness.Item{}, false, errors.New("Host callback receipt committed; acknowledgement lost")
	}
	return saved, fresh, err
}
func requireHostReceiptWithoutContinuationClaim(t *testing.T, store harness.Store, turnID string) {
	t.Helper()
	items, err := store.ListItems(t.Context(), turnID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	verified := 0
	for _, item := range items {
		if strings.HasPrefix(item.ID, "hbizverified_") {
			verified++
		}
		if strings.HasPrefix(item.ID, "hcontclaim_") {
			t.Fatal("test unexpectedly persisted a continuation claim")
		}
	}
	if verified != 1 {
		t.Fatalf("expected precisely one committed Host business receipt, got %d", verified)
	}
}
func TestHostReceiptCommittedACKLostCannotReenterRootBeforeClaim(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	backing := harness.NewMemoryStore()
	store := &auditLoseVerifiedHostReceiptACK{Store: backing}
	agentFeature := &recordingInteractionAgent{loadingFeatureAgent: loadingFeatureAgent{runtime: runtime}}
	handler := &auditReadOnlyInteractionReconciler{}
	handler.known.Store(true)
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: agentFeature, Store: store, Clock: featureInvocationClock{}, Interactions: handler})
	if err != nil {
		t.Fatal(err)
	}
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "claim-ack-replay"}
	sid, _ := harness.SessionID(hostThread)
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "claim-ack-replay-turn"}
	tid, _ := harness.TurnID(sid, hostTurn)
	actor := kernel.ActorRef{TenantID: testTenant, ActorID: testActor}
	thread := kernel.ThreadRef{Kind: testThreadKind, ID: hostThread.ID}
	now := featureInvocationClock{}.Now()
	seedFeatureInvocationEnvelope(t, backing, sid, tid, hostThread, hostTurn, actor, now)
	_, parentItem := seedFeatureInvocationParent(t, runtime, backing, tid, actor, thread, now)
	waiting, err := runner.RequestInteraction(t.Context(), tid, harness.RequestInteraction{InvocationID: featureParentInvocationID(tid), ParentItemID: parentItem, Key: "business-callback-known", Kind: harness.InteractionChoice, Schema: json.RawMessage("{}")})
	if err != nil || len(waiting.Interactions) != 1 {
		t.Fatalf("waiting=%v", err)
	}
	i := waiting.Interactions[0]
	i.Status = harness.InteractionResolved
	i.Response = json.RawMessage("{}")
	if _, err = backing.ResolveInteraction(t.Context(), i, i.Revision); err != nil {
		t.Fatal(err)
	}
	inv, err := backing.GetInvocation(t.Context(), featureParentInvocationID(tid))
	if err != nil {
		t.Fatal(err)
	}
	inv.Status = harness.InvocationRunning
	if _, err = backing.UpdateInvocation(t.Context(), inv, inv.Revision); err != nil {
		t.Fatal(err)
	}
	turn, err := backing.GetTurn(t.Context(), tid)
	if err != nil {
		t.Fatal(err)
	}
	turn.Status = harness.TurnRunning
	if _, err = backing.UpdateTurn(t.Context(), turn, turn.Revision); err != nil {
		t.Fatal(err)
	}
	_, err = runner.ReconcileInteraction(t.Context(), tid, i.ID)
	if err == nil || !store.failed.Load() {
		t.Fatalf("must lose Host receipt ACK after committing it: err=%v faulted=%v", err, store.failed.Load())
	}
	requireHostReceiptWithoutContinuationClaim(t, backing, tid)
	resumed, resumeErr := runner.ResumeTurn(t.Context(), tid)
	if agentFeature.resumeCalls != 0 || !errors.Is(resumeErr, harness.ErrInteractionOutcomeUnknown) {
		t.Fatalf("REPRO: ResumeTurn drove Agent.Resume after committed Host receipt without a continuation claim: calls=%d err=%v turn=%s", agentFeature.resumeCalls, resumeErr, resumed.Turn.Status)
	}
}
