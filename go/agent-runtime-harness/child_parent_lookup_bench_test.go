package harness

import (
	"context"
	"fmt"
	"testing"
)

// Admission used to materialize every prior Turn Item even when the requested
// parent was one immutable global ID. This benchmark measures only that read
// boundary, not an entire Agent/Workflow execution or PostgreSQL latency.
func BenchmarkChildParentAdmissionIndexed(b *testing.B) {
	for _, size := range []int{500, 2000, 10000} {
		b.Run(fmt.Sprintf("items=%d", size), func(b *testing.B) {
			backing := NewMemoryStore()
			const turnID = "bench-parent-admission"
			for i := range size {
				_, _, err := backing.AppendItem(context.Background(), Item{
					ID: fmt.Sprintf("unrelated-%05d", i), TurnID: turnID,
					Kind: ItemArtifact, Status: ItemCompleted,
				})
				if err != nil {
					b.Fatal(err)
				}
			}
			if _, _, err := backing.AppendItem(context.Background(), Item{
				ID: "actual-parent", TurnID: turnID,
				Kind: ItemInvocation, Status: ItemStarted,
			}); err != nil {
				b.Fatal(err)
			}
			runner := &Runner{store: backing}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := runner.validateParentItem(context.Background(), turnID, "actual-parent"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
