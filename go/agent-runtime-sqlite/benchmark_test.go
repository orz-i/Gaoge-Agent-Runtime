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
