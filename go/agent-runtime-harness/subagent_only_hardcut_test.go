package harness

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// A pre-stable composite Invocation must not become executable through an
// old deserializer, a command alias, or the generic physical control path.
func TestRemovedCompositeExecutionClassesAreNotAdmitted(t *testing.T) {
	for _, name := range []ExecutionClass{"team", "group_chat", "workflow", "application"} {
		if validExecutionClass(name) {
			t.Fatalf("removed execution class %q accepted", name)
		}

	}
	api := reflect.TypeOf((*Runner)(nil))
	for _, method := range []string{"StartTeamTurn", "StartGroupChatTurn", "StartWorkflowTurn", "StartTeamInvocation", "StartWorkflowInvocation"} {
		if _, ok := api.MethodByName(method); ok {
			t.Fatalf("removed method %s reintroduced", method)
		}
	}
	deps := reflect.TypeOf(Dependencies{})
	for _, field := range []string{"Teams", "GroupChats", "Workflows"} {
		if _, ok := deps.FieldByName(field); ok {
			t.Fatalf("removed dependency %s reintroduced", field)
		}
	}
}

// A pre-cut SQL/Memory row can still exist after its Application executor has
// been removed. RetryInvocation is a direct Store CAS, not just a Runner API:
// it must never rotate that historical non-Agent record to a new physical ID.
func TestRetiredApplicationStoreRetryCannotRotateIntoAgent(t *testing.T) {
	now := time.Now().UTC()
	store := NewMemoryStore()
	input := []byte(`{"goal":"legacy host operation"}`)
	old := Invocation{
		ID: "old-application", TurnID: "old-turn", CapabilityKey: "app.legacy",
		ExecutionClass: ExecutionClass("application"), ExecutionRefID: "hcr-old-application",
		Input: input, InputHash: hashInvocationBytes(input), Status: InvocationFailed,
		Attempt: 1, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	store.invocations[old.ID] = old // Historical persisted row, not new admission.
	store.invocationExecutionRefs[old.ExecutionRefID] = old.ID
	store.turns[old.TurnID] = Turn{ID: old.TurnID, Status: TurnFailed, Revision: 1}
	if _, err := store.RetryInvocation(t.Context(), old.ID, old.Revision, "hr-new-agent", now.Add(time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatalf("retired Application was allowed to mint a new Agent identity: %v", err)
	}
	current := store.invocations[old.ID]
	if current.ExecutionRefID != old.ExecutionRefID || current.Attempt != old.Attempt {
		t.Fatalf("legacy physical identity mutated: %+v", current)
	}
	// Runner entry points must reject before consulting any physical Kernel
	// runtime or manufacturing an Agent retry for an obsolete owner.
	runner := &Runner{store: store}
	if _, err := runner.RetryInvocation(t.Context(), old.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("Runner retried retired Application: %v", err)
	}
	if _, err := runner.RefreshInvocation(t.Context(), old.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("Runner refreshed retired Application as Agent: %v", err)
	}
	if _, err := runner.ResumeInvocation(t.Context(), old.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("Runner resumed retired Application as Agent: %v", err)
	}
}
