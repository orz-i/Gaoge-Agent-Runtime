package harness

import (
	"context"
	"fmt"
	"testing"
)

func seededParentItemBenchmark(b *testing.B, size int) (*Runner, Invocation) {
	b.Helper()
	store := NewMemoryStore()
	const turnID = "long-benchmark-turn"
	const parentInvocationID = "parent-application"
	const parentItemID = "parent-item"
	for i := range size - 1 {
		if _, _, err := store.AppendItem(context.Background(), Item{
			ID: fmt.Sprintf("filler-%06d", i), TurnID: turnID,
			Kind: ItemArtifact, Status: ItemCompleted,
		}); err != nil {
			b.Fatal(err)
		}
	}
	if _, _, err := store.AppendItem(context.Background(), Item{
		ID: parentItemID, TurnID: turnID, InvocationID: parentInvocationID,
		Kind: ItemInvocation, Status: ItemStarted,
	}); err != nil {
		b.Fatal(err)
	}
	// Frozen live parent state. A Host-only Application has no Kernel Run;
	// its Invocation status is still required by the ancestry fence.
	store.invocations[parentInvocationID] = Invocation{
		ID: parentInvocationID, TurnID: turnID,
		ExecutionClass: ExecutionApplication, Status: InvocationRunning,
	}
	child := Invocation{ID: "child-application", TurnID: turnID, ParentItemID: parentItemID,
		ExecutionClass: ExecutionApplication, Status: InvocationAccepted}
	return &Runner{store: store}, child
}

func BenchmarkRequireLiveInvocationParentsMemoryScaling(b *testing.B) {
	for _, size := range []int{500, 2000, 10000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			runner, child := seededParentItemBenchmark(b, size)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := runner.requireLiveInvocationParents(context.Background(), child); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkListAllItemsMemoryScaling(b *testing.B) {
	for _, size := range []int{500, 2000, 10000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			runner, child := seededParentItemBenchmark(b, size)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				items, err := listAllItems(context.Background(), runner.store, child.TurnID)
				if err != nil || len(items) != size {
					b.Fatalf("items=%d err=%v", len(items), err)
				}
			}
		})
	}
}

// The late cursor is common after a client has already consumed most of a
// long Turn. It must not rescan the prefix of thousands of immutable Items.
func BenchmarkMemoryStoreTailCursorPage(b *testing.B) {
	for _, size := range []int{500, 2000, 10000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			runner, child := seededParentItemBenchmark(b, size)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				page, err := runner.store.ListItems(context.Background(), child.TurnID, uint64(size-1), 1)
				if err != nil || len(page) != 1 || page[0].Seq != uint64(size) {
					b.Fatalf("tail item=%+v err=%v", page, err)
				}
			}
		})
	}
}
