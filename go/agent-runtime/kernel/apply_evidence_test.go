package kernel_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

var errInjectedApplyResponseLost = errors.New("injected Apply response lost after commit")
var errInjectedApplyNeverCommitted = errors.New("injected Apply failed before commit")
var errInjectedJournalUnavailable = errors.New("injected journal read unavailable")

type applyResponseFaultStore struct {
	kernel.Store
	beforeCommit bool
	failed       bool
}

func (store *applyResponseFaultStore) Apply(ctx context.Context, id string, revision uint64, mutation kernel.StoreMutation) (kernel.Snapshot, error) {
	if store.failed {
		return store.Store.Apply(ctx, id, revision, mutation)
	}
	store.failed = true
	if store.beforeCommit {
		return kernel.Snapshot{}, errInjectedApplyNeverCommitted
	}
	committed, err := store.Store.Apply(ctx, id, revision, mutation)
	if err != nil {
		return committed, err
	}
	return kernel.Snapshot{}, errInjectedApplyResponseLost
}

type unavailableJournalStore struct{ kernel.Store }

func (*unavailableJournalStore) ListEvents(context.Context, string, int64, int) ([]kernel.Event, error) {
	return nil, errInjectedJournalUnavailable
}

func newApplyProofRun(t *testing.T, store kernel.Store) (*kernel.Runtime, kernel.Snapshot) {
	t.Helper()
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	created, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: "apply-loss-run", Kind: "test.proof",
		Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "lost commit response", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return rt, created
}

func applyProofMutation() kernel.Mutation {
	return kernel.Mutation{
		Status: kernel.RunStatusRunning, State: json.RawMessage(`{"step":1}`),
		Events: []kernel.EventDraft{{Type: "feature.step", Wakeup: true}},
	}
}

func TestIdentifiedApplyLostReplyAfterCommitAndLaterRevision(t *testing.T) {
	base := memory.NewStore()
	store := &applyResponseFaultStore{Store: base}
	rt, created := newApplyProofRun(t, store)
	original := applyProofMutation()
	_, err := rt.ApplyIdentified(t.Context(), created.Run.ID, created.Run.Revision, "known-apply", original)
	if !errors.Is(err, errInjectedApplyResponseLost) {
		t.Fatalf("commit-loss injection not reproduced: %v", err)
	}
	// A distinct actor/Feature transition races ahead after the original
	// commit but before the original sender learns whether it succeeded.
	known, err := rt.Load(t.Context(), created.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	later, err := rt.Apply(t.Context(), known.Run.ID, known.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: json.RawMessage(`{"step":2}`),
		Events: []kernel.EventDraft{{Type: "feature.later"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	proof, err := rt.VerifyIdentifiedApply(t.Context(), created.Run.ID, created.Run.Revision, "known-apply", original)
	if err != nil || proof.Status != kernel.ApplyEvidenceCommitted ||
		proof.CommittedRevision != 2 || proof.CurrentRevision != 3 {
		t.Fatalf("lost response treated as unknown after newer revision: %+v %v", proof, err)
	}
	if _, err = rt.ApplyIdentified(t.Context(), created.Run.ID, created.Run.Revision, "known-apply", original); !errors.Is(err, kernel.ErrApplyAlreadyCommitted) {
		t.Fatalf("duplicate Apply re-executed: %v", err)
	}
	current, err := rt.Load(t.Context(), created.Run.ID)
	if err != nil || current.Run.Revision != later.Run.Revision ||
		current.EventHead != later.EventHead {
		t.Fatalf("retry changed the Run or appended duplicate proof: %+v err=%v", current, err)
	}
}

func TestIdentifiedApplyProvablyUncommittedCanRetryFixedCAS(t *testing.T) {
	base := memory.NewStore()
	store := &applyResponseFaultStore{Store: base, beforeCommit: true}
	rt, created := newApplyProofRun(t, store)
	original := applyProofMutation()
	_, err := rt.ApplyIdentified(t.Context(), created.Run.ID, created.Run.Revision, "known-apply", original)
	if !errors.Is(err, errInjectedApplyNeverCommitted) {
		t.Fatalf("pre-commit injection: %v", err)
	}
	proof, err := rt.VerifyIdentifiedApply(t.Context(), created.Run.ID, created.Run.Revision, "known-apply", original)
	if err != nil || proof.Status != kernel.ApplyEvidenceRetryableCAS {
		t.Fatalf("confirmed pre-commit was treated as committed: %+v %v", proof, err)
	}
	written, err := rt.ApplyIdentified(t.Context(), created.Run.ID, created.Run.Revision, "known-apply", original)
	if err != nil || written.Run.Revision != 2 {
		t.Fatalf("CAS retry did not recover: %+v %v", written, err)
	}
	proof, err = rt.VerifyIdentifiedApply(t.Context(), created.Run.ID, created.Run.Revision, "known-apply", original)
	if err != nil || proof.Status != kernel.ApplyEvidenceCommitted {
		t.Fatalf("retry lost evidence: %+v %v", proof, err)
	}
}

func TestIdentifiedApplyJournalOutageFailsClosed(t *testing.T) {
	base := memory.NewStore()
	rt, created := newApplyProofRun(t, &unavailableJournalStore{Store: base})
	if _, err := rt.VerifyIdentifiedApply(t.Context(), created.Run.ID, created.Run.Revision,
		"known-apply", applyProofMutation()); !errors.Is(err, kernel.ErrApplyOutcomeUnknown) ||
		!errors.Is(err, errInjectedJournalUnavailable) {
		t.Fatalf("missing journal was misclassified as absent proof: %v", err)
	}
}

func TestIdentifiedApplyConcurrentSameIDSingleCAS(t *testing.T) {
	rt, created := newApplyProofRun(t, memory.NewStore())
	var group sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := rt.ApplyIdentified(context.Background(), created.Run.ID, created.Run.Revision,
				"same-logical-operation", applyProofMutation())
			errs <- err
		}()
	}
	group.Wait()
	close(errs)
	committed := 0
	for err := range errs {
		switch {
		case err == nil:
			committed++
		case errors.Is(err, kernel.ErrApplyAlreadyCommitted), errors.Is(err, kernel.ErrConflict):
		default:
			t.Fatalf("unexpected concurrent receipt: %v", err)
		}
	}
	if committed != 1 {
		t.Fatalf("identity committed %d times", committed)
	}
	saved, err := rt.Load(t.Context(), created.Run.ID)
	if err != nil || saved.Run.Revision != 2 || saved.EventHead != 3 {
		t.Fatalf("duplicate committed events: %+v err=%v", saved, err)
	}
}
