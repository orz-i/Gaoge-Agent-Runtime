package harness_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

type auditCancellationIntentBarrierStore struct {
	harness.Store
	armed   atomic.Bool
	blocked atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (s *auditCancellationIntentBarrierStore) AppendItem(ctx context.Context, item harness.Item) (harness.Item, bool, error) {
	if s.armed.Load() && item.Kind == harness.ItemSubtask &&
		bytes.Contains(item.Payload, []byte(`"cancelStatus":"requested"`)) &&
		s.blocked.CompareAndSwap(false, true) {
		close(s.entered)
		<-s.release
	}
	return s.Store.AppendItem(ctx, item)
}

type auditCancellationKernelBarrier struct {
	runtime *kernel.Runtime
	target  string
	entered chan struct{}
	release chan struct{}
	hit     atomic.Bool
}

func (b *auditCancellationKernelBarrier) Cancel(ctx context.Context, runID string, revision uint64, reason string) (kernel.Snapshot, error) {
	if runID == b.target && b.hit.CompareAndSwap(false, true) {
		close(b.entered)
		<-b.release
	}
	return b.runtime.Cancel(ctx, runID, revision, reason)
}

func TestIndependentCancellationIntentPreventsLateChildAdmission(t *testing.T) {
	for _, mode := range []string{"CancelSubtask", "CancelInvocation", "CancelTurn"} {
		t.Run(mode, func(t *testing.T) {
			_, turnID, parentItemID, relations, rootRunID, backing, runtime := newFeatureInvocationHarness(t)
			store := &auditCancellationIntentBarrierStore{Store: backing, entered: make(chan struct{}), release: make(chan struct{})}
			barrier := &auditCancellationKernelBarrier{runtime: runtime, entered: make(chan struct{}), release: make(chan struct{})}
			runner, err := harness.NewRunner(harness.Dependencies{
				Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: store,
				Clock: featureInvocationClock{}, Teams: auditRunningTeamFeature{runtime: runtime},
				Relations: relations, Interactions: noopInteractionResponseHandler{}, Cancellation: barrier,
			})
			if err != nil {
				t.Fatal(err)
			}
			started, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
				ParentItemID: parentItemID, RequestID: "parent-" + mode, Goal: "parent work",
				Mode: team.ExecutionSequential, Members: []team.Member{{ID: "member", Goal: "task"}},
			})
			if err != nil || len(started.Subtasks) == 0 {
				t.Fatalf("start=%v", err)
			}
			var parent harness.Invocation
			for _, inv := range started.Invocations {
				if inv.ParentItemID != "" && inv.ExecutionClass == harness.ExecutionTeam {
					parent = inv
					break
				}
			}
			if parent.ID == "" {
				t.Fatal("missing parent Invocation")
			}
			var entered, release chan struct{}
			if mode == "CancelSubtask" {
				store.armed.Store(true)
				entered, release = store.entered, store.release
			} else {
				if mode == "CancelTurn" {
					barrier.target = rootRunID
				} else {
					barrier.target = parent.ExecutionRefID
				}
				entered, release = barrier.entered, barrier.release
			}
			done := make(chan error, 1)
			go func() {
				switch mode {
				case "CancelSubtask":
					_, e := runner.CancelSubtask(context.Background(), turnID, started.Subtasks[0].ID, "cancel parent")
					done <- e
				case "CancelInvocation":
					_, e := runner.CancelInvocation(context.Background(), parent.ID, "cancel parent")
					done <- e
				default:
					_, e := runner.Cancel(context.Background(), turnID, "cancel root")
					done <- e
				}
			}()
			<-entered // Cancellation intent is committed, physical cancel is paused.
			now := featureInvocationClock{}.Now()
			late := harness.Invocation{
				ID: "hiv_late_" + mode, TurnID: turnID, ParentItemID: parentItemID,
				CapabilityKey: harness.CapabilityTeam, DefinitionVersion: harness.RuntimeCapabilityVersion,
				ExecutionClass: harness.ExecutionTeam, InputHash: "late", ExecutionRefID: "run_late_" + mode,
				Status: harness.InvocationRunning, Attempt: 1, Revision: 1,
				OutputRefs: []harness.HostRef{}, CreatedAt: now, UpdatedAt: now,
			}
			_, _, admitErr := store.CreateInvocation(t.Context(), late)
			close(release)
			cancelErr := <-done
			if !errors.Is(admitErr, harness.ErrConflict) {
				t.Fatalf("late child admitted after committed cancel intent: admissionErr=%v cancelErr=%v", admitErr, cancelErr)
			}
			if cancelErr != nil {
				t.Fatalf("cancel committed after rejecting late admission: %v", cancelErr)
			}
		})
	}
}

func TestIndependentCancellationIntentFencesInteractionAndInvocationStore(t *testing.T) {
	runner, turnID, parentItemID, _, _, store, _ := newFeatureInvocationHarness(t)
	waiting, err := runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
		InvocationID: interactionParentInvocationID, ParentItemID: parentItemID,
		Key: "pre-existing-wait", Kind: harness.InteractionChoice,
		Schema: json.RawMessage("{}"),
	})
	if err != nil || len(waiting.Interactions) != 1 {
		t.Fatalf("setup=%v", err)
	}
	now := featureInvocationClock{}.Now()
	intent := harness.Item{
		ID: "audit-fenced-cancel", TurnID: turnID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
		RunID: "scope-test", ParentItemID: harness.CancellationFenceParentItemID,
		Payload:   json.RawMessage("{\"boundary\":\"cancel_scope_pending\"}"),
		CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, appendErr := store.AppendItem(t.Context(), intent); appendErr != nil || !fresh {
		t.Fatalf("claim intent: %v", appendErr)
	}
	if _, err = runner.ResolveInteraction(t.Context(), turnID, waiting.Interactions[0].ID, harness.ResolveInteractionRequest{
		Response: json.RawMessage("{}"),
	}); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("response dispatch escaped cancellation intent: %v", err)
	}
	inv, err := store.GetInvocation(t.Context(), interactionParentInvocationID)
	if err != nil {
		t.Fatal(err)
	}
	inv.Status = harness.InvocationRunning
	if _, err = store.UpdateInvocation(t.Context(), inv, inv.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("Invocation CAS escaped cancellation intent: %v", err)
	}
	child := harness.Invocation{
		ID: "hiv_late_store", TurnID: turnID, ParentItemID: parentItemID,
		CapabilityKey: harness.CapabilityTeam, DefinitionVersion: harness.RuntimeCapabilityVersion,
		ExecutionClass: harness.ExecutionTeam, InputHash: "late-store", ExecutionRefID: "run-late-store",
		Status: harness.InvocationAccepted, Attempt: 1, Revision: 1, OutputRefs: []harness.HostRef{},
		CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err = store.CreateInvocation(t.Context(), child); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("late Invocation admitted: %v", err)
	}
	if _, _, err = store.AppendItem(t.Context(), harness.Item{
		ID: "audit-fenced-cancel-closed", TurnID: turnID, Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
		ParentItemID: intent.ID, Payload: json.RawMessage("{\"boundary\":\"cancel_scope_settled\"}"),
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := store.CreateInvocation(t.Context(), child); err != nil || !fresh {
		t.Fatalf("settled intent blocked new admission: fresh=%v err=%v", fresh, err)
	}
}
