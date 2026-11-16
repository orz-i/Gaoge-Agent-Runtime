package harness

import (
	"context"
	"fmt"
	"testing"
)

// A one-edge ancestry lookup must not materialize a Turn's entire history.
// Use the same 500/2k/10k immutable Item fixtures as the existing parent-item
// scaling benchmarks so before/after results are comparable locally.
func BenchmarkDelegationDepthMemoryScaling(b *testing.B) {
	for _, size := range []int{500, 2000, 10000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			runner, child := seededParentItemBenchmark(b, size)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				depth, err := invocationDelegationDepth(context.Background(), runner.store, child)
				if err != nil || depth != 0 {
					b.Fatalf("depth=%d err=%v", depth, err)
				}
			}
		})
	}
}
