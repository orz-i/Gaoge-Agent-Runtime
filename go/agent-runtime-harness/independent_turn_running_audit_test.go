package harness_test

import (
	"context"
	"encoding/json"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

func TestIndependentTurnRunningCannotCancelClaimedChildInteraction(t *testing.T) {
	for _, mode := range []string{"CancelSubtask", "CancelInvocation", "CancelTurn"} {
		t.Run(mode, func(t *testing.T) {
			_, turnID, parentItemID, relations, _, store, runtime := newFeatureInvocationHarness(t)
			handler := &auditBlockBusinessHandler{start: make(chan struct{}), release: make(chan struct{})}
			runner, err := harness.NewRunner(harness.Dependencies{
				Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
				Store: store, Clock: featureInvocationClock{}, Teams: auditRunningTeamFeature{runtime: runtime},
				Relations: relations, Interactions: handler,
			})
			if err != nil {
				t.Fatal(err)
			}
			started, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
				ParentItemID: parentItemID, RequestID: "live_child_callback_" + mode, Goal: "running child waiting for business input",
				Mode: team.ExecutionSequential, Members: []team.Member{{ID: "member", Goal: "task"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			var child harness.Invocation
			for _, inv := range started.Invocations {
				if inv.ParentItemID != "" && inv.ExecutionClass == harness.ExecutionTeam {
					child = inv
					break
				}
			}
			if child.ID == "" || len(started.Subtasks) == 0 {
				t.Fatal("child or subtask missing")
			}
			waiting, err := runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
				InvocationID: child.ID, Key: "input_" + mode, Kind: harness.InteractionChoice, Schema: json.RawMessage("{}"),
			})
			if err != nil || len(waiting.Interactions) == 0 {
				t.Fatalf("waiting=%+v err=%v", waiting.Turn, err)
			}
			done := make(chan error, 1)
			go func() {
				_, e := runner.ResolveInteraction(context.Background(), turnID, waiting.Interactions[0].ID,
					harness.ResolveInteractionRequest{Response: json.RawMessage("{}")})
				done <- e
			}()
			<-handler.start // Business callback dispatch grant has been committed.
			// Another root/sibling progression may project Turn=running while the
			// child Invocation and its already-dispatched response remain unchanged.
			current, err := store.GetTurn(t.Context(), turnID)
			if err != nil {
				t.Fatal(err)
			}
			current.Status = harness.TurnRunning
			current, err = store.UpdateTurn(t.Context(), current, current.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if current.Status != harness.TurnRunning {
				t.Fatal("setup didn't project Turn running")
			}
			var cancelErr error
			switch mode {
			case "CancelSubtask":
				_, cancelErr = runner.CancelSubtask(t.Context(), turnID, started.Subtasks[0].ID, "cancel claimed child effect")
			case "CancelInvocation":
				_, cancelErr = runner.CancelInvocation(t.Context(), child.ID, "cancel claimed child effect")
			default:
				_, cancelErr = runner.Cancel(t.Context(), turnID, "cancel turn with claimed child effect")
			}
			// We also check the actual Kernel Run state, not only the API status.
			state, loadErr := runtime.Load(t.Context(), child.ExecutionRefID)
			close(handler.release)
			resolutionErr := <-done
			if cancelErr == nil {
				t.Fatalf("REPRO: %s reported successful cancellation despite running Interaction callback: childRunStatus=%s callbackExecutions=%d resolutionErr=%v loadErr=%v",
					mode, state.Run.Status, handler.calls.Load(), resolutionErr, loadErr)
			}
			if loadErr == nil && state.Run.Status == kernel.RunStatusCancelled {
				t.Fatalf("REPRO: %s cancelled Kernel Run before claiming callback completed: callbacks=%d cancelErr=%v", mode, handler.calls.Load(), cancelErr)
			}
		})
	}
}

func TestIndependentVerifiedInteractionReceiptResumesRunningAgentTurn(t *testing.T) {
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
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "reconcile-running-root"}
	sid, err := harness.SessionID(hostThread)
	if err != nil {
		t.Fatal(err)
	}
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "reconcile-running-root-turn"}
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
		Key: "running-turn-receipt", Kind: harness.InteractionChoice, Schema: json.RawMessage("{}"),
	})
	if err != nil || len(waiting.Interactions) != 1 {
		t.Fatalf("setup interaction=%+v err=%v", waiting.Turn, err)
	}
	// The durable response and dispatch CAS already committed, the host effect
	// independently succeeded, but the Runner crashed before Runtime Resume.
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
	// Another allowed turn-level status projection has changed to Running.
	turn, err := store.GetTurn(t.Context(), tid)
	if err != nil {
		t.Fatal(err)
	}
	turn.Status = harness.TurnRunning
	if _, err = store.UpdateTurn(t.Context(), turn, turn.Revision); err != nil {
		t.Fatal(err)
	}
	snapshot, err := runner.ReconcileInteraction(t.Context(), tid, i.ID)
	if err != nil {
		t.Fatalf("reconcile verified host success: %v", err)
	}
	if agentFeature.resumeCalls == 0 {
		t.Fatalf("REPRO: ReconcileInteraction returned success without resuming verified Agent callback; turn=%s rootInvocation=%s", snapshot.Turn.Status, snapshot.Invocations[0].Status)
	}
}

// A parent Runtime can advance independently of a child Interaction callback.
// Refresh must not terminalize the owning Turn while the child's already-claimed
// physical business effect has an unknown outcome, even if Turn is "running".
func TestIndependentRootRefreshCannotTerminalizeInFlightChildCallback(t *testing.T) {
	_, turnID, parentItemID, relations, _, store, runtime := newFeatureInvocationHarness(t)
	handler := &auditBlockBusinessHandler{start: make(chan struct{}), release: make(chan struct{})}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
		Store: store, Clock: featureInvocationClock{}, Teams: auditRunningTeamFeature{runtime: runtime},
		Relations: relations, Interactions: handler,
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
		ParentItemID: parentItemID, RequestID: "child-refresh-in-flight", Goal: "waiting child",
		Mode: team.ExecutionSequential, Members: []team.Member{{ID: "member", Goal: "task"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var child harness.Invocation
	for _, invocation := range started.Invocations {
		if invocation.ParentItemID != "" && invocation.ExecutionClass == harness.ExecutionTeam {
			child = invocation
			break
		}
	}
	if child.ID == "" {
		t.Fatal("missing child Invocation")
	}
	waiting, err := runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
		InvocationID: child.ID, Key: "running-root-pending-child",
		Kind: harness.InteractionChoice, Schema: json.RawMessage("{}"),
	})
	if err != nil || len(waiting.Interactions) != 1 {
		t.Fatalf("waiting: %v", err)
	}
	result := make(chan error, 1)
	go func() {
		_, resolveErr := runner.ResolveInteraction(context.Background(), turnID, waiting.Interactions[0].ID,
			harness.ResolveInteractionRequest{Response: json.RawMessage("{}")})
		result <- resolveErr
	}()
	<-handler.start
	turn, err := store.GetTurn(t.Context(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	turn.Status = harness.TurnRunning
	_, err = store.UpdateTurn(t.Context(), turn, turn.Revision)
	if err != nil {
		t.Fatal(err)
	}
	root, err := store.GetInvocation(t.Context(), interactionParentInvocationID)
	if err != nil {
		t.Fatal(err)
	}
	rootRun, err := runtime.Load(t.Context(), root.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Apply(t.Context(), root.ExecutionRefID, rootRun.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: rootRun.State,
		Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage("{\"done\":true}")},
	})
	if err != nil {
		t.Fatal(err)
	}
	refreshed, refreshErr := runner.Refresh(t.Context(), turnID)
	close(handler.release)
	<-result
	if refreshErr == nil && refreshed.Turn.Status == harness.TurnCompleted {
		t.Fatalf("REPRO: Refresh completed root while unverified child callback was in flight")
	}
}

// A successful business callback is not an unbounded cancellation lock.
// Its immutable verified receipt distinguishes the settled callback from an
// unverified physical dispatch even while parent Turn / child Run stay running.
func TestIndependentVerifiedChildCallbackAllowsLaterLegitimateCancellation(t *testing.T) {
	for _, mode := range []string{"CancelSubtask", "CancelInvocation", "CancelTurn"} {
		t.Run(mode, func(t *testing.T) {
			_, turnID, parentItemID, relations, _, store, runtime := newFeatureInvocationHarness(t)
			handler := &genericEffectHandler{}
			runner, err := harness.NewRunner(harness.Dependencies{
				Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
				Store: store, Clock: featureInvocationClock{},
				Teams:     auditRunningTeamFeature{runtime: runtime},
				Relations: relations, Interactions: handler,
			})
			if err != nil {
				t.Fatal(err)
			}
			started, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
				ParentItemID: parentItemID, RequestID: "settled-child-" + mode,
				Goal: "settle child callback", Mode: team.ExecutionSequential,
				Members: []team.Member{{ID: "member", Goal: "work"}},
			})
			if err != nil || len(started.Subtasks) == 0 {
				t.Fatalf("start=%v", err)
			}
			var child harness.Invocation
			for _, invocation := range started.Invocations {
				if invocation.ParentItemID != "" && invocation.ExecutionClass == harness.ExecutionTeam {
					child = invocation
					break
				}
			}
			if child.ID == "" {
				t.Fatal("missing child Invocation")
			}
			waiting, err := runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
				InvocationID: child.ID, Key: "settled-input-" + mode,
				Kind: harness.InteractionChoice, Schema: json.RawMessage("{}"),
			})
			if err != nil || len(waiting.Interactions) != 1 {
				t.Fatalf("request=%v", err)
			}
			resolved, err := runner.ResolveInteraction(t.Context(), turnID, waiting.Interactions[0].ID,
				harness.ResolveInteractionRequest{Response: json.RawMessage("{}")})
			if err != nil {
				t.Fatalf("finish business callback=%v", err)
			}
			if handler.count.Load() != 1 {
				t.Fatalf("business callback count=%d", handler.count.Load())
			}
			if resolved.Turn.Status != harness.TurnRunning {
				t.Fatalf("resolved turn status=%s", resolved.Turn.Status)
			}
			var cancelErr error
			switch mode {
			case "CancelSubtask":
				_, cancelErr = runner.CancelSubtask(t.Context(), turnID, started.Subtasks[0].ID, "settled callback")
			case "CancelInvocation":
				_, cancelErr = runner.CancelInvocation(t.Context(), child.ID, "settled callback")
			case "CancelTurn":
				_, cancelErr = runner.Cancel(t.Context(), turnID, "settled callback")
			}
			if cancelErr != nil {
				t.Fatalf("legitimate cancellation blocked after verified business callback: %v", cancelErr)
			}
			if handler.count.Load() != 1 {
				t.Fatalf("cancel redispatched business callback: %d", handler.count.Load())
			}
		})
	}
}
