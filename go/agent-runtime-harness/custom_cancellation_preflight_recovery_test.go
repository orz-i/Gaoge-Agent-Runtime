package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

// A host-owned Canceller must not be called after the Harness's own
// Kernel-read failure. Its type has no bearing on that pre-dispatch proof.
type customPreflightCancellationRecorder struct {
	runtime *kernel.Runtime
	calls   atomic.Int32
}

func (c *customPreflightCancellationRecorder) Cancel(
	ctx context.Context, runID string, revision uint64, reason string,
) (kernel.Snapshot, error) {
	c.calls.Add(1)
	return c.runtime.Cancel(ctx, runID, revision, reason)
}

type customPreflightReadFailureStore struct {
	*memory.Store
	armed        atomic.Bool
	reads        atomic.Int32
	failAt       int32
	cancel       context.CancelFunc
	controlCalls atomic.Int32
}

func (s *customPreflightReadFailureStore) Load(ctx context.Context, id string) (kernel.Snapshot, error) {
	if s.armed.Load() && s.reads.Add(1) == s.failAt {
		s.cancel()
		return kernel.Snapshot{}, ctx.Err()
	}
	return s.Store.Load(ctx, id)
}

func (s *customPreflightReadFailureStore) ApplyControl(
	ctx context.Context, id string, revision uint64, guard kernel.ControlGuard,
	mutation kernel.StoreMutation,
) (kernel.Snapshot, bool, error) {
	s.controlCalls.Add(1)
	return s.Store.ApplyControl(ctx, id, revision, guard, mutation)
}

func TestCustomCancellerPreflightReadFailureAllowsSafeRetry(t *testing.T) {
	for _, operation := range []string{"Cancel", "CancelInvocation", "CancelSubtask"} {
		for _, initial := range []kernel.RunStatus{
			kernel.RunStatusRunning, kernel.RunStatusPausedBudget, kernel.RunStatusWaitingInput,
		} {
			t.Run(operation+"/"+string(initial), func(t *testing.T) {
				physical := &customPreflightReadFailureStore{Store: memory.NewStore()}
				runtime, err := kernel.New(kernel.Dependencies{Store: physical})
				if err != nil {
					t.Fatal(err)
				}
				_, store, relations, turnID, parentItem, rootRunID := newNativeCancellationSeededHarness(t, runtime)
				custom := &customPreflightCancellationRecorder{runtime: runtime}
				runner, err := harness.NewRunner(harness.Dependencies{
					Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
					Teams: auditRunningTeamFeature{runtime: runtime},
					Store: store, Clock: featureInvocationClock{}, Relations: relations,
					Cancellation: custom,
				})
				if err != nil {
					t.Fatal(err)
				}
				targetRunID := rootRunID
				var invocationID, taskID string
				if operation != "Cancel" {
					started, startErr := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
						ParentItemID: parentItem, RequestID: "custom-preflight-" + operation,
						Goal: "cancellable child", Mode: team.ExecutionSequential,
						Members: []team.Member{{ID: "worker", Goal: "task"}},
					})
					if startErr != nil {
						t.Fatal(startErr)
					}
					child := teamChildInvocation(t, started)
					invocationID, targetRunID = child.ID, child.ExecutionRefID
					for _, task := range started.Subtasks {
						if task.RunID == targetRunID {
							taskID = task.ID
						}
					}
					if taskID == "" {
						t.Fatal("missing child Subtask")
					}
				}
				current, err := runtime.Load(t.Context(), targetRunID)
				if err != nil {
					t.Fatal(err)
				}
				if initial != kernel.RunStatusRunning {
					mutation := kernel.Mutation{Status: initial, State: current.State}
					if initial == kernel.RunStatusPausedBudget {
						mutation.ErrorCode = "audit.paused_budget"
					} else {
						mutation.Checkpoint = &kernel.Checkpoint{
							ID: "custom-cancel-wait", Kind: "audit.wait",
							Status: kernel.CheckpointPending, Payload: json.RawMessage("{}"),
							CreatedAt: featureInvocationClock{}.Now(),
						}
					}
					if _, err := runtime.Apply(t.Context(), targetRunID, current.Run.Revision, mutation); err != nil {
						t.Fatal(err)
					}
				}
				cancel := func(ctx context.Context) error {
					switch operation {
					case "CancelInvocation":
						_, err := runner.CancelInvocation(ctx, invocationID, "custom preflight")
						return err
					case "CancelSubtask":
						_, err := runner.CancelSubtask(ctx, turnID, taskID, "custom preflight")
						return err
					default:
						_, err := runner.Cancel(ctx, turnID, "custom preflight")
						return err
					}
				}
				disconnected, abort := context.WithCancel(t.Context())
				defer abort()
				physical.cancel = abort
				physical.failAt = 1
				if operation == "CancelSubtask" {
					physical.failAt = 2 // findSubtask loads before acquiring its intent.
				}
				physical.armed.Store(true)
				firstErr := cancel(disconnected)
				physical.armed.Store(false)
				if !errors.Is(firstErr, context.Canceled) ||
					custom.calls.Load() != 0 || physical.controlCalls.Load() != 0 ||
					physical.reads.Load() < physical.failAt {
					t.Fatalf("cancellation was not rejected before custom dispatch: err=%v calls=%d CAS=%d reads=%d",
						firstErr, custom.calls.Load(), physical.controlCalls.Load(), physical.reads.Load())
				}
				unchanged, err := runtime.Load(t.Context(), targetRunID)
				if err != nil || unchanged.Run.Status != initial {
					t.Fatalf("pre-dispatch failure modified Kernel: status=%s err=%v", unchanged.Run.Status, err)
				}
				items, err := store.ListItems(t.Context(), turnID, 0, 500)
				if err != nil {
					t.Fatal(err)
				}
				foundIntent := false
				for _, item := range items {
					if item.ParentItemID == harness.CancellationFenceParentItemID &&
						item.Kind == harness.ItemDiagnostic && item.Status == harness.ItemStarted {
						foundIntent = true
						break
					}
				}
				if !foundIntent {
					t.Fatal("read error happened before any durable cancellation intent")
				}
				if harness.ActiveCancellationFence(items) {
					t.Fatal("a proven first-read no-effect cancellation left its intent unresolved")
				}
				if err = cancel(t.Context()); err != nil {
					t.Fatalf("valid retry after proven no-effect: %v", err)
				}
				final, loadErr := runtime.Load(t.Context(), targetRunID)
				if loadErr != nil || final.Run.Status != kernel.RunStatusCancelled || custom.calls.Load() != 1 {
					t.Fatalf("retry was not a single physical cancellation: run=%s calls=%d err=%v",
						final.Run.Status, custom.calls.Load(), loadErr)
				}
			})
		}
	}
}

// The proof for the Harness's own initial read must NOT be synthesized from
// an arbitrary callback outcome. Once a custom Canceller is invoked, an
// unacknowledged physical CAS stays fenced for explicit reconciliation.
type customCommittedUnknownCancellation struct {
	runtime *kernel.Runtime
	calls   atomic.Int32
}

func (c *customCommittedUnknownCancellation) Cancel(
	ctx context.Context, runID string, revision uint64, reason string,
) (kernel.Snapshot, error) {
	c.calls.Add(1)
	if _, err := c.runtime.Cancel(ctx, runID, revision, reason); err != nil {
		return kernel.Snapshot{}, err
	}
	return kernel.Snapshot{}, context.Canceled
}

func TestCustomCancellerUnknownAfterPhysicalCallRemainsFenced(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	_, store, _, turnID, _, runID := newNativeCancellationSeededHarness(t, runtime)
	custom := &customCommittedUnknownCancellation{runtime: runtime}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
		Store: store, Clock: featureInvocationClock{}, Cancellation: custom,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, firstErr := runner.Cancel(t.Context(), turnID, "lose custom cancellation acknowledgement")
	if !errors.Is(firstErr, context.Canceled) || custom.calls.Load() != 1 {
		t.Fatalf("custom cancellation was not invoked before outcome uncertainty: %v calls=%d",
			firstErr, custom.calls.Load())
	}
	final, err := runtime.Load(t.Context(), runID)
	if err != nil || final.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("fixture did not commit the physical CAS: status=%s err=%v", final.Run.Status, err)
	}
	items, err := store.ListItems(t.Context(), turnID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if !harness.ActiveCancellationFence(items) {
		t.Fatal("unknown custom physical outcome discharged the admission intent")
	}
}

// Even when the read-only failure is definitely pre-dispatch, earlier Host
// Interaction mutations cannot be rolled back by releasing the Turn intent.
func TestCustomCancellerPreflightAfterInteractionMutationRemainsFenced(t *testing.T) {
	physical := &customPreflightReadFailureStore{Store: memory.NewStore()}
	runtime, err := kernel.New(kernel.Dependencies{Store: physical})
	if err != nil {
		t.Fatal(err)
	}
	_, store, relations, turnID, parentItem, rootRunID := newNativeCancellationSeededHarness(t, runtime)
	custom := &customPreflightCancellationRecorder{runtime: runtime}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
		Store: store, Clock: featureInvocationClock{}, Relations: relations,
		Cancellation: custom,
	})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
		InvocationID: "hiv_parent", ParentItemID: parentItem,
		Key: "custom-noeffect-partial-interaction", Kind: harness.InteractionChoice,
		Schema: json.RawMessage("{}"),
	})
	if err != nil || len(waiting.Interactions) != 1 {
		t.Fatalf("did not seed Host Interaction: status=%s err=%v", waiting.Turn.Status, err)
	}
	disconnected, abort := context.WithCancel(t.Context())
	defer abort()
	physical.cancel = abort
	physical.failAt = 1
	physical.armed.Store(true)
	_, firstErr := runner.Cancel(disconnected, turnID, "Host Interaction mutated before cancelled request")
	physical.armed.Store(false)
	if !errors.Is(firstErr, context.Canceled) || custom.calls.Load() != 0 ||
		physical.controlCalls.Load() != 0 {
		t.Fatalf("did not fail at first read without effects: err=%v calls=%d CAS=%d",
			firstErr, custom.calls.Load(), physical.controlCalls.Load())
	}
	items, err := store.ListItems(t.Context(), turnID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if !harness.ActiveCancellationFence(items) {
		t.Fatal("a previously modified Host Interaction was misclassified as no-effect")
	}
	owner, err := store.GetInvocation(t.Context(), "hiv_parent")
	if err != nil || owner.Status != harness.InvocationCancelled {
		t.Fatalf("Interaction cancellation mutation missing: status=%s err=%v", owner.Status, err)
	}
	physicalRun, err := runtime.Load(t.Context(), rootRunID)
	if err != nil || physicalRun.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("pre-dispatch cancellation changed physical Kernel: status=%s err=%v", physicalRun.Run.Status, err)
	}
}
