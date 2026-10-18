package sqlite_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// BenchmarkSQLiteRuntimeLoad is the persisted Run hot path, not an event replay.
func BenchmarkSQLiteRuntimeLoad(b *testing.B) {
	store, err := sqlite.CreateNew(context.Background(), filepath.Join(privateDir(b), "load.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	runtime, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		b.Fatal(err)
	}
	req := sampleCreate("load")
	if _, err := runtime.Create(context.Background(), req); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := runtime.Load(context.Background(), req.ID); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSQLiteStoreLoadStateSizes isolates the persisted Snapshot path
// for realistic growing Agent State payloads. The fixture is seeded outside
// the timed loop; no extra copies or writes are introduced by the benchmark.
func BenchmarkSQLiteStoreLoadStateSizes(b *testing.B) {
	for _, tc := range []struct {
		name string
		size int
	}{
		{name: "8KiB", size: 8 * 1024},
		{name: "128KiB", size: 128 * 1024},
		{name: "800KiB", size: 800 * 1024},
	} {
		b.Run(tc.name, func(b *testing.B) {
			ctx := context.Background()
			store, err := sqlite.CreateNew(ctx, filepath.Join(privateDir(b), "sized-load.sqlite"))
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = store.Close() })
			rt, err := kernel.New(kernel.Dependencies{Store: store})
			if err != nil {
				b.Fatal(err)
			}
			req := sampleCreate("sized-load")
			req.State = statePayload(tc.size)
			if _, err := rt.Create(ctx, req); err != nil {
				b.Fatal(err)
			}
			// Consume an initial read before measuring so first-open work
			// does not contaminate the State ownership comparison.
			if _, err := store.Load(ctx, req.ID); err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(len(req.State)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				loaded, loadErr := store.Load(ctx, req.ID)
				if loadErr != nil || len(loaded.State) != len(req.State) {
					b.Fatalf("sized Load: State len=%d err=%v", len(loaded.State), loadErr)
				}
			}
		})
	}
}

// BenchmarkSQLiteRuntimeApplyWakeup commits the Run, event journal,
// in-database ReadyWork and revision proof in one short SQLite transaction.
func BenchmarkSQLiteRuntimeApplyWakeup(b *testing.B) {
	store, err := sqlite.CreateNew(context.Background(), filepath.Join(privateDir(b), "apply.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	runtime, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		b.Fatal(err)
	}
	req := sampleCreate("apply")
	current, err := runtime.Create(context.Background(), req)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		state := json.RawMessage(`{"step":` + strconv.Itoa(i) + `}`)
		current, err = runtime.Apply(context.Background(), req.ID, current.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning,
			State:  state,
			Events: []kernel.EventDraft{{Type: "agent.progress", Wakeup: true}},
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}
