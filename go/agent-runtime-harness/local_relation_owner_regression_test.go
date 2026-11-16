package harness

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// Independent permanent regression: native child Invocation with a frozen owned Goal has a same
// ID but foreign physical Agent Run. A public child Cancel must NOT perform
// irreversible Kernel Cancel before validating the original child owner.
// Independent permanent read-only regression: after a physical Agent child Run
// carrying a foreign Goal and Result appears under the same deterministic ID,
// public Turn.Load must not copy its result into the Subtask model.
// Canonical KindCapability edges bind the original child Attempt to its
// frozen root, not merely a valid local Run ID or a coincident child owner.
// A RunRelation edge is only a routing assertion, not physical control authority.
// A forged delegation edge cannot relabel an unrelated valid root Agent as a
// child of another Turn, even when the local Run and Invocation match exactly.
func TestDelegationRelationCannotBorrowUnrelatedNativeAgentRoot(t *testing.T) {
	f := newPR82HandoffFixture(t)
	f.runner.relationReader = f.runner.relations.(RunRelationSource)
	relations := f.runner.relations.(*runrelation.Registry)
	_, err := relations.Ensure(t.Context(), runrelation.Draft{
		ParentRunID: "foreign-turn-root", ChildRunID: f.parentRun.Run.ID,
		Kind: runrelation.KindDelegation, OwnerNodeID: "foreign-delegation",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.runner.verifyLocalRelationRun(t.Context(), f.parentRun, true); !errors.Is(err, ErrRuntimeCancellationUnverified) {
		t.Fatalf("forged delegation edge borrowed valid native root execution authority: %v", err)
	}
	f.runner.cancellation = f.runtime
	if _, _, err = f.runner.cancelRuntimeRun(t.Context(), f.parentRun.Run.ID, "forged parent cancellation", &f.parent); !errors.Is(err, ErrRuntimeCancellationUnverified) {
		t.Fatalf("forged delegation index reached physical cancellation: %v", err)
	}
	persisted, loadErr := f.runtime.Load(t.Context(), f.parentRun.Run.ID)
	if loadErr != nil || persisted.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("foreign cancellation changed original Agent: status=%s err=%v", persisted.Run.Status, loadErr)
	}
}
