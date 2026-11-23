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
	if _, exists := reflect.TypeOf(Invocation{}).FieldByName("ExecutionClass"); exists {
		t.Fatal("removed multi-execution class was reintroduced into Agent Invocation")
	}
	api := reflect.TypeOf((*Runner)(nil))
	for _, method := range []string{"StartTeamTurn", "StartGroupChatTurn", "StartWorkflowTurn", "StartTeamInvocation", "StartWorkflowInvocation", "ResumeInvocation"} {
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
		ExecutionRefID: "hcr-old-application",
		Input:          input, InputHash: hashInvocationBytes(input), Status: InvocationFailed,
		Attempt: 1, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := store.CreateInvocation(t.Context(), old); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("new non-Agent Invocation admitted: %v", err)
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
	// A historical arbitrary Native Child has no first-party physical
	// starter or resumer, even when its execution class says Agent.
	child := old
	child.ID = "legacy-native-child"

	child.ParentItemID = "parent-item"
	child.ExecutionRefID = "old-native-child-run"
	child.CapabilityKey = CapabilityAgent
	store.invocations[child.ID] = child
	store.invocationExecutionRefs[child.ExecutionRefID] = child.ID
	if _, err := store.RetryInvocation(t.Context(), child.ID, child.Revision, "hr-next-child", now.Add(time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatalf("Store rotated retired native Child: %v", err)
	}
	if _, err := runner.RetryInvocation(t.Context(), child.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("Runner retried retired native Child: %v", err)
	}
	if _, err := runner.RefreshInvocation(t.Context(), child.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("Runner observed retired native Child: %v", err)
	}
	if _, err := runner.CancelInvocation(t.Context(), child.ID, "no original route"); !errors.Is(err, ErrConflict) {
		t.Fatalf("Runner cancelled retired native Child: %v", err)
	}
}
