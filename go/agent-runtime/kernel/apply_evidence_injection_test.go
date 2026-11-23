package kernel_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

// A reserved Apply evidence event cannot be forged at Run creation or
// through a separately validated Control transition. Either would make
// later recovery incorrectly claim that an unrelated Apply committed.
func TestApplyEvidenceCannotBeForgedThroughCreateOrControl(t *testing.T) {
	for _, kind := range []string{"kernel.apply.evidence", "kernel.allowance.dispatch_committed"} {
		t.Run(kind, func(t *testing.T) { checkReservedEvent(t, kind) })
	}
}

func checkReservedEvent(t *testing.T, kind string) {
	t.Helper()
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	record := kernel.CreateRequest{
		ID: "reserved-evidence", Kind: "test.evidence",
		Actor:  kernel.ActorRef{TenantID: "t", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "cannot impersonate commit proof", State: json.RawMessage(`{}`),
		Events: []kernel.EventDraft{{Type: kind, Data: json.RawMessage(`{}`)}},
	}
	if _, err = rt.Create(t.Context(), record); !errors.Is(err, kernel.ErrInvalidInput) {
		t.Fatalf("Create accepted a forged proof event: %v", err)
	}
	record.Events = nil
	created, err := rt.Create(t.Context(), record)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = rt.ApplyControl(t.Context(), created.Run.ID, created.Run.Revision,
		kernel.ControlGuard{Kind: kernel.ControlCancel}, kernel.Mutation{
			Status: kernel.RunStatusCancelled, State: created.State,
			ErrorCode: "run.cancelled",
			Events:    []kernel.EventDraft{{Type: kind, Data: json.RawMessage(`{}`)}},
		}, nil); !errors.Is(err, kernel.ErrInvalidInput) {
		t.Fatalf("Control accepted a forged proof event: %v", err)
	}
	ordinary := kernel.Mutation{Status: kernel.RunStatusRunning, State: created.State, Events: []kernel.EventDraft{{Type: kind}}}
	if _, err = rt.Apply(t.Context(), created.Run.ID, created.Run.Revision, ordinary); !errors.Is(err, kernel.ErrInvalidInput) {
		t.Fatalf("ordinary Apply accepted reserved event: %v", err)
	}
	if _, err = rt.ApplyIdentified(t.Context(), created.Run.ID, created.Run.Revision, "unrelated", ordinary); !errors.Is(err, kernel.ErrInvalidInput) {
		t.Fatalf("identified Apply accepted reserved event: %v", err)
	}
	stored, err := rt.Load(t.Context(), created.Run.ID)
	if err != nil || stored.Run.Revision != 1 {
		t.Fatalf("forged control changed persisted Run: %+v err=%v", stored.Run, err)
	}
}
