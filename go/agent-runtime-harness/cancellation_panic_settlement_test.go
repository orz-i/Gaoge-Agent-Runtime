package harness_test

import (
	"context"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

type panickingCancellationStore struct{ *memory.Store }

func (receiver *panickingCancellationStore) Commit(commitCtx context.Context, commitID string, commitRev uint64, commitMutation kernel.StoreMutation, commitAuth kernel.CommitAuthority) (kernel.Snapshot, bool, error) {
	if commitAuth.Control != nil {
		panic("write-capable cancellation callback interrupted")
	}
	return receiver.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
}

func TestCancellationPanicCannotMintSuccessfulSettlement(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: &panickingCancellationStore{Store: memory.NewStore()}})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	r, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: protectiveRootPendingAgent{runtime: rt}, Store: store, Clock: featureInvocationClock{}})
	if err != nil {
		t.Fatal(err)
	}
	started, err := r.StartAgentTurn(t.Context(), testStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	var caught any
	func() {
		defer func() { caught = recover() }()
		_, _ = r.Cancel(t.Context(), started.Turn.ID, "protective stop")
	}()
	if caught == nil {
		t.Fatal("write boundary panic was not exercised")
	}
	items, err := store.ListItems(t.Context(), started.Turn.ID, 0, 500)
	if err != nil || !harness.ActiveCancellationFence(items) {
		t.Fatalf("panic was mistaken for successful normal return: %v", err)
	}
}
