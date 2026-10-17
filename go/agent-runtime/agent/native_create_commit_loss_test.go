package agent_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

var errLostCreateCommitResponse = errors.New("Create COMMIT succeeded, but response was lost")

type lostCreateResponseStore struct {
	kernel.Store
	kernel.ExecutionStore
	lost bool
}

func (store *lostCreateResponseStore) Create(ctx context.Context, record kernel.Record, events []kernel.EventDraft) (kernel.Snapshot, error) {
	snapshot, err := store.Store.Create(ctx, record, events)
	if err != nil {
		return snapshot, err
	}
	if !store.lost {
		store.lost = true
		return kernel.Snapshot{}, errLostCreateCommitResponse
	}
	return snapshot, nil
}

// A Native caller supplies a stable Run ID before first Create. When a
// successful COMMIT loses its response and an unrelated fenced CAS follows,
// retrying Start with that same ID cannot mint another Run or execute Model.
// Recovery reuses the newest authoritative revision.
func TestNativeLostCreateResponseAndAdvancedRevisionRecoverByStableID(t *testing.T) {
	base := memory.NewStore()
	wrapped := &lostCreateResponseStore{Store: base, ExecutionStore: base}
	rt, err := kernel.New(kernel.Dependencies{Store: wrapped})
	if err != nil {
		t.Fatal(err)
	}
	modelClient := &nativeLeaseFixtureModel{}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: modelClient,
		RunExecution: &agent.RunExecution{OwnerID: "stable-after-loss", LeaseDuration: 3 * time.Minute},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := agent.StartRequest{
		ID: "known-before-commit", Actor: kernel.ActorRef{TenantID: "t", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "unknown Create response",
	}
	if _, err = runner.StartRun(t.Context(), request); !errors.Is(err, errLostCreateCommitResponse) ||
		modelClient.calls != 0 {
		t.Fatalf("lost reply fixture dispatched side effects: %v, model=%d", err, modelClient.calls)
	}
	persisted, err := rt.Load(t.Context(), request.ID)
	if err != nil || persisted.Run.Revision != 1 {
		t.Fatalf("committed Create cannot be found by known ID: %+v err=%v", persisted, err)
	}
	lease, err := rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{
		RunID: persisted.Run.ID, OwnerID: "independent-control", LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := rt.WithExecution(lease)
	if err != nil {
		t.Fatal(err)
	}
	moved, err := bound.Apply(t.Context(), persisted.Run.ID, persisted.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: persisted.State,
		Events: []kernel.EventDraft{{Type: "test.unrelated_commit"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = rt.ReleaseExecution(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	if _, err = runner.StartRun(t.Context(), request); !errors.Is(err, kernel.ErrAlreadyExists) ||
		modelClient.calls != 0 {
		t.Fatalf("retry minted a second logical Run: %v model=%d", err, modelClient.calls)
	}
	final, err := runner.Resume(t.Context(), request.ID, moved.Run.Revision)
	if err != nil || final.Run.Status != kernel.RunStatusCompleted || modelClient.calls != 1 {
		t.Fatalf("recovery after newer Revision did not consume one Model result: %+v err=%v calls=%d",
			final.Run, err, modelClient.calls)
	}
}
