package harnesspostgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func ea1HACMatrix(t *testing.T, run func(*testing.T, *ea1HACFixture)) {
	t.Helper()
	for _, backend := range []string{"memory", "sqlite", "postgres"} {
		t.Run("kernel_"+backend, func(t *testing.T) {
			for _, host := range []bool{false, true} {
				label := "host_memory"
				if host {
					label = "host_postgres"
				}
				t.Run(label, func(t *testing.T) { run(t, newEA1HACFixture(t, backend, host)) })
			}
		})
	}
}
func TestEA1HACReceiptBeforeMark(t *testing.T) { ea1HACMatrix(t, ea1HACReceiptBeforeMark) }
func ea1HACReceiptBeforeMark(t *testing.T, f *ea1HACFixture) {
	waiting, task, root, ledger := ea1HACStart(t, f)
	child := ea1HACApprove(t, f, waiting, task)
	f.physical.markEntered = make(chan kernel.ChildExecutionGrant, 1)
	f.physical.markRelease = make(chan struct{})
	f.physical.markArmed.Store(true)
	var once sync.Once
	release := func() { once.Do(func() { close(f.physical.markRelease) }) }
	t.Cleanup(release)
	done := make(chan error, 1)
	go func() { _, e := f.agent.Resume(context.Background(), task.RunID, child.Run.Revision); done <- e }()
	var intercepted kernel.ChildExecutionGrant
	select {
	case intercepted = <-f.physical.markEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("original child Mark not intercepted")
	}
	child = ea1HACLoad(t, f, task.RunID)
	parent := ea1HACLoad(t, f, root.ExecutionRefID)
	grant, err := f.runtime.LookupChildExecution(t.Context(), task.RunID)
	if err != nil || grant != intercepted || grant.ChildRunID != task.RunID || f.physical.markHits.Load() != 1 {
		t.Fatal("intercepted wrong physical child scope")
	}
	ea1HACAdmissionModel(t, f, child)
	ea1HACAssertPeer(t, f, child, grant)
	if child.Run.Status != kernel.RunStatusCompleted || f.toolCalls != 1 || !reflect.DeepEqual(f.toolRequest, ea1HACExpectedToolRequest(task.RunID)) || !reflect.DeepEqual(f.toolResult, tools.ExecutionResult{Content: json.RawMessage(`{"evidence":"approved"}`), Receipt: tools.Receipt{ExecutionID: "ea1HAC-call", Disposition: "read"}}) || !reflect.DeepEqual(child.Result, &kernel.Result{ContentType: "application/json", Content: f.toolResult.Content}) {
		t.Fatal("actual terminal Tool receipt was not committed before original Mark")
	}
	events, eventErr := f.runtime.ListEvents(t.Context(), task.RunID, 0, 500)
	if eventErr != nil {
		t.Fatal(eventErr)
	}
	receiptEvents := 0
	for _, event := range events {
		if event.Type == "tool.completed" {
			receiptEvents++
			if event.Message != f.toolResult.Receipt.Disposition {
				t.Fatal("durable Tool receipt disposition differs from actual handler return")
			}
		}
	}
	if receiptEvents != 1 {
		t.Fatalf("original Tool completion event count=%d", receiptEvents)
	}
	if err = f.agent.ReconcileChildPhysicalGrant(t.Context(), task.RunID); !errors.Is(err, kernel.ErrChildExecutionBusy) {
		t.Fatalf("receipt alone retired live callback: %v", err)
	}
	_, err = f.harness.CancelSubtask(t.Context(), waiting.Turn.ID, task.ID, "confirm original settled Tool while Mark pending")
	if err != nil {
		t.Fatalf("verified terminal observation incorrectly rejected: %v", err)
	}
	ea1HACAssertOwner(t, f, root, parent, child)
	retained, e := f.runtime.LookupChildExecution(t.Context(), task.RunID)
	if e != nil || retained != grant || f.physical.controlCalls.Load() != 0 {
		t.Fatal("Host confirmation replaced physical settlement or issued Cancel")
	}
	if !reflect.DeepEqual(ea1HACExpectedBudget(ledger, task.RunID), ea1HACBudget(t, f.backend.ledgers, waiting.Turn.ID)) {
		t.Fatal("terminal confirmation altered unrelated budget")
	}
	release()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("original callback did not retire")
	}
	if _, err = f.runtime.LookupChildExecution(t.Context(), task.RunID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("original marked scope failed to retire: %v", err)
	}
	if !reflect.DeepEqual(child, ea1HACLoad(t, f, task.RunID)) || !reflect.DeepEqual(parent, ea1HACLoad(t, f, root.ExecutionRefID)) {
		t.Fatal("Mark/retirement changed original receipt snapshot")
	}
	if f.toolCalls != 1 || f.model.calls != 1 || f.physical.markHits.Load() != 1 {
		t.Fatal("receipt completion replayed physical work")
	}
}

// G11 starts from actual busy Tool execution, never a seeded cancellation fact.
func TestEA1HACCancelledBudgetSameIntentAcrossStoreRestart(t *testing.T) {
	ea1HACMatrix(t, ea1HACBusyReceiptRecovery)
}
func ea1HACBusyReceiptRecovery(t *testing.T, f *ea1HACFixture) {
	waiting, task, root, before := ea1HACStart(t, f)
	approved := ea1HACApprove(t, f, waiting, task)
	f.toolStarted = make(chan struct{})
	f.toolRelease = make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(f.toolRelease) }) }
	t.Cleanup(release)
	done := make(chan error, 1)
	go func() { _, err := f.agent.Resume(context.Background(), task.RunID, approved.Run.Revision); done <- err }()
	select {
	case <-f.toolStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("actual original Tool send not reached")
	}
	if f.toolCalls != 1 || !reflect.DeepEqual(f.toolRequest, ea1HACExpectedToolRequest(task.RunID)) {
		t.Fatal("wrong original Tool physical request")
	}
	busyChild := ea1HACLoad(t, f, task.RunID)
	parent := ea1HACLoad(t, f, root.ExecutionRefID)
	grant, err := f.runtime.LookupChildExecution(t.Context(), task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	ea1HACAssertPeer(t, f, busyChild, grant)
	fault := &ea1ChildCancelReceiptACK{Store: f.store, child: task.RunID}
	candidate := ea1HACRunner(t, f, fault, f.backend.ledgers, f.relations)
	_, err = candidate.CancelSubtask(t.Context(), waiting.Turn.ID, task.ID, "cancel original live Tool")
	if !errors.Is(err, kernel.ErrChildExecutionBusy) {
		t.Fatalf("live Tool cancel=%v", err)
	}
	items := ea1HACItems(t, f.store, waiting.Turn.ID)
	original := ea1OnlyCancelIntent(t, items, waiting.Turn.ID, task.RunID)
	if !ea1ChildCancelItemFullEqual(original, fault.intent) || !harness.ActiveCancellationFence(items) || len(ea1CancelReceipts(items, original.ID)) != 0 {
		t.Fatal("busy cancellation did not retain the original unsettled election")
	}
	ledger := ea1HACBudget(t, f.backend.ledgers, waiting.Turn.ID)
	if !reflect.DeepEqual(ledger, ea1HACExpectedBudget(before, task.RunID)) {
		t.Fatal("busy cancellation changed unrelated budget fields")
	}
	ea1HACAssertOwner(t, f, root, parent, busyChild)
	retained, e := f.runtime.LookupChildExecution(t.Context(), task.RunID)
	if e != nil || retained != grant {
		t.Fatal("busy cancellation changed original grant")
	}
	if _, err = candidate.CancelSubtask(t.Context(), waiting.Turn.ID, task.ID, "repeat while original Tool busy"); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("repeat busy election=%v", err)
	}
	if !reflect.DeepEqual(items, ea1HACItems(t, f.store, waiting.Turn.ID)) || !reflect.DeepEqual(ledger, ea1HACBudget(t, f.backend.ledgers, waiting.Turn.ID)) || f.physical.controlCalls.Load() != 1 {
		t.Fatal("repeat election mutated original facts")
	}
	release()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("original Tool callback did not finish")
	}
	child := ea1HACLoad(t, f, task.RunID)
	ea1HACAssertToolReceipt(t, f, task.RunID, child)
	if _, err = f.runtime.LookupChildExecution(t.Context(), task.RunID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("original Tool settlement did not retire: %v", err)
	}
	// Parent+Host cancellation scope remain the original ones; Tool completion
	// does not silently create a new cancellation or restore revoked budget.
	if !reflect.DeepEqual(parent, ea1HACLoad(t, f, root.ExecutionRefID)) || !reflect.DeepEqual(ledger, ea1HACBudget(t, f.backend.ledgers, waiting.Turn.ID)) {
		t.Fatal("Tool settlement changed parent/budget")
	}
	_, err = candidate.CancelSubtask(t.Context(), waiting.Turn.ID, task.ID, "resume original cancellation after original receipt")
	if !errors.Is(err, context.DeadlineExceeded) || fault.hits.Load() != 1 {
		t.Fatalf("actual original cancellation receipt ACK fault: %v hits=%d", err, fault.hits.Load())
	}
	items = ea1HACItems(t, f.store, waiting.Turn.ID)
	if !ea1ChildCancelItemFullEqual(original, ea1OnlyCancelIntent(t, items, waiting.Turn.ID, task.RunID)) || !harness.VerifiedCancellationFenceReceipt(original, ea1CancelReceipts(items, original.ID)) || harness.ActiveCancellationFence(items) {
		t.Fatal("retry did not settle the same original election")
	}
	actualReceipt, err := f.store.GetItem(t.Context(), fault.receipt.ID)
	if err != nil || !ea1ChildCancelItemFullEqual(actualReceipt, fault.receipt) {
		t.Fatalf("actual committed receipt differs: %v", err)
	}
	ea1HACAssertOwner(t, f, root, parent, child)
	if !reflect.DeepEqual(ledger, ea1HACBudget(t, f.backend.ledgers, waiting.Turn.ID)) || f.physical.controlCalls.Load() != 1 {
		t.Fatal("terminal receipt confirmation replayed Control or reset budget")
	}
	// Preserve original source artifacts, but rebuild all execution owners from
	// reopened authoritative Host+Kernel+relation Stores. Memory is a fresh-owner
	// control, not evidence of process persistence.

	if !reflect.DeepEqual(items, ea1HACItems(t, f.backend.peer, waiting.Turn.ID)) || !reflect.DeepEqual(ledger, ea1HACBudget(t, f.backend.peerLedgers, waiting.Turn.ID)) {
		t.Fatal("independent Host peer did not observe original receipt/full budget")
	}
	ea1HACReopen(t, f)
	if !reflect.DeepEqual(items, ea1HACItems(t, f.store, waiting.Turn.ID)) || !reflect.DeepEqual(ledger, ea1HACBudget(t, f.backend.ledgers, waiting.Turn.ID)) {
		t.Fatal("reopen lost original timeline/full ledger")
	}
	ea1HACAssertOwner(t, f, root, parent, child)
	gotIntent, err := f.store.GetItem(t.Context(), original.ID)
	if err != nil || !ea1ChildCancelItemFullEqual(gotIntent, original) {
		t.Fatalf("reopen lost original intent: %v", err)
	}
	gotReceipt, err := f.store.GetItem(t.Context(), actualReceipt.ID)
	if err != nil || !ea1ChildCancelItemFullEqual(gotReceipt, actualReceipt) {
		t.Fatalf("reopen lost original receipt: %v", err)
	}
	if _, err = f.harness.CancelSubtask(t.Context(), waiting.Turn.ID, task.ID, "inspect original terminal receipt after reopen"); err != nil {
		t.Fatal(err)
	}
	ea1HACAssertOwner(t, f, root, parent, child)
	if !reflect.DeepEqual(ledger, ea1HACBudget(t, f.backend.ledgers, waiting.Turn.ID)) || f.physical.controlCalls.Load() != 1 || f.toolCalls != 1 || f.model.calls != 1 {
		t.Fatal("reopened same source replayed physical work or reset budget")
	}
	gotIntent, err = f.store.GetItem(t.Context(), original.ID)
	if err != nil || !ea1ChildCancelItemFullEqual(gotIntent, original) {
		t.Fatal("later call replaced original election")
	}
	gotReceipt, err = f.store.GetItem(t.Context(), actualReceipt.ID)
	if err != nil || !ea1ChildCancelItemFullEqual(gotReceipt, actualReceipt) {
		t.Fatal("later call replaced original committed receipt")
	}
	if _, err = f.harness.ResolveSubtaskApproval(t.Context(), waiting.Turn.ID, task.ID, task.Approval.CheckpointID, harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove}); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("old approval replay accepted: %v", err)
	}
	ea1HACAssertOwner(t, f, root, parent, child)
	if f.toolCalls != 1 || f.model.calls != 1 || f.physical.controlCalls.Load() != 1 || !reflect.DeepEqual(ledger, ea1HACBudget(t, f.backend.ledgers, waiting.Turn.ID)) {
		t.Fatal("stale approval changed original physical/budget facts")
	}
}
func ea1HACAssertToolReceipt(t *testing.T, f *ea1HACFixture, id string, child kernel.Snapshot) {
	t.Helper()
	ea1HACAdmissionModel(t, f, child)
	expected := tools.ExecutionResult{Content: json.RawMessage(`{"evidence":"approved"}`), Receipt: tools.Receipt{ExecutionID: "ea1HAC-call", Disposition: "read"}}
	if child.Run.Status != kernel.RunStatusCompleted || !reflect.DeepEqual(f.toolRequest, ea1HACExpectedToolRequest(id)) || !reflect.DeepEqual(f.toolResult, expected) || !reflect.DeepEqual(child.Result, &kernel.Result{ContentType: "application/json", Content: expected.Content}) {
		t.Fatal("original full request/receipt/result binding failed")
	}
	events, err := f.runtime.ListEvents(t.Context(), id, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, ev := range events {
		if ev.Type == "tool.completed" {
			count++
			if ev.Message != expected.Receipt.Disposition {
				t.Fatal("durable receipt disposition differs")
			}
		}
	}
	if count != 1 {
		t.Fatalf("durable original receipt count=%d", count)
	}
}
func TestEA1HACApprovalCancellationElection(t *testing.T) {
	ea1HACMatrix(t, func(t *testing.T, f *ea1HACFixture) {
		// The approval-first, actual-send election and same-intent resolution are
		// exercised by G11 above; this direction freezes cancellation first.
		waiting, task, root, before := ea1HACStart(t, f)
		parent := ea1HACLoad(t, f, root.ExecutionRefID)

		barrier := &ea1HACCancelReceiptBarrier{Store: f.store, child: task.RunID, entered: make(chan struct{}), release: make(chan struct{})}
		var once sync.Once
		release := func() { once.Do(func() { close(barrier.release) }) }
		t.Cleanup(release)
		done := make(chan error, 1)
		candidate := ea1HACRunner(t, f, barrier, f.backend.ledgers, f.relations)
		go func() {
			_, e := candidate.CancelSubtask(context.Background(), waiting.Turn.ID, task.ID, "cancel before original approval")
			done <- e
		}()
		select {
		case <-barrier.entered:
		case <-time.After(10 * time.Second):
			t.Fatal("cancel-first receipt barrier not reached")
		}

		child := ea1HACLoad(t, f, task.RunID)
		if child.Run.Status != kernel.RunStatusCancelled || f.physical.controlCalls.Load() != 1 {
			t.Fatal("original cancellation did not win")
		}
		ledger := ea1HACBudget(t, f.backend.ledgers, waiting.Turn.ID)
		if !reflect.DeepEqual(ledger, ea1HACExpectedBudget(before, task.RunID)) {
			t.Fatal("cancellation election changed unrelated budget")
		}
		items := ea1HACItems(t, f.store, waiting.Turn.ID)
		intent := ea1OnlyCancelIntent(t, items, waiting.Turn.ID, task.RunID)

		if !harness.ActiveCancellationFence(items) || len(ea1CancelReceipts(items, intent.ID)) != 0 || !ea1ChildCancelItemFullEqual(intent, barrier.intent) {
			t.Fatal("cancel-first barrier did not preserve original pending election")
		}

		if _, err := f.harness.ResolveSubtaskApproval(t.Context(), waiting.Turn.ID, task.ID, task.Approval.CheckpointID, harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove}); !errors.Is(err, harness.ErrConflict) {
			t.Fatalf("cancel-first approval=%v", err)
		}
		ea1HACAssertOwner(t, f, root, parent, child)
		if !reflect.DeepEqual(items, ea1HACItems(t, f.store, waiting.Turn.ID)) || !reflect.DeepEqual(ledger, ea1HACBudget(t, f.backend.ledgers, waiting.Turn.ID)) || f.toolCalls != 0 || f.model.calls != 1 || f.hostCalls.Load() != 0 || f.approvalCalls.Load() != 0 || f.physical.controlCalls.Load() != 1 {
			t.Fatal("losing approval crossed original cancellation election")
		}
		release()
		select {
		case e := <-done:
			if e != nil {
				t.Fatal(e)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("winning cancellation failed to finish")
		}
		finalItems := ea1HACItems(t, f.store, waiting.Turn.ID)
		if !ea1ChildCancelItemFullEqual(intent, ea1OnlyCancelIntent(t, finalItems, waiting.Turn.ID, task.RunID)) || !harness.VerifiedCancellationFenceReceipt(intent, ea1CancelReceipts(finalItems, intent.ID)) || harness.ActiveCancellationFence(finalItems) {
			t.Fatal("original cancellation receipt failed to settle original election")
		}
		ea1HACAssertOwner(t, f, root, parent, child)
		if !reflect.DeepEqual(ledger, ea1HACBudget(t, f.backend.ledgers, waiting.Turn.ID)) || f.toolCalls != 0 || f.model.calls != 1 || f.physical.controlCalls.Load() != 1 {
			t.Fatal("winner completion replayed original work")
		}

	})
}

// Exact original cancellation completion is held before Host receipt commit;
// no fake receipt/intent is inserted and no arbitrary Append is intercepted.
type ea1HACCancelReceiptBarrier struct {
	harness.Store
	child            string
	intent           harness.Item
	entered, release chan struct{}
}

func (s *ea1HACCancelReceiptBarrier) AppendItem(ctx context.Context, item harness.Item) (harness.Item, bool, error) {
	if item.RunID == s.child && s.intent.ID != "" && item.ParentItemID == s.intent.ID && item.Status == harness.ItemCompleted && harness.VerifiedCancellationFenceReceipt(s.intent, []harness.Item{item}) {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return harness.Item{}, false, ctx.Err()
		}
	}
	saved, fresh, err := s.Store.AppendItem(ctx, item)
	if err == nil && fresh && item.RunID == s.child && item.ParentItemID == harness.CancellationFenceParentItemID {
		s.intent = saved
	}
	return saved, fresh, err
}

func ea1HACAssertPeer(t *testing.T, f *ea1HACFixture, child kernel.Snapshot, grant kernel.ChildExecutionGrant) {
	t.Helper()
	actual, err := f.kernelBackend.peer.Load(t.Context(), child.Run.ID)
	if err != nil || !reflect.DeepEqual(actual, child) {
		t.Fatalf("independent Kernel peer changed original snapshot: %v", err)
	}
	got, err := f.kernelBackend.peer.LookupChildExecution(t.Context(), child.Run.ID)
	if err != nil || got != grant {
		t.Fatalf("independent Kernel peer changed original grant: %v", err)
	}
}
