package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

type boundedCancellationSettlementStore struct {
	harness.Store
	armed               atomic.Bool
	causeCancel         context.CancelFunc
	completionCalls     atomic.Int32
	unboundedReceipt    atomic.Bool
	timeoutCompletion   atomic.Bool
	failedRecordCalls   atomic.Int32
	unboundedFailure    atomic.Bool
	timeoutFailedRecord atomic.Bool
}

func (s *boundedCancellationSettlementStore) AppendItem(
	ctx context.Context, value harness.Item,
) (harness.Item, bool, error) {
	if s.armed.Load() && value.Kind == harness.ItemDiagnostic &&
		value.Status == harness.ItemCompleted && strings.HasPrefix(value.ParentItemID, "hcancel_") {
		s.completionCalls.Add(1)
		if s.causeCancel != nil {
			s.causeCancel()
		}
		if _, bounded := ctx.Deadline(); !bounded {
			s.unboundedReceipt.Store(true)
			return harness.Item{}, false, errors.New("unbounded cancellation completion receipt")
		}
		if err := ctx.Err(); err != nil {
			return harness.Item{}, false, err
		}
		if s.timeoutCompletion.Load() {
			<-ctx.Done()
			return harness.Item{}, false, ctx.Err()
		}
	}
	if s.armed.Load() && value.Kind == harness.ItemSubtask {
		var task harness.Subtask
		if json.Unmarshal(value.Payload, &task) == nil && task.CancelStatus == "failed" {
			s.failedRecordCalls.Add(1)
			if _, bounded := ctx.Deadline(); !bounded {
				s.unboundedFailure.Store(true)
				return harness.Item{}, false, errors.New("unbounded failed Subtask receipt")
			}
			if err := ctx.Err(); err != nil {
				return harness.Item{}, false, err
			}
			if s.timeoutFailedRecord.Load() {
				<-ctx.Done()
				return harness.Item{}, false, ctx.Err()
			}
		}
	}
	return s.Store.AppendItem(ctx, value)
}

func newBoundedCancellationSettlementHarness(t *testing.T) (
	*harness.Runner, *boundedCancellationSettlementStore, *harness.MemoryStore,
	*kernel.Runtime, string, string, string,
) {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	_, backing, relations, turnID, parentItem, rootRunID := newNativeCancellationSeededHarness(t, runtime)
	store := &boundedCancellationSettlementStore{Store: backing}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
		Teams: auditRunningTeamFeature{runtime: runtime},
		Store: store, Clock: featureInvocationClock{}, Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner, store, backing, runtime, turnID, parentItem, rootRunID
}

func startBoundedSettlementChild(t *testing.T, runner *harness.Runner, turnID, parentItem string) (
	string, string, string,
) {
	t.Helper()
	started, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
		ParentItemID: parentItem, RequestID: "bounded-cancel-settlement",
		Goal: "child cancellation", Mode: team.ExecutionSequential,
		Members: []team.Member{{ID: "worker", Goal: "task"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	child := teamChildInvocation(t, started)
	for _, task := range started.Subtasks {
		if task.RunID == child.ExecutionRefID {
			return child.ID, task.ID, child.ExecutionRefID
		}
	}
	t.Fatal("child Subtask not created")
	return "", "", ""
}

// Each of the three cancellation entrypoints must persist a completed
// cancellation receipt on a new deadline-bound Context, including after a
// client request disconnects just before that receipt is appended.
func TestCancellationSuccessReceiptsHaveBoundedContexts(t *testing.T) {
	for _, operation := range []string{"Cancel", "CancelInvocation", "CancelSubtask"} {
		t.Run(operation, func(t *testing.T) {
			runner, observed, backing, runtime, turnID, parentItem, runID := newBoundedCancellationSettlementHarness(t)
			var invocationID, taskID string
			if operation != "Cancel" {
				invocationID, taskID, runID = startBoundedSettlementChild(t, runner, turnID, parentItem)
			}
			requestCtx, disconnect := context.WithCancel(t.Context())
			defer disconnect()
			observed.causeCancel = disconnect
			observed.armed.Store(true)
			var resultErr error
			switch operation {
			case "CancelInvocation":
				_, resultErr = runner.CancelInvocation(requestCtx, invocationID, "bounded success completion")
			case "CancelSubtask":
				_, resultErr = runner.CancelSubtask(requestCtx, turnID, taskID, "bounded success completion")
			default:
				_, resultErr = runner.Cancel(requestCtx, turnID, "bounded success completion")
			}
			observed.armed.Store(false)
			if resultErr != nil || observed.completionCalls.Load() != 1 ||
				observed.unboundedReceipt.Load() || !errors.Is(requestCtx.Err(), context.Canceled) {
				t.Fatalf("success receipt was not independently bounded: err=%v callbacks=%d unbounded=%v disconnected=%v",
					resultErr, observed.completionCalls.Load(), observed.unboundedReceipt.Load(), requestCtx.Err())
			}
			final, err := runtime.Load(t.Context(), runID)
			if err != nil || final.Run.Status != kernel.RunStatusCancelled {
				t.Fatalf("physical cancellation did not commit: run=%s err=%v", final.Run.Status, err)
			}
			items, err := backing.ListItems(t.Context(), turnID, 0, 500)
			if err != nil {
				t.Fatal(err)
			}
			if harness.ActiveCancellationFence(items) {
				t.Fatal("successful bounded receipt left admission intent open")
			}
		})
	}
}

// Before a Host CAS attempt, a failed post-intent read may safely retire its
// own intent; the completion write still must be bounded after disconnect.
type boundedClaimReadFaultStore struct {
	*boundedCancellationSettlementStore
	armedRead atomic.Bool
	reads     atomic.Int32
	cancel    context.CancelFunc
}

func (s *boundedClaimReadFaultStore) ListInteractions(
	ctx context.Context, turnID string,
) ([]harness.Interaction, error) {
	if s.armedRead.Load() && s.reads.Add(1) == 2 {
		s.cancel()
		return nil, errCancellationClaimRead
	}
	return s.Store.ListInteractions(ctx, turnID)
}

func TestCancellationReadOnlyPostIntentClaimReceiptIsBounded(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	_, backing, _, turnID, _, runID := newNativeCancellationSeededHarness(t, runtime)
	observed := &boundedClaimReadFaultStore{
		boundedCancellationSettlementStore: &boundedCancellationSettlementStore{Store: backing},
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
		Store: observed, Clock: featureInvocationClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	disconnected, disconnect := context.WithCancel(t.Context())
	defer disconnect()
	observed.cancel = disconnect
	observed.armed.Store(true)
	observed.armedRead.Store(true)
	_, firstErr := runner.Cancel(disconnected, turnID, "read-only claim failure and request disconnect")
	observed.armedRead.Store(false)
	observed.armed.Store(false)
	if !errors.Is(firstErr, errCancellationClaimRead) || observed.completionCalls.Load() != 1 ||
		observed.unboundedReceipt.Load() || disconnected.Err() == nil {
		t.Fatalf("post-intent read failure did not get bounded completion: err=%v calls=%d unbounded=%v",
			firstErr, observed.completionCalls.Load(), observed.unboundedReceipt.Load())
	}
	items, err := backing.ListItems(t.Context(), turnID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if harness.ActiveCancellationFence(items) {
		t.Fatal("proven read-only post-intent failure stranded its bounded receipt")
	}
	run, err := runtime.Load(t.Context(), runID)
	if err != nil || run.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("pre-Host-CAS failure mutated physical Kernel: status=%s err=%v", run.Run.Status, err)
	}
	if _, err := runner.Cancel(t.Context(), turnID, "retry after bounded post-intent read failure"); err != nil {
		t.Fatalf("later operator cannot retry safely: %v", err)
	}
}

func TestCancellationSuccessReceiptTimeoutKeepsIntentForReconciliation(t *testing.T) {
	runner, observed, backing, runtime, turnID, _, runID := newBoundedCancellationSettlementHarness(t)
	observed.armed.Store(true)
	observed.timeoutCompletion.Store(true)
	started := time.Now()
	_, err := runner.Cancel(t.Context(), turnID, "physical cancel succeeds, completion blocks")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("completion timeout not reported: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 8*time.Second {
		t.Fatalf("bounded completion exceeded expected deadline: %s", elapsed)
	}
	final, loadErr := runtime.Load(t.Context(), runID)
	if loadErr != nil || final.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("physical cancellation should commit before receipt timeout: status=%s err=%v", final.Run.Status, loadErr)
	}
	items, err := backing.ListItems(t.Context(), turnID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if observed.completionCalls.Load() != 1 || !harness.ActiveCancellationFence(items) {
		t.Fatal("timed-out acknowledgement improperly completed cancellation intent")
	}
}

// After a physical CancelSubtask callback becomes outcome-unknown, the
// observational failed Subtask Item is still written on a bounded Context.
// This is NOT permission to release the unresolved cancellation intent.
type uncertainBoundedSubtaskCanceller struct {
	runtime *kernel.Runtime
	cancel  context.CancelFunc
}

func (c uncertainBoundedSubtaskCanceller) Cancel(
	ctx context.Context, runID string, expected uint64, reason string,
) (kernel.Snapshot, error) {
	_, err := c.runtime.Cancel(ctx, runID, expected, reason)
	c.cancel()
	return kernel.Snapshot{}, errors.Join(context.Canceled, err)
}

func TestCancelledSubtaskFailureRecordIsBounded(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "receipt-committed", true: "receipt-timeout"}[timeout], func(t *testing.T) {
			runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
			if err != nil {
				t.Fatal(err)
			}
			_, backing, relations, turnID, parentItem, _ := newNativeCancellationSeededHarness(t, runtime)
			observed := &boundedCancellationSettlementStore{Store: backing}
			starter, err := harness.NewRunner(harness.Dependencies{
				Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
				Teams: auditRunningTeamFeature{runtime: runtime},
				Store: observed, Clock: featureInvocationClock{}, Relations: relations,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, taskID, runID := startBoundedSettlementChild(t, starter, turnID, parentItem)
			disconnected, disconnect := context.WithCancel(t.Context())
			defer disconnect()
			canceller := uncertainBoundedSubtaskCanceller{runtime: runtime, cancel: disconnect}
			runner, err := harness.NewRunner(harness.Dependencies{
				Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
				Teams: auditRunningTeamFeature{runtime: runtime},
				Store: observed, Clock: featureInvocationClock{}, Relations: relations,
				Cancellation: canceller,
			})
			if err != nil {
				t.Fatal(err)
			}
			observed.armed.Store(true)
			observed.timeoutFailedRecord.Store(timeout)
			started := time.Now()
			_, firstErr := runner.CancelSubtask(disconnected, turnID, taskID, "remote cleanup result unknown")
			if !errors.Is(firstErr, context.Canceled) || observed.failedRecordCalls.Load() != 1 ||
				observed.unboundedFailure.Load() || disconnected.Err() == nil {
				t.Fatalf("failed Subtask receipt did not use an independent bounded Context: err=%v attempts=%d unbounded=%v",
					firstErr, observed.failedRecordCalls.Load(), observed.unboundedFailure.Load())
			}
			if timeout {
				if !errors.Is(firstErr, context.DeadlineExceeded) || time.Since(started) > 8*time.Second {
					t.Fatalf("Subtask failure receipt did not time out conservatively: %v", firstErr)
				}
			}
			current, err := runtime.Load(t.Context(), runID)
			if err != nil || current.Run.Status != kernel.RunStatusCancelled {
				t.Fatalf("fixture did not commit the physical subtask cancellation: status=%s err=%v",
					current.Run.Status, err)
			}
			items, err := backing.ListItems(t.Context(), turnID, 0, 500)
			if err != nil {
				t.Fatal(err)
			}
			if !harness.ActiveCancellationFence(items) {
				t.Fatal("uncertain Subtask callback or failed status receipt released admission")
			}
			if !timeout {
				seen := false
				for _, item := range items {
					if item.Kind != harness.ItemSubtask {
						continue
					}
					var fact harness.Subtask
					if json.Unmarshal(item.Payload, &fact) == nil &&
						fact.ID == taskID && fact.CancelStatus == "failed" {
						seen = true
					}
				}
				if !seen {
					t.Fatal("failed Subtask fact was not durably recorded")
				}
			}
		})
	}
}

// The inverse of a timed-out completion that did not commit: the Host Store
// may have committed the deterministic Item but lost its acknowledgement.
// Report the error without fabricating a "not committed" claim, and allow
// the authoritative persisted Item to distinguish the two outcomes.
type committedCancellationReceiptLostACKStore struct {
	harness.Store
	dropACK atomic.Bool
}

func (s *committedCancellationReceiptLostACKStore) AppendItem(
	ctx context.Context, value harness.Item,
) (harness.Item, bool, error) {
	if s.dropACK.Load() && value.Kind == harness.ItemDiagnostic &&
		value.Status == harness.ItemCompleted &&
		strings.HasPrefix(value.ParentItemID, "hcancel_") {
		s.dropACK.Store(false)
		_, _, err := s.Store.AppendItem(ctx, value)
		if err != nil {
			return harness.Item{}, false, err
		}
		return harness.Item{}, false, context.DeadlineExceeded
	}
	return s.Store.AppendItem(ctx, value)
}

func TestCancellationCompletionLostACKRequiresDurableReceiptEvidence(t *testing.T) {
	physical := &nativeCancellationReadFaultStore{Store: memory.NewStore()}
	runtime, err := kernel.New(kernel.Dependencies{Store: physical})
	if err != nil {
		t.Fatal(err)
	}
	_, backing, _, turnID, _, runID := newNativeCancellationSeededHarness(t, runtime)
	store := &committedCancellationReceiptLostACKStore{Store: backing}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
		Store: store, Clock: featureInvocationClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	store.dropACK.Store(true)
	_, firstErr := runner.Cancel(t.Context(), turnID, "cancel physically but lose completion ACK")
	if !errors.Is(firstErr, context.DeadlineExceeded) || physical.controlCalls.Load() != 1 {
		t.Fatalf("committed receipt/lost ACK fixture failed: err=%v controlCalls=%d",
			firstErr, physical.controlCalls.Load())
	}
	items, err := backing.ListItems(t.Context(), turnID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if harness.ActiveCancellationFence(items) {
		t.Fatal("a successfully committed receipt was mistaken for a missing receipt")
	}
	run, err := runtime.Load(t.Context(), runID)
	if err != nil || run.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("fixture did not commit Kernel cancellation: status=%s err=%v", run.Run.Status, err)
	}
	// The persisted receipt is sufficient to distinguish lost ACK from
	// absent write. Never reissue the physical Store CAS on later inspection.
	_, _ = runner.Cancel(t.Context(), turnID, "reinspect after lost completion ACK")
	if calls := physical.controlCalls.Load(); calls != 1 {
		t.Fatalf("reinspection duplicated physical Kernel cancellation: calls=%d", calls)
	}
}
