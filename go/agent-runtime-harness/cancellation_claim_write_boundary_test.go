package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

var errCancellationClaimLostACK = errors.New("injected invocation cancellation CAS committed but reply lost")

type claimCommittedLostReplyStore struct {
	harness.Store
	armed           atomic.Bool
	completedWrites atomic.Int32
}

func (s *claimCommittedLostReplyStore) UpdateInvocation(
	ctx context.Context, invocation harness.Invocation, expectedRevision uint64,
) (harness.Invocation, error) {
	updated, err := s.Store.UpdateInvocation(ctx, invocation, expectedRevision)
	if err == nil && invocation.Status == harness.InvocationCancelled && s.armed.CompareAndSwap(true, false) {
		s.completedWrites.Add(1)
		return harness.Invocation{}, errCancellationClaimLostACK
	}
	return updated, err
}

func TestCancellationClaimCommittedHostCASLostReplyKeepsIntent(t *testing.T) {
	_, turnID, parentItem, relations, rootRunID, backing, rt := newFeatureInvocationHarness(t)
	store := &claimCommittedLostReplyStore{Store: backing}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: rt, Agent: loadingFeatureAgent{runtime: rt}, Store: store,
		Clock: featureInvocationClock{}, Relations: relations,
		Interactions: noopInteractionResponseHandler{},
	})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
		InvocationID: "hiv_parent", ParentItemID: parentItem,
		Key: "partial-cancel-claim-response", Kind: harness.InteractionChoice, Schema: json.RawMessage("{}"),
	})
	if err != nil || len(waiting.Interactions) != 1 {
		t.Fatalf("setup Interaction waiting failed: status=%s err=%v", waiting.Turn.Status, err)
	}
	store.armed.Store(true)
	_, firstErr := runner.Cancel(t.Context(), turnID, "cancel waiting Interaction")
	if !errors.Is(firstErr, errCancellationClaimLostACK) || store.completedWrites.Load() != 1 {
		t.Fatalf("failed to inject after committed Host Invocation CAS: err=%v writes=%d", firstErr, store.completedWrites.Load())
	}
	owner, err := backing.GetInvocation(t.Context(), "hiv_parent")
	if err != nil || owner.Status != harness.InvocationCancelled {
		t.Fatalf("committed mutation missing: status=%s err=%v", owner.Status, err)
	}
	run, err := rt.Load(t.Context(), rootRunID)
	if err != nil || run.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("physical Kernel cancellation happened even though claim failed: run=%s err=%v", run.Run.Status, err)
	}
	items, err := backing.ListItems(t.Context(), turnID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if !harness.ActiveCancellationFence(items) {
		t.Fatalf("unsafe release: claim() committed InvocationCancelled but lost its ACK; cancelPreflightWithIntent treated the failed post-intent claim as no effect and CLOSED cancellation fence despite partial Host CAS; Kernel still %s, Host Invocation=%s", run.Run.Status, owner.Status)
	}
}

func TestChildCancellationClaimCommittedHostCASLostReplyKeepsIntent(t *testing.T) {
	for _, operation := range []string{"CancelInvocation", "CancelSubtask"} {
		t.Run(operation, func(t *testing.T) {
			_, turnID, parentItem, relations, _, backing, rt := newFeatureInvocationHarness(t)
			store := &claimCommittedLostReplyStore{Store: backing}
			runner, err := harness.NewRunner(harness.Dependencies{
				Runtime: rt, Agent: loadingFeatureAgent{runtime: rt},
				Store: store, Clock: featureInvocationClock{}, Relations: relations,
				Teams:        auditRunningTeamFeature{runtime: rt},
				Interactions: noopInteractionResponseHandler{},
			})
			if err != nil {
				t.Fatal(err)
			}
			started, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
				ParentItemID: parentItem, RequestID: "claim-lost-" + operation, Goal: "child cancel", Mode: team.ExecutionSequential,
				Members: []team.Member{{ID: "worker", Goal: "work"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			child := teamChildInvocation(t, started)
			taskID, childItemID := "", ""
			for _, task := range started.Subtasks {
				if task.RunID == child.ExecutionRefID {
					taskID = task.ID
					break
				}
			}
			for _, item := range started.Items {
				if item.InvocationID == child.ID && item.Kind == harness.ItemInvocation {
					childItemID = item.ID
					break
				}
			}
			if taskID == "" || childItemID == "" {
				t.Fatalf("missing child Subtask/Item: task=%s item=%s", taskID, childItemID)
			}
			waiting, err := runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
				InvocationID: child.ID, ParentItemID: childItemID, Key: "claim-acklost-" + operation,
				Kind: harness.InteractionChoice, Schema: json.RawMessage("{}"),
			})
			if err != nil || waiting.Turn.Status != harness.TurnWaitingInput {
				t.Fatalf("couldn't seed waiting child Host Interaction: status=%s err=%v", waiting.Turn.Status, err)
			}
			store.armed.Store(true)
			var firstErr error
			if operation == "CancelInvocation" {
				_, firstErr = runner.CancelInvocation(t.Context(), child.ID, "child CAS committed without ACK")
			} else {
				_, firstErr = runner.CancelSubtask(t.Context(), turnID, taskID, "subtask CAS committed without ACK")
			}
			if !errors.Is(firstErr, errCancellationClaimLostACK) || store.completedWrites.Load() != 1 {
				t.Fatalf("injection didn't commit Host CAS: op=%s err=%v writes=%d", operation, firstErr, store.completedWrites.Load())
			}
			changed, err := backing.GetInvocation(t.Context(), child.ID)
			if err != nil || changed.Status != harness.InvocationCancelled {
				t.Fatalf("Host CAS didn't commit: status=%s err=%v", changed.Status, err)
			}
			run, err := rt.Load(t.Context(), child.ExecutionRefID)
			if err != nil || run.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("child Kernel control unexpectedly applied: status=%s err=%v", run.Run.Status, err)
			}
			items, err := backing.ListItems(t.Context(), turnID, 0, 500)
			if err != nil {
				t.Fatal(err)
			}
			if !harness.ActiveCancellationFence(items) {
				t.Fatalf("unsafe release in %s: Host InvocationCancelled CAS committed but reply lost, cancellation preflight prematurely COMPLETED intent with child Kernel still %s", operation, run.Run.Status)
			}
		})
	}
}

// A read-only error in the *post-intent* reinspection, before any Host
// Store UpdateInvocation CAS, may still retire only this fresh intent.
// This preserves recovery from transient DB read faults without treating
// arbitrary errors AFTER a Host CAS as no-effect.
var errCancellationClaimRead = errors.New("injected post-intent cancellation read failure before any Host CAS")

type claimReadOnlyFaultStore struct {
	harness.Store
	armed atomic.Bool
	reads atomic.Int32
}

func (s *claimReadOnlyFaultStore) ListInteractions(
	ctx context.Context, turnID string,
) ([]harness.Interaction, error) {
	if s.armed.Load() && s.reads.Add(1) == 2 {
		return nil, errCancellationClaimRead
	}
	return s.Store.ListInteractions(ctx, turnID)
}

func TestReadOnlyPostIntentClaimFailureRetiresFreshCancellationIntent(t *testing.T) {
	for _, operation := range []string{"Cancel", "CancelInvocation", "CancelSubtask"} {
		t.Run(operation, func(t *testing.T) {
			runtime := newFeatureInvocationRuntime(t)
			backing := harness.NewMemoryStore()
			relations := newFeatureInvocationRelations(t)
			now := featureInvocationClock{}.Now()
			hostThread := harness.HostRef{Kind: testThreadKind, ID: "claim-read-only"}
			sessionID, err := harness.SessionID(hostThread)
			if err != nil {
				t.Fatal(err)
			}
			hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "claim-read-only"}
			turnID, err := harness.TurnID(sessionID, hostTurn)
			if err != nil {
				t.Fatal(err)
			}
			actor := kernel.ActorRef{TenantID: testTenant, ActorID: testActor}
			thread := kernel.ThreadRef{Kind: testThreadKind, ID: hostThread.ID}
			seedFeatureInvocationEnvelope(t, backing, sessionID, turnID, hostThread, hostTurn, actor, now)
			rootRunID, parentItem := seedFeatureInvocationParent(t, runtime, backing, turnID, actor, thread, now)
			fault := &claimReadOnlyFaultStore{Store: backing}
			runner, err := harness.NewRunner(harness.Dependencies{
				Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
				Teams: auditRunningTeamFeature{runtime: runtime}, Relations: relations,
				Store: fault, Clock: featureInvocationClock{},
			})
			if err != nil {
				t.Fatal(err)
			}
			targetRunID := rootRunID
			var childID, taskID string
			if operation != "Cancel" {
				started, startErr := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
					ParentItemID: parentItem, RequestID: "claim-read-" + operation,
					Goal: "cancel child after read failure", Mode: team.ExecutionSequential,
					Members: []team.Member{{ID: "worker", Goal: "task"}},
				})
				if startErr != nil {
					t.Fatal(startErr)
				}
				child := teamChildInvocation(t, started)
				targetRunID, childID = child.ExecutionRefID, child.ID
				for _, task := range started.Subtasks {
					if task.RunID == targetRunID {
						taskID = task.ID
						break
					}
				}
				if taskID == "" {
					t.Fatal("no child Subtask")
				}
			}
			cancel := func() error {
				switch operation {
				case "CancelInvocation":
					_, cancelErr := runner.CancelInvocation(t.Context(), childID, "read-only cancellation fault")
					return cancelErr
				case "CancelSubtask":
					_, cancelErr := runner.CancelSubtask(t.Context(), turnID, taskID, "read-only cancellation fault")
					return cancelErr
				default:
					_, cancelErr := runner.Cancel(t.Context(), turnID, "read-only cancellation fault")
					return cancelErr
				}
			}
			fault.armed.Store(true)
			firstErr := cancel()
			fault.armed.Store(false)
			if !errors.Is(firstErr, errCancellationClaimRead) || fault.reads.Load() != 2 {
				t.Fatalf("did not inject post-intent read-only failure: err=%v reads=%d", firstErr, fault.reads.Load())
			}
			items, err := backing.ListItems(t.Context(), turnID, 0, 500)
			if err != nil {
				t.Fatal(err)
			}
			if harness.ActiveCancellationFence(items) {
				t.Fatal("fresh intent remained open despite read-only claim failure")
			}
			current, err := runtime.Load(t.Context(), targetRunID)
			if err != nil || current.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("read-only preflight changed physical Run: status=%s err=%v", current.Run.Status, err)
			}
			if retryErr := cancel(); retryErr != nil {
				t.Fatalf("valid retry after proven pre-write failure blocked: %v", retryErr)
			}
			final, err := runtime.Load(t.Context(), targetRunID)
			if err != nil || final.Run.Status != kernel.RunStatusCancelled {
				t.Fatalf("retry did not cancel Kernel Run: status=%s err=%v", final.Run.Status, err)
			}
		})
	}
}

// Even a Host Store CAS that rejects BEFORE writing cannot certify that no
// effect occurred from its error alone. A callback/Store may report conflict
// after committing, so the mark-before-Store boundary stays fail-closed.
type rejectedHostCancellationCASStore struct {
	harness.Store
	attempts atomic.Int32
}

func (s *rejectedHostCancellationCASStore) UpdateInvocation(
	ctx context.Context, value harness.Invocation, expectedRevision uint64,
) (harness.Invocation, error) {
	if value.Status == harness.InvocationCancelled {
		s.attempts.Add(1)
		return harness.Invocation{}, harness.ErrConflict
	}
	return s.Store.UpdateInvocation(ctx, value, expectedRevision)
}

func TestRejectedHostCancellationCASStillKeepsIntent(t *testing.T) {
	_, turnID, parentItem, relations, runID, backing, runtime := newFeatureInvocationHarness(t)
	store := &rejectedHostCancellationCASStore{Store: backing}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
		Store: store, Clock: featureInvocationClock{}, Relations: relations,
		Interactions: noopInteractionResponseHandler{},
	})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
		InvocationID: "hiv_parent", ParentItemID: parentItem,
		Key: "reject-host-cas-before-write", Kind: harness.InteractionChoice,
		Schema: json.RawMessage("{}"),
	})
	if err != nil || len(waiting.Interactions) != 1 {
		t.Fatalf("failed to prepare waiting Interaction: err=%v", err)
	}
	_, err = runner.Cancel(t.Context(), turnID, "Host CAS reports conflict before writing")
	if !errors.Is(err, harness.ErrConflict) || store.attempts.Load() != 1 {
		t.Fatalf("Host CAS rejection not exercised: err=%v attempts=%d", err, store.attempts.Load())
	}
	owner, err := backing.GetInvocation(t.Context(), "hiv_parent")
	if err != nil || owner.Status != harness.InvocationWaitingInput {
		t.Fatalf("fixture unexpectedly changed Host Invocation: status=%s err=%v", owner.Status, err)
	}
	run, err := runtime.Load(t.Context(), runID)
	if err != nil || run.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("fixture unexpectedly changed Kernel Run: status=%s err=%v", run.Run.Status, err)
	}
	items, err := backing.ListItems(t.Context(), turnID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if !harness.ActiveCancellationFence(items) {
		t.Fatal("a Host CAS ErrConflict was incorrectly treated as proven no-effect")
	}
}
