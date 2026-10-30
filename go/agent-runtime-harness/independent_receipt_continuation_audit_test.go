package harness_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

func TestReauditVerifiedInteractionReconcileIdempotency(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	agentFeature := &recordingInteractionAgent{loadingFeatureAgent: loadingFeatureAgent{runtime: runtime}}
	handler := &auditReadOnlyInteractionReconciler{}
	handler.known.Store(true)
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: agentFeature, Store: store, Clock: featureInvocationClock{}, Interactions: handler,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := featureInvocationClock{}.Now()
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "reconcile-idempotence-root"}
	sid, err := harness.SessionID(hostThread)
	if err != nil {
		t.Fatal(err)
	}
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "reconcile-idempotence-turn"}
	tid, err := harness.TurnID(sid, hostTurn)
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: testTenant, ActorID: testActor}
	thread := kernel.ThreadRef{Kind: testThreadKind, ID: hostThread.ID}
	seedFeatureInvocationEnvelope(t, store, sid, tid, hostThread, hostTurn, actor, now)
	_, parentItemID := seedFeatureInvocationParent(t, runtime, store, tid, actor, thread, now)
	waiting, err := runner.RequestInteraction(t.Context(), tid, harness.RequestInteraction{
		InvocationID: interactionParentInvocationID, ParentItemID: parentItemID,
		Key: "recoverable-idempotence", Kind: harness.InteractionChoice, Schema: json.RawMessage("{}"),
	})
	if err != nil || len(waiting.Interactions) != 1 {
		t.Fatalf("setup=%v", err)
	}
	i, err := store.GetInteraction(t.Context(), waiting.Interactions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	i.Status = harness.InteractionResolved
	i.Response = json.RawMessage("{}")
	if _, err = store.UpdateInteraction(t.Context(), i, i.Revision); err != nil {
		t.Fatal(err)
	}
	inv, err := store.GetInvocation(t.Context(), interactionParentInvocationID)
	if err != nil {
		t.Fatal(err)
	}
	inv.Status = harness.InvocationRunning
	if _, err = store.UpdateInvocation(t.Context(), inv, inv.Revision); err != nil {
		t.Fatal(err)
	}
	turn, err := store.GetTurn(t.Context(), tid)
	if err != nil {
		t.Fatal(err)
	}
	turn.Status = harness.TurnRunning
	if _, err = store.UpdateTurn(t.Context(), turn, turn.Revision); err != nil {
		t.Fatal(err)
	}
	first, firstErr := runner.ReconcileInteraction(t.Context(), tid, i.ID)
	if firstErr != nil || first.Turn.Status != harness.TurnRunning || agentFeature.resumeCalls != 1 {
		t.Fatalf("first verified continuation failed: err=%v turn=%s resumeCalls=%d", firstErr, first.Turn.Status, agentFeature.resumeCalls)
	}
	_, secondErr := runner.ReconcileInteraction(t.Context(), tid, i.ID)
	if !errors.Is(secondErr, harness.ErrInteractionOutcomeUnknown) || agentFeature.resumeCalls != 1 {
		t.Fatalf("duplicate verified continuation was not fail-closed: err=%v resumeCalls=%d", secondErr, agentFeature.resumeCalls)
	}
}

func TestReauditNestedSubtaskCancellationFencesDescendantBusinessCall(t *testing.T) {
	for _, mode := range []string{"CancelSubtask", "CancelInvocation"} {
		t.Run(mode, func(t *testing.T) {
			_, turnID, parentItemID, relations, _, store, runtime := newFeatureInvocationHarness(t)
			handler := &auditBlockBusinessHandler{start: make(chan struct{}), release: make(chan struct{})}
			runner, err := harness.NewRunner(harness.Dependencies{
				Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: store,
				Clock: featureInvocationClock{}, Teams: auditRunningTeamFeature{runtime: runtime},
				Relations: relations, Interactions: handler,
			})
			if err != nil {
				t.Fatal(err)
			}
			started, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
				ParentItemID: parentItemID, RequestID: "nested-parent-" + mode, Goal: "parent team",
				Mode: team.ExecutionSequential, Members: []team.Member{{ID: "member", Goal: "task"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			var parent harness.Invocation
			for _, inv := range started.Invocations {
				if inv.ExecutionClass == harness.ExecutionTeam && inv.ParentItemID != "" {
					parent = inv
					break
				}
			}
			if parent.ID == "" {
				t.Fatal("missing parent Team Invocation")
			}
			nestedRun, err := runtime.Create(t.Context(), kernel.CreateRequest{
				ID: "nested-run", Kind: team.RunKind,
				Actor:     kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
				Thread:    kernel.ThreadRef{Kind: testThreadKind, ID: "feature-thread"},
				RequestID: "nested-" + mode, Goal: "nested effect", State: json.RawMessage("{}"),
			})
			if err != nil {
				t.Fatal(err)
			}
			nested := harness.Invocation{
				ID: "hiv_nested", TurnID: turnID, ParentItemID: parentItemID,
				CapabilityKey: harness.CapabilityTeam, DefinitionVersion: harness.RuntimeCapabilityVersion,
				ExecutionClass: harness.ExecutionTeam, InputHash: "nested",
				ExecutionRefID: nestedRun.Run.ID, Status: harness.InvocationRunning, Attempt: 1, Revision: 1,
				CreatedAt: featureInvocationClock{}.Now(), UpdatedAt: featureInvocationClock{}.Now(),
			}
			if _, _, err = store.CreateInvocation(t.Context(), nested); err != nil {
				t.Fatal(err)
			}
			if _, err = relations.Ensure(t.Context(), runrelation.Draft{
				ParentRunID: parent.ExecutionRefID, ChildRunID: nestedRun.Run.ID,
				Kind: runrelation.KindCapability, OwnerNodeID: "nested-relation-" + mode,
			}); err != nil {
				t.Fatal(err)
			}
			waiting, err := runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
				InvocationID: nested.ID, Key: "nested-decision", Kind: harness.InteractionChoice, Schema: json.RawMessage("{}"),
			})
			if err != nil || len(waiting.Interactions) != 1 {
				t.Fatalf("create nested Interaction: err=%v interactions=%d", err, len(waiting.Interactions))
			}
			done := make(chan error, 1)
			go func() {
				_, e := runner.ResolveInteraction(context.Background(), turnID, waiting.Interactions[0].ID,
					harness.ResolveInteractionRequest{Response: json.RawMessage("{}")})
				done <- e
			}()
			<-handler.start
			var cancelErr error
			switch mode {
			case "CancelSubtask":
				loaded, e := runner.Load(t.Context(), turnID)
				if e != nil {
					close(handler.release)
					t.Fatal(e)
				}
				var taskID string
				for _, task := range loaded.Subtasks {
					if task.RunID == parent.ExecutionRefID {
						taskID = task.ID
						break
					}
				}
				if taskID == "" {
					close(handler.release)
					t.Fatal("missing Team Subtask")
				}
				_, cancelErr = runner.CancelSubtask(t.Context(), turnID, taskID, "cancel parent with nested claim")
			default:
				_, cancelErr = runner.CancelInvocation(t.Context(), parent.ID, "cancel parent with nested claim")
			}
			state, loadErr := runtime.Load(t.Context(), nested.ExecutionRefID)
			close(handler.release)
			businessErr := <-done
			if cancelErr == nil {
				t.Fatalf("REPRO: %s succeeded while descendant business callback in flight: nestedStatus=%s businessRuns=%d bizErr=%v loadErr=%v",
					mode, state.Run.Status, handler.calls.Load(), businessErr, loadErr)
			}
			if loadErr == nil && state.Run.Status == kernel.RunStatusCancelled {
				t.Fatalf("REPRO: %s cancelled descendant Kernel Run during physical business callback: err=%v", mode, cancelErr)
			}
		})
	}
}

type auditBlockingResumeAgent struct {
	loadingFeatureAgent
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (a *auditBlockingResumeAgent) Resume(ctx context.Context, runID string, _ uint64) (kernel.Snapshot, error) {
	a.calls.Add(1)
	a.entered <- struct{}{}
	<-a.release
	return a.runtime.Load(ctx, runID)
}

func TestReauditConcurrentVerifiedReconcileDoubleResumesRootAgent(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	feature := &auditBlockingResumeAgent{loadingFeatureAgent: loadingFeatureAgent{runtime: runtime},
		entered: make(chan struct{}, 2), release: make(chan struct{})}
	handler := &auditReadOnlyInteractionReconciler{}
	handler.known.Store(true)
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: feature, Store: store,
		Clock: featureInvocationClock{}, Interactions: handler})
	if err != nil {
		t.Fatal(err)
	}
	now := featureInvocationClock{}.Now()
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "concurrent-reconcile-root"}
	sid, _ := harness.SessionID(hostThread)
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "concurrent-reconcile-turn"}
	tid, _ := harness.TurnID(sid, hostTurn)
	actor := kernel.ActorRef{TenantID: testTenant, ActorID: testActor}
	thread := kernel.ThreadRef{Kind: testThreadKind, ID: hostThread.ID}
	seedFeatureInvocationEnvelope(t, store, sid, tid, hostThread, hostTurn, actor, now)
	_, parentItemID := seedFeatureInvocationParent(t, runtime, store, tid, actor, thread, now)
	waiting, err := runner.RequestInteraction(t.Context(), tid, harness.RequestInteraction{
		InvocationID: interactionParentInvocationID, ParentItemID: parentItemID,
		Key: "reconcile-dual-run", Kind: harness.InteractionChoice, Schema: json.RawMessage("{}"),
	})
	if err != nil {
		t.Fatal(err)
	}
	i, err := store.GetInteraction(t.Context(), waiting.Interactions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	i.Status = harness.InteractionResolved
	i.Response = json.RawMessage("{}")
	if _, err = store.UpdateInteraction(t.Context(), i, i.Revision); err != nil {
		t.Fatal(err)
	}
	inv, err := store.GetInvocation(t.Context(), interactionParentInvocationID)
	if err != nil {
		t.Fatal(err)
	}
	inv.Status = harness.InvocationRunning
	if _, err = store.UpdateInvocation(t.Context(), inv, inv.Revision); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetTurn(t.Context(), tid)
	if err != nil {
		t.Fatal(err)
	}
	current.Status = harness.TurnRunning
	if _, err = store.UpdateTurn(t.Context(), current, current.Revision); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() { _, e := runner.ReconcileInteraction(context.Background(), tid, i.ID); done <- e }()
	<-feature.entered
	go func() { _, e := runner.ReconcileInteraction(context.Background(), tid, i.ID); done <- e }()
	twice := false
	select {
	case <-feature.entered:
		twice = true
	case <-time.After(400 * time.Millisecond):
	}
	close(feature.release)
	e1, e2 := <-done, <-done
	if twice || feature.calls.Load() != 1 {
		t.Fatalf("multiple concurrent reconciliations dispatched Agent.Resume: calls=%d errors=%v,%v", feature.calls.Load(), e1, e2)
	}
	if e1 == nil && e2 == nil {
		t.Fatal("duplicate verified continuation was acknowledged as successfully dispatched")
	}
	if e1 != nil && !errors.Is(e1, harness.ErrInteractionOutcomeUnknown) ||
		e2 != nil && !errors.Is(e2, harness.ErrInteractionOutcomeUnknown) {
		t.Fatalf("unexpected concurrent continuation errors: first=%v second=%v", e1, e2)
	}
}

// Application child Invocations are intentionally not in RunRelation. Their
// immutable parent Item ancestry must still protect the physical executor
// from ancestor Subtask/Invocation/Turn cancellation.
func TestReauditRunningApplicationChildFencesAncestorCancellation(t *testing.T) {
	for _, mode := range []string{"CancelSubtask", "CancelInvocation", "CancelTurn"} {
		t.Run(mode, func(t *testing.T) {
			_, turnID, parentItemID, relations, _, store, runtime := newFeatureInvocationHarness(t)
			executor := &blockingHardcutApplication{started: make(chan struct{}), release: make(chan struct{})}
			runner, err := harness.NewRunner(harness.Dependencies{
				Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: store,
				Clock: featureInvocationClock{}, Teams: auditRunningTeamFeature{runtime: runtime},
				Relations: relations, Applications: executor,
			})
			if err != nil {
				t.Fatal(err)
			}
			started, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
				ParentItemID: parentItemID, RequestID: "parent-application-" + mode,
				Goal: "own child effect", Mode: team.ExecutionSequential,
				Members: []team.Member{{ID: "member", Goal: "task"}},
			})
			if err != nil || len(started.Subtasks) == 0 {
				t.Fatalf("start: %v", err)
			}
			var parent harness.Invocation
			for _, inv := range started.Invocations {
				if inv.ParentItemID != "" && inv.ExecutionClass == harness.ExecutionTeam {
					parent = inv
					break
				}
			}
			if parent.ID == "" {
				t.Fatal("missing Team parent")
			}
			parentLifecycle := ""
			for _, item := range started.Items {
				if item.Kind == harness.ItemInvocation && item.InvocationID == parent.ID {
					parentLifecycle = item.ID
					break
				}
			}
			if parentLifecycle == "" {
				t.Fatal("missing parent Item")
			}
			done := make(chan error, 1)
			go func() {
				_, e := runner.StartApplicationInvocation(context.Background(), turnID, harness.ApplicationInvocationRequest{
					ParentItemID: parentLifecycle, RequestID: "physical-app-" + mode, Goal: "external effect",
					CapabilityKey: testApplicationCapabilityKey, DefinitionVersion: testApplicationVersion,
					Input: json.RawMessage("{}"),
				})
				done <- e
			}()
			<-executor.started
			var cancelErr error
			switch mode {
			case "CancelSubtask":
				_, cancelErr = runner.CancelSubtask(t.Context(), turnID, started.Subtasks[0].ID, "cancel ancestor")
			case "CancelInvocation":
				_, cancelErr = runner.CancelInvocation(t.Context(), parent.ID, "cancel ancestor")
			default:
				_, cancelErr = runner.Cancel(t.Context(), turnID, "cancel root")
			}
			parentRun, loadErr := runtime.Load(t.Context(), parent.ExecutionRefID)
			close(executor.release)
			<-done
			if !errors.Is(cancelErr, harness.ErrApplicationOutcomeUnknown) ||
				loadErr != nil || parentRun.Run.Status == kernel.RunStatusCancelled {
				t.Fatalf("ancestor cancellation crossed running Application executor: err=%v runStatus=%s loadErr=%v", cancelErr, parentRun.Run.Status, loadErr)
			}
		})
	}
}

// A committed resolved Interaction and Running Invocation alone cannot prove
// that the Host callback ran. A second identical response is not an ACK when
// its durable physical outcome is unknown (even if the parent Turn is running).
func TestReauditRunningResolvedResponseReplayRequiresBusinessReceipt(t *testing.T) {
	runner, turnID, parentItemID, _, _, store, _ := newFeatureInvocationHarness(t)
	waiting, err := runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
		InvocationID: interactionParentInvocationID, ParentItemID: parentItemID,
		Key: "ambiguous-replay", Kind: harness.InteractionChoice, Schema: json.RawMessage("{}"),
	})
	if err != nil {
		t.Fatal(err)
	}
	interaction, err := store.GetInteraction(t.Context(), waiting.Interactions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	interaction.Status = harness.InteractionResolved
	interaction.Response = json.RawMessage("{}")
	if _, err = store.UpdateInteraction(t.Context(), interaction, interaction.Revision); err != nil {
		t.Fatal(err)
	}
	invocation, err := store.GetInvocation(t.Context(), interactionParentInvocationID)
	if err != nil {
		t.Fatal(err)
	}
	invocation.Status = harness.InvocationRunning
	if _, err = store.UpdateInvocation(t.Context(), invocation, invocation.Revision); err != nil {
		t.Fatal(err)
	}
	turn, err := store.GetTurn(t.Context(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	turn.Status = harness.TurnRunning
	if _, err = store.UpdateTurn(t.Context(), turn, turn.Revision); err != nil {
		t.Fatal(err)
	}
	_, err = runner.ResolveInteraction(t.Context(), turnID, interaction.ID, harness.ResolveInteractionRequest{Response: json.RawMessage("{}")})
	if !errors.Is(err, harness.ErrInteractionOutcomeUnknown) {
		t.Fatalf("running owner without verified business receipt was replayed as success: %v", err)
	}
}

// A Store transport can lose its response AFTER a continuation-claim Item
// commits. Such a loss must not authorize a new physical feature Resume even
// though the host's read-only reconciler returns known=true on every retry.
type auditLostContinuationClaimACK struct {
	harness.Store
	failed atomic.Bool
}

func (s *auditLostContinuationClaimACK) AppendItem(ctx context.Context, item harness.Item) (harness.Item, bool, error) {
	result, created, err := s.Store.AppendItem(ctx, item)
	if err == nil && created && item.Kind == harness.ItemDiagnostic &&
		bytes.Contains(item.Payload, []byte("runtime_continuation_claimed")) &&
		s.failed.CompareAndSwap(false, true) {
		return harness.Item{}, false, errors.New("injected lost continuation-claim commit ACK")
	}
	return result, created, err
}

func TestReauditLostContinuationClaimACKCannotReenterAgent(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	backing := harness.NewMemoryStore()
	store := &auditLostContinuationClaimACK{Store: backing}
	agentFeature := &recordingInteractionAgent{loadingFeatureAgent: loadingFeatureAgent{runtime: runtime}}
	handler := &auditReadOnlyInteractionReconciler{}
	handler.known.Store(true)
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: agentFeature, Store: store,
		Clock: featureInvocationClock{}, Interactions: handler,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := featureInvocationClock{}.Now()
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "lost-continuation-ack"}
	sid, _ := harness.SessionID(hostThread)
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "lost-continuation-ack-turn"}
	tid, _ := harness.TurnID(sid, hostTurn)
	actor := kernel.ActorRef{TenantID: testTenant, ActorID: testActor}
	thread := kernel.ThreadRef{Kind: testThreadKind, ID: hostThread.ID}
	seedFeatureInvocationEnvelope(t, backing, sid, tid, hostThread, hostTurn, actor, now)
	_, parentItemID := seedFeatureInvocationParent(t, runtime, backing, tid, actor, thread, now)
	waiting, err := runner.RequestInteraction(t.Context(), tid, harness.RequestInteraction{
		InvocationID: interactionParentInvocationID, ParentItemID: parentItemID,
		Key: "lost-ack", Kind: harness.InteractionChoice, Schema: json.RawMessage("{}"),
	})
	if err != nil {
		t.Fatal(err)
	}
	interaction := waiting.Interactions[0]
	interaction.Status = harness.InteractionResolved
	interaction.Response = json.RawMessage("{}")
	if _, err = backing.UpdateInteraction(t.Context(), interaction, interaction.Revision); err != nil {
		t.Fatal(err)
	}
	invocation, err := backing.GetInvocation(t.Context(), interactionParentInvocationID)
	if err != nil {
		t.Fatal(err)
	}
	invocation.Status = harness.InvocationRunning
	if _, err = backing.UpdateInvocation(t.Context(), invocation, invocation.Revision); err != nil {
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

	if _, err = runner.ReconcileInteraction(t.Context(), tid, interaction.ID); err == nil || !store.failed.Load() {
		t.Fatalf("post-commit claim ACK fault was not injected: err=%v faulted=%v", err, store.failed.Load())
	}
	if agentFeature.resumeCalls != 0 {
		t.Fatalf("continuation ran despite uncertain claim commit: calls=%d", agentFeature.resumeCalls)
	}
	restarted, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: agentFeature, Store: store, Clock: featureInvocationClock{}, Interactions: handler,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = restarted.ReconcileInteraction(t.Context(), tid, interaction.ID)
	if !errors.Is(err, harness.ErrInteractionOutcomeUnknown) || agentFeature.resumeCalls != 0 {
		t.Fatalf("ambiguous claim ACK triggered unsafe replay: err=%v calls=%d", err, agentFeature.resumeCalls)
	}
}

// An unrelated sibling Run must not inherit the cancellation block from a
// claimed callback on another sibling when RunRelation proves disjoint owners.
func TestReauditUnrelatedSiblingCanCancelWhileOtherCallbackPending(t *testing.T) {
	_, turnID, parentItemID, relations, _, store, runtime := newFeatureInvocationHarness(t)
	handler := &auditBlockBusinessHandler{start: make(chan struct{}), release: make(chan struct{})}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: store,
		Clock: featureInvocationClock{}, Teams: auditRunningTeamFeature{runtime: runtime},
		Relations: relations, Interactions: handler,
	})
	if err != nil {
		t.Fatal(err)
	}
	var childRuns [2]string
	var childIDs [2]string
	var taskIDs [2]string
	for i, requestID := range []string{"busy-sibling", "independent-sibling"} {
		started, startErr := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
			ParentItemID: parentItemID, RequestID: requestID, Goal: requestID,
			Mode: team.ExecutionSequential, Members: []team.Member{{ID: "member", Goal: "task"}},
		})
		if startErr != nil {
			t.Fatal(startErr)
		}
		for _, invocation := range started.Invocations {
			if invocation.ExecutionClass == harness.ExecutionTeam && invocation.ParentItemID != "" &&
				invocation.ExecutionRefID != childRuns[0] {
				childRuns[i], childIDs[i] = invocation.ExecutionRefID, invocation.ID
			}
		}
		for _, task := range started.Subtasks {
			if task.RunID == childRuns[i] {
				taskIDs[i] = task.ID
			}
		}
		if childIDs[i] == "" || taskIDs[i] == "" {
			t.Fatalf("missing sibling %d: invocations=%+v subtasks=%+v", i, started.Invocations, started.Subtasks)
		}
	}
	waiting, err := runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
		InvocationID: childIDs[0], Key: "unrelated-sibling-business",
		Kind: harness.InteractionChoice, Schema: json.RawMessage("{}"),
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, callbackErr := runner.ResolveInteraction(context.Background(), turnID, waiting.Interactions[0].ID,
			harness.ResolveInteractionRequest{Response: json.RawMessage("{}")})
		done <- callbackErr
	}()
	<-handler.start
	_, cancelErr := runner.CancelSubtask(t.Context(), turnID, taskIDs[1], "cancel unrelated sibling")
	busyRun, busyErr := runtime.Load(t.Context(), childRuns[0])
	close(handler.release)
	<-done
	if cancelErr != nil || busyErr != nil || busyRun.Run.Status == kernel.RunStatusCancelled {
		t.Fatalf("unrelated sibling cancel blocked or cancelled business owner: err=%v busyStatus=%s loadErr=%v",
			cancelErr, busyRun.Run.Status, busyErr)
	}
}
