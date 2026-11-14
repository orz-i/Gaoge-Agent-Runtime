package continuation_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// Embedding the concrete Store preserves every production capability. Only the
// return of the real approval Mark is delayed; neither its commit nor the
// worker's actual original-wakeup delivery is synthesized.
type approvalSettlementBarrierStore struct {
	*memory.Store
	mu             sync.Mutex
	target         string
	markedGrant    kernel.ChildExecutionGrant
	markedSnapshot kernel.Snapshot
	markedLease    kernel.ExecutionLease
	marked         chan struct{}
	release        chan struct{}
	finished       chan approvalSettlementFinish
	releaseOnce    sync.Once
	allowACK       chan struct{}
	allowWorker    chan struct{}
	acknowledged   chan kernel.OwnedRetirementAckResult
	ackCount       int
	ackOnce        sync.Once
	workerOnce     sync.Once
}

type approvalSettlementFinish struct {
	grant kernel.ChildExecutionGrant
	err   error
}

func (store *approvalSettlementBarrierStore) unblock() {
	store.releaseOnce.Do(func() { close(store.release) })
}
func (store *approvalSettlementBarrierStore) unblockACK() {
	store.ackOnce.Do(func() { close(store.allowACK) })
}
func (store *approvalSettlementBarrierStore) unblockWorker() {
	store.workerOnce.Do(func() { close(store.allowWorker) })
}

func (store *approvalSettlementBarrierStore) MarkChildExecutionSettled(ctx context.Context, grant kernel.ChildExecutionGrant, permit kernel.ChildExecutionSettlement) error {
	store.mu.Lock()
	target := grant.ChildRunID == store.target && grant.AdmissionOperation == "approval"
	store.mu.Unlock()
	if !target || permit.IsRecovery() {
		return store.Store.MarkChildExecutionSettled(ctx, grant, permit)
	}
	snapshot, err := store.Load(ctx, grant.ChildRunID)
	if err != nil {
		return err
	}
	lease, ok := permit.ExecutionLease()
	if !ok {
		return kernel.ErrExecutionRequired
	}
	store.mu.Lock()
	store.markedGrant, store.markedSnapshot, store.markedLease = grant, snapshot, lease
	store.mu.Unlock()
	// Publish the observer identity before committing Mark, so even an immediate
	// worker Finish is captured. No barrier is signalled until Mark succeeds.
	if err := store.Store.MarkChildExecutionSettled(ctx, grant, permit); err != nil {
		return err
	}
	close(store.marked)
	select {
	case <-store.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (store *approvalSettlementBarrierStore) FinishChildExecution(ctx context.Context, grant kernel.ChildExecutionGrant) error {
	store.mu.Lock()
	waitForOwnerMark := grant.ChildRunID == store.target && grant.AdmissionOperation == "approval"
	store.mu.Unlock()
	if waitForOwnerMark {
		select {
		case <-store.marked:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	err := store.Store.FinishChildExecution(ctx, grant)
	store.mu.Lock()
	target := grant == store.markedGrant && grant.ID != ""
	store.mu.Unlock()
	if target && err == nil {
		// Preserve the first actual successful Finish result. Before the Mark return is released
		// only the asynchronous worker can reach this call.
		select {
		case store.finished <- approvalSettlementFinish{grant: grant, err: err}:
		default:
		}
	}
	return err
}

func (store *approvalSettlementBarrierStore) AcknowledgeOwnedRetirement(ctx context.Context, grant kernel.ChildExecutionGrant, proof kernel.OwnedRetirementAcknowledgement) (kernel.OwnedRetirementAckResult, error) {
	store.mu.Lock()
	target := grant == store.markedGrant && grant.ID != ""
	store.mu.Unlock()
	if !target {
		return store.Store.AcknowledgeOwnedRetirement(ctx, grant, proof)
	}
	select {
	case <-store.allowACK:
	case <-ctx.Done():
		return kernel.OwnedRetirementAckResult{}, ctx.Err()
	}
	result, err := store.Store.AcknowledgeOwnedRetirement(ctx, grant, proof)
	if err != nil {
		return result, err
	}
	store.mu.Lock()
	store.ackCount++
	first := store.ackCount == 1
	store.mu.Unlock()
	if first {
		store.acknowledged <- result
		// Hold only the peer's return, not its transaction/lock. The original call
		// must observe the same historical ACK without clearing a second lease.
		select {
		case <-store.allowWorker:
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
	return result, nil
}

func TestApprovalWakeupCannotStealOriginalOwnerSettlement(t *testing.T) {
	store := &approvalSettlementBarrierStore{
		Store: memory.NewStore(), marked: make(chan struct{}), release: make(chan struct{}),
		finished: make(chan approvalSettlementFinish, 1),
		allowACK: make(chan struct{}), allowWorker: make(chan struct{}), acknowledged: make(chan kernel.OwnedRetirementAckResult, 1),
	}
	fixture := newContinuationIntegrationFixture(t, store)
	defer fixture.close(t)
	defer store.unblock()
	defer store.unblockACK()
	defer store.unblockWorker()
	parent := fixture.startPendingTeam(t)
	child := fixture.waitingChild(t, parent)
	if child.Run.ParentRunID != "" {
		t.Fatal("fixture must exercise actual Root-owned Agent admission")
	}
	store.mu.Lock()
	store.target = child.Run.ID
	store.mu.Unlock()
	// Same startup ordering as the original integration test: the real worker
	// runs before approval and consumes the real interaction.resolved outbox item.
	if err := fixture.worker.Start(fixture.workerContext); err != nil {
		t.Fatal(err)
	}
	type approvalResult struct {
		snapshot kernel.Snapshot
		err      error
	}
	returned := make(chan approvalResult, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	go func() {
		snapshot, err := fixture.agent.ResolveApproval(ctx, child.Run.ID, child.Run.Revision,
			plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
		returned <- approvalResult{snapshot: snapshot, err: err}
	}()
	select {
	case <-store.marked:
	case <-ctx.Done():
		t.Fatal("actual approval Mark commit not reached", ctx.Err())
	}
	store.mu.Lock()
	grant, source, lease := store.markedGrant, store.markedSnapshot, store.markedLease
	store.mu.Unlock()
	if !grant.IsOwned() || !grant.MatchesExecution(lease) || source.Run.ID != child.Run.ID ||
		source.Run.Status != kernel.RunStatusRunning || source.Run.Revision <= child.Run.Revision ||
		source.Run.Actor != child.Run.Actor || source.Run.Thread != child.Run.Thread {
		t.Fatalf("wrong actual approval source: grant=%#v lease=%#v source=%#v", grant, lease, source)
	}
	var retired approvalSettlementFinish
	select {
	case retired = <-store.finished:
	case <-ctx.Done():
		t.Fatal("worker did not reach actual original-grant Finish", ctx.Err())
	}
	if retired.err != nil || retired.grant != grant {
		t.Fatalf("worker Finish=%#v", retired)
	}
	// A successful Store Finish proves the original committed settlement marker
	// matched the unchanged revision. It is not inferred from a clean state.
	if _, err := store.LookupChildExecution(ctx, child.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("worker did not retire exact marked grant: %v", err)
	}
	after, err := store.Load(ctx, child.Run.ID)
	if err != nil || !reflect.DeepEqual(source, after) {
		t.Fatalf("worker changed full approval snapshot: before=%#v after=%#v err=%v", source, after, err)
	}
	if err := store.CheckExecution(ctx, child.Run.ID, &lease); err != nil {
		t.Fatalf("original owner/epoch changed before cleanup: %v", err)
	}
	if fixture.executions.Load() != 0 || fixture.model.CallCount() != 1 {
		t.Fatalf("unexpected physical work before original owner cleanup: tools=%d models=%d", fixture.executions.Load(), fixture.model.CallCount())
	}
	retirement, err := store.LoadOwnedRetirement(ctx, grant.ID)
	if err != nil || !retirement.Valid() || retirement.Evidence.Grant != grant ||
		kernel.ManagedDigest(retirement.Evidence.Snapshot) != kernel.ManagedDigest(source) ||
		!retirement.Evidence.Completion.HostCompleted || !retirement.Evidence.Completion.LeaseOwned || retirement.Phase != "RETIRED" {
		t.Fatalf("missing exact genuine retirement: %#v %v", retirement, err)
	}
	store.unblockACK()
	var firstACK kernel.OwnedRetirementAckResult
	select {
	case firstACK = <-store.acknowledged:
	case <-ctx.Done():
		t.Fatal("worker ACK not reached", ctx.Err())
	}
	if firstACK.ReceiptID != grant.ID || firstACK.PreviouslyAcknowledged || firstACK.Disposition != "released_owned" || firstACK.SettledSourceDigest != kernel.ManagedDigest(source) {
		t.Fatalf("incorrect first peer ACK: %#v", firstACK)
	}
	if err := store.CheckExecution(ctx, child.Run.ID, &lease); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("original owned lease not retired: %v", err)
	}
	after, err = store.Load(ctx, child.Run.ID)
	if err != nil || !reflect.DeepEqual(source, after) {
		t.Fatalf("ACK changed Run source: %#v %v", after, err)
	}
	store.unblock()
	var result approvalResult
	select {
	case result = <-returned:
	case <-ctx.Done():
		t.Fatal("original approval did not return", ctx.Err())
	}
	// Preserve the original public success oracle after actual peer retirement.
	// The original call confirms the same canonical ACK; it must not recreate
	// a grant or require the already-deleted grant for managed completion.
	if result.err != nil || result.snapshot.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("resolved=%#v err=%v; originalGrant=%#v originalLease=%#v tools=%d models=%d", result.snapshot.Run, result.err, grant, lease, fixture.executions.Load(), fixture.model.CallCount())
	}
	store.mu.Lock()
	ackCount := store.ackCount
	store.mu.Unlock()
	if ackCount != 2 {
		t.Fatalf("owner did not confirm exactly the peer ACK: %d", ackCount)
	}
	acknowledged, err := store.LoadOwnedRetirement(ctx, grant.ID)
	if err != nil || acknowledged.Phase != "ACKNOWLEDGED" || acknowledged.Identity() != retirement.Identity() {
		t.Fatalf("historical receipt identity changed: %#v %v", acknowledged, err)
	}
	store.unblockWorker()
	fixture.waitForCompletedParent(t, parent.Run.ID)
	fixture.assertSingleExecution(t)
}
