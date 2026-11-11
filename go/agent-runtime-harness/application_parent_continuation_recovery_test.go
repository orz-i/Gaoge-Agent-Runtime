package harness_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

type applicationParentReopenFailure struct {
	harness.Store
	failed atomic.Bool
}

func (s *applicationParentReopenFailure) UpdateTurn(ctx context.Context, turn harness.Turn, revision uint64) (harness.Turn, error) {
	if turn.Status == harness.TurnRunning && !s.failed.Load() {
		owners, err := s.ListInvocations(ctx, turn.ID)
		if err == nil {
			for _, child := range owners {
				if child.ExecutionClass == harness.ExecutionApplication && child.ParentItemID != "" &&
					child.Status == harness.InvocationCompleted && s.failed.CompareAndSwap(false, true) {
					return harness.Turn{}, errors.New("injected crash between committed child callback and parent Agent resume")
				}
			}
		}
	}
	return s.Store.UpdateTurn(ctx, turn, revision)
}

// An Application's own completed Host callback must not be confused with
// evidence that the parent Agent entered a later physical Resume. Recovery
// observes without dispatching; explicit ResumeTurn owns that physical effect.
// A parent Resume that predates the child's terminal lifecycle fact is not a
// valid continuation witness.
func TestApplicationChildRecoveryDistinguishesHostCompletionFromParentResumeInMemory(t *testing.T) {
	backing := harness.NewMemoryStore()
	runtime := newFeatureInvocationRuntime(t)
	store := &applicationParentReopenFailure{Store: backing}
	resumer := &applicationResumeAgent{loadingFeatureAgent: loadingFeatureAgent{runtime: runtime}}
	executor := &applicationCapabilityExecutorFixture{interaction: &harness.ApplicationInteractionRequest{
		ApplicationRef: &harness.HostRef{Kind: testApplicationStoryRefKind, ID: "child_recovery_story"},
		ArtifactRefs:   []harness.HostRef{{Kind: testApplicationPortfolioKind, ID: "child_recovery_portfolio"}},
		Key:            testApplicationCapabilityKey, Kind: harness.InteractionChoice,
		Schema: json.RawMessage(`{"type":"object"}`),
	}}
	handler := &countOriginalHostResponse{}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: resumer, Store: store, Clock: featureInvocationClock{},
		Applications: executor, Interactions: handler,
	})
	if err != nil {
		t.Fatal(err)
	}

	now := featureInvocationClock{}.Now()
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "app-child-parent-recovery-thread"}
	sid, err := harness.SessionID(hostThread)
	if err != nil {
		t.Fatal(err)
	}
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "app-child-parent-recovery-turn"}
	tid, err := harness.TurnID(sid, hostTurn)
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: testTenant, ActorID: testActor}
	thread := kernel.ThreadRef{Kind: testThreadKind, ID: hostThread.ID}
	seedFeatureInvocationEnvelope(t, backing, sid, tid, hostThread, hostTurn, actor, now)
	_, parentItemID := seedFeatureInvocationParent(t, runtime, backing, tid, actor, thread, now)

	waiting, err := runner.StartApplicationInvocation(t.Context(), tid, harness.ApplicationInvocationRequest{
		ParentItemID: parentItemID, RequestID: "application-child-parent-recovery",
		Goal: "ask for author decision", CapabilityKey: testApplicationCapabilityKey,
		DefinitionVersion: testApplicationVersion, Input: json.RawMessage(`{}`),
	})
	if err != nil || len(waiting.Interactions) != 1 {
		t.Fatalf("initial child interaction: %v %#v", err, waiting.Interactions)
	}
	// A previously observed parent Resume is not evidence that the Agent
	// resumed AFTER this child callback. Keep a fully valid but earlier
	// dispatch receipt to defend the source-ordering trust boundary.
	originalRoot, err := backing.GetInvocation(t.Context(), featureParentInvocationID(tid))
	if err != nil {
		t.Fatal(err)
	}
	priorGrantPayload, err := json.Marshal(struct {
		Phase         string `json:"phase"`
		PhysicalRunID string `json:"physicalRunID"`
	}{"resume", originalRoot.ExecutionRefID})
	if err != nil {
		t.Fatal(err)
	}
	priorGrant := harness.Item{
		ID: "hdispatch_prior_parent_resume_segment", TurnID: tid,
		Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
		RunID: originalRoot.ExecutionRefID, InvocationID: originalRoot.ID,
		ParentItemID: harness.ExecutionDispatchParentItemID,
		Payload:      priorGrantPayload, CreatedAt: now, UpdatedAt: now,
	}
	if _, created, appendErr := backing.AppendItem(t.Context(), priorGrant); appendErr != nil || !created {
		t.Fatalf("seed prior parent dispatch: created=%v err=%v", created, appendErr)
	}
	priorReceiptDigest := sha256.Sum256([]byte(priorGrant.ID + "\x00"))
	priorReceiptID := "hdispatchdone_" + hex.EncodeToString(priorReceiptDigest[:])[:24]
	if _, created, appendErr := backing.AppendItem(t.Context(), harness.Item{
		ID: priorReceiptID, TurnID: tid, Kind: harness.ItemDiagnostic,
		Status: harness.ItemCompleted, RunID: originalRoot.ExecutionRefID,
		InvocationID: originalRoot.ID, ParentItemID: priorGrant.ID,
		Payload:   json.RawMessage(`{"boundary":"physical_dispatch_returned"}`),
		CreatedAt: now, UpdatedAt: now,
	}); appendErr != nil || !created {
		t.Fatalf("seed earlier valid parent receipt: created=%v err=%v", created, appendErr)
	}

	interaction := waiting.Interactions[0]
	answer := harness.ResolveInteractionRequest{Response: json.RawMessage(`{"outcome":"select","selectedCandidateIDs":["candidate_1"]}`)}
	_, err = runner.ResolveInteraction(t.Context(), tid, interaction.ID, answer)
	if err == nil || !store.failed.Load() {
		t.Fatalf("child-to-parent fault not injected: %v", err)
	}

	restarted, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: resumer, Store: backing, Clock: featureInvocationClock{},
		Applications: executor, Interactions: handler,
	})
	if err != nil {
		t.Fatal(err)
	}
	children, err := backing.ListInvocations(t.Context(), tid)
	if err != nil {
		t.Fatal(err)
	}
	completedChild := false
	for _, child := range children {
		if child.ParentItemID != "" && child.ExecutionClass == harness.ExecutionApplication &&
			child.Status == harness.InvocationCompleted {
			completedChild = true
		}
	}
	if !completedChild {
		t.Fatalf("child business result did not commit: %#v", children)
	}

	refreshed, err := restarted.Refresh(t.Context(), tid)
	if err != nil || refreshed.Turn.Status == harness.TurnCompleted || resumer.resumeCalls != 0 {
		t.Fatalf("read-only Refresh reentered or terminalized parent: turn=%s calls=%d err=%v",
			refreshed.Turn.Status, resumer.resumeCalls, err)
	}
	reconciled, err := restarted.ReconcileInteraction(t.Context(), tid, interaction.ID)
	if !errors.Is(err, harness.ErrApplicationParentContinuationUnverified) || resumer.resumeCalls != 0 {
		t.Fatalf("reconciliation silently accepted missing parent physical resume: turn=%s calls=%d err=%v",
			reconciled.Turn.Status, resumer.resumeCalls, err)
	}
	replayed, err := restarted.ResolveInteraction(t.Context(), tid, interaction.ID, answer)
	if !errors.Is(err, harness.ErrApplicationParentContinuationUnverified) || resumer.resumeCalls != 0 {
		t.Fatalf("response replay silently accepted missing parent resume: turn=%s calls=%d err=%v",
			replayed.Turn.Status, resumer.resumeCalls, err)
	}
	resumed, err := restarted.ResumeTurn(t.Context(), tid)
	if err != nil || resumer.resumeCalls != 1 {
		t.Fatalf("explicit original parent physical resume failed: turn=%s calls=%d err=%v",
			resumed.Turn.Status, resumer.resumeCalls, err)
	}
	_, err = restarted.ReconcileInteraction(t.Context(), tid, interaction.ID)
	if err != nil {
		t.Fatalf("validated physical parent receipt was not observed: %v", err)
	}
	_, err = restarted.ResolveInteraction(t.Context(), tid, interaction.ID, answer)
	if err != nil {
		t.Fatalf("idempotent response replay rejected observed parent receipt: %v", err)
	}
	if resumer.resumeCalls != 1 || handler.calls.Load() != 1 || executor.calls != 1 {
		t.Fatalf("a recovery endpoint repeated physical work: parent=%d host=%d app=%d",
			resumer.resumeCalls, handler.calls.Load(), executor.calls)
	}
}
