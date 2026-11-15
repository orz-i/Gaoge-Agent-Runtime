package conformance

import (
	"errors"
	"reflect"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// ea1StartRuntime creates an observer over a potentially fault-injected Store.
// Shared by root and child Agent execution conformance.
func ea1StartRuntime(t *testing.T, s kernel.Store) *kernel.Runtime {
	t.Helper()
	r, err := kernel.New(kernel.Dependencies{Store: s})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// ea1CompBarrierUnchanged asserts no Store mutation after a denied physical
// operation. Keep the original identifier for Agent conformance fixtures.
func ea1CompBarrierUnchanged(t *testing.T, rt *kernel.Runtime, before kernel.Snapshot) {
	t.Helper()
	after, err := rt.Load(t.Context(), before.Run.ID)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatal("rejected control mutated full snapshot")
	}
}

// ea1StartAbsent proves a rejected child cannot persist a Run or grant.
func ea1StartAbsent(t *testing.T, rt *kernel.Runtime, id string) {
	t.Helper()
	if _, err := rt.Load(t.Context(), id); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("pre-Create denial persisted: %v", err)
	}
	if _, err := rt.LookupChildExecution(t.Context(), id); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("denied source grant: %v", err)
	}
}
