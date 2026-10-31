package harness

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

type exactRootLookupStore struct {
	Store
	getIDs    []string
	listCalls int
}

func (store *exactRootLookupStore) GetInvocation(ctx context.Context, id string) (Invocation, error) {
	store.getIDs = append(store.getIDs, id)
	return store.Store.GetInvocation(ctx, id)
}

func (store *exactRootLookupStore) ListInvocations(context.Context, string) ([]Invocation, error) {
	store.listCalls++
	return nil, ErrConflict
}

func rootBoundChildFixture(t testing.TB, backing *MemoryStore, turnID string) Invocation {
	t.Helper()
	now := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	root := Invocation{
		ID: "indexed-root", TurnID: turnID, CapabilityKey: "application.root",
		ExecutionClass: ExecutionApplication, ExecutionRefID: "host-root-attempt-1",
		Status: InvocationRunning, Attempt: 1, Revision: 1,
		OutputRefs: []HostRef{}, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := backing.CreateInvocation(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if _, _, err := backing.AppendItem(context.Background(), Item{
		ID: "root-scoped-artifact", TurnID: turnID, Kind: ItemArtifact, Status: ItemCompleted,
	}); err != nil {
		t.Fatal(err)
	}
	raw, inputHash, err := marshalInvocationValue(struct {
		Goal string `json:"goal"`
	}{Goal: "child"})
	if err != nil {
		t.Fatal(err)
	}
	input, inputHash, err := sealChildRootInput(raw, inputHash, root)
	if err != nil {
		t.Fatal(err)
	}
	return Invocation{
		ID: "indexed-child", TurnID: turnID, ParentItemID: "root-scoped-artifact",
		CapabilityKey: "application.child", ExecutionClass: ExecutionApplication,
		Input: input, InputHash: inputHash, ExecutionRefID: "child-attempt-1",
		Status: InvocationAccepted, Attempt: 1, Revision: 1,
		OutputRefs: []HostRef{}, CreatedAt: now, UpdatedAt: now,
	}
}

// The last pre-effect fence must not list and deserialize all Invocations.
// It looks up the frozen root by exact PK, independent of number of children.
func TestChildFinalEffectFenceUsesExactFrozenRootLookup(t *testing.T) {
	backing := NewMemoryStore()
	child := rootBoundChildFixture(t, backing, "indexed-fence-turn")
	for index := range 1000 {
		record := child
		record.ID = fmt.Sprintf("unrelated-child-%05d", index)
		record.ExecutionRefID = fmt.Sprintf("child-execution-%05d", index)
		if _, _, err := backing.CreateInvocation(t.Context(), record); err != nil {
			t.Fatal(err)
		}
	}
	index := &exactRootLookupStore{Store: backing}
	runner := &Runner{store: index}
	if err := runner.requireLiveInvocationParents(t.Context(), child); err != nil {
		t.Fatalf("valid frozen root rejected: %v", err)
	}
	if index.listCalls != 0 || len(index.getIDs) != 1 || index.getIDs[0] != "indexed-root" {
		t.Fatalf("last effect fence performed non-indexed root lookup: %+v", index)
	}
}

func TestChildFrozenRootProofIsMandatoryAndImmutable(t *testing.T) {
	backing := NewMemoryStore()
	child := rootBoundChildFixture(t, backing, "frozen-root-turn")
	runner := &Runner{store: backing}
	if err := runner.requireLiveInvocationParents(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	// Pre-hardcut children have no frozen root proof and may not be
	// redispatched by reconstructing authority from the current root.
	legacy := child
	legacy.Input, legacy.InputHash, _ = marshalInvocationValue(struct {
		Goal string `json:"goal"`
	}{Goal: "child"})
	if err := runner.requireLiveInvocationParents(t.Context(), legacy); !errors.Is(err, ErrConflict) {
		t.Fatalf("unbound legacy child was granted physical authority: %v", err)
	}
	// The canonical input and its hash are immutable; changing the root ID
	// without the old identity hash is not an admission certificate.
	tampered := child
	tampered.Input = append([]byte(nil), child.Input...)
	tampered.Input[1] = 'x'
	if err := runner.requireLiveInvocationParents(t.Context(), tampered); !errors.Is(err, ErrConflict) {
		t.Fatalf("tampered root binding was trusted: %v", err)
	}
	// Re-sealing previously sealed input cannot overwrite the root Attempt.
	if _, _, err := sealChildRootInput(child.Input, child.InputHash, Invocation{
		ID: "indexed-root", ExecutionRefID: "another-root-attempt", Attempt: 2,
		ExecutionClass: ExecutionApplication, Status: InvocationRunning,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("frozen child admission was rebound: %v", err)
	}
}

// Memory-only microbenchmark for the exact last pre-effect lookup, not
// the external Feature call, Postgres round trip or total Turn projection.
func BenchmarkChildRootFinalFenceIndexed(b *testing.B) {
	for _, size := range []int{500, 2000, 10000} {
		b.Run(fmt.Sprintf("invocations=%d", size), func(b *testing.B) {
			backing := NewMemoryStore()
			child := rootBoundChildFixture(b, backing, "bench-root-fence-turn")
			for index := range size {
				record := child
				record.ID = fmt.Sprintf("unrelated-child-%05d", index)
				record.ExecutionRefID = fmt.Sprintf("child-execution-%05d", index)
				if _, _, err := backing.CreateInvocation(context.Background(), record); err != nil {
					b.Fatal(err)
				}
			}
			runner := &Runner{store: backing}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := runner.requireLiveInvocationParents(context.Background(), child); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
