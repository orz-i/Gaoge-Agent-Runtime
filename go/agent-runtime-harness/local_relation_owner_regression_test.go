package harness

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// Independent permanent regression: native child Invocation with a frozen owned Goal has a same
// ID but foreign physical Agent Run. A public child Cancel must NOT perform
// irreversible Kernel Cancel before validating the original child owner.
func TestIndependentNativeChildCancelDoesNotCancelForeignSameIDRun(t *testing.T) {
	f := newPR82HandoffFixture(t)
	f.runner.cancellation = f.runtime
	f.runner.relationReader = f.runner.relations.(RunRelationSource)
	raw, hash, err := marshalInvocationValue(struct {
		Goal string `json:"goal"`
	}{Goal: "original authorized child task"})
	if err != nil {
		t.Fatal(err)
	}
	child, _, _, err := f.runner.beginChildInvocation(t.Context(), f.turn.ID, f.startedItemID, "audit-child-req", CapabilityAgent, "1", ExecutionAgent, raw, hash)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := f.runtime.Create(t.Context(), kernel.CreateRequest{
		ID: child.ExecutionRefID, Kind: agent.RunKind,
		Actor: f.parentRun.Run.Actor, Thread: f.parentRun.Run.Thread,
		RequestID: "audit-child-req", Goal: "different physical task", State: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, cancelErr := f.runner.CancelInvocation(t.Context(), child.ID, "stop original child")
	actual, err := f.runtime.Load(t.Context(), foreign.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if actual.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("FOREIGN PHYSICAL CHILD CANCELLED: status=%s cancelErr=%v child=%+v", actual.Run.Status, cancelErr, child)
	}
	if !errors.Is(cancelErr, ErrConflict) && !errors.Is(cancelErr, ErrRuntimeCancellationUnverified) {
		t.Fatalf("expected fail-closed error, got %v", cancelErr)
	}
}

// Independent permanent read-only regression: after a physical Agent child Run
// carrying a foreign Goal and Result appears under the same deterministic ID,
// public Turn.Load must not copy its result into the Subtask model.
func TestIndependentForeignNativeChildCannotLeakSubtaskResult(t *testing.T) {
	f := newPR82HandoffFixture(t)
	f.runner.relationReader = f.runner.relations.(RunRelationSource)
	raw, hash, err := marshalInvocationValue(struct {
		Goal string `json:"goal"`
	}{Goal: "legitimate-owned-goal"})
	if err != nil {
		t.Fatal(err)
	}
	child, _, _, err := f.runner.beginChildInvocation(t.Context(), f.turn.ID, f.startedItemID, "audit-child-result-read", CapabilityAgent, "1", ExecutionAgent, raw, hash)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := f.runtime.Create(t.Context(), kernel.CreateRequest{
		ID: child.ExecutionRefID, Kind: agent.RunKind, Actor: kernel.ActorRef{TenantID: "foreign-tenant", ActorID: "other-actor"}, Thread: f.parentRun.Run.Thread,
		RequestID: "audit-child-result-read", Goal: "OTHER TENANT DIFFERENT GOAL", State: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.runtime.Apply(t.Context(), foreign.Run.ID, foreign.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: foreign.State,
		Result: &kernel.Result{ContentType: "application/json", Content: []byte(`"PRIVATE FOREIGN RESULT"`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := f.runner.Load(t.Context(), f.turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range view.Subtasks {
		if task.RunID != child.ExecutionRefID {
			continue
		}
		if len(task.Result) > 0 || task.Goal == "OTHER TENANT DIFFERENT GOAL" || task.Status == "completed" {
			t.Fatalf("UNVERIFIED PHYSICAL CHILD EXPOSED VIA SUBTASK: task=%+v", task)
		}
	}
}

// Canonical KindCapability edges bind the original child Attempt to its
// frozen root, not merely a valid local Run ID or a coincident child owner.
func TestNativeCapabilityRelationRequiresExactOwnerNodeAndRootEdge(t *testing.T) {
	f := newPR82HandoffFixture(t)
	child := auditMixedNativeChild(t, f, f.startedItemID, "canonical-edge-owner")
	reader := f.runner.relations.(RunRelationSource)
	relation, err := reader.GetByChild(t.Context(), child.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.runner.verifyCapabilityRelationOwner(t.Context(), relation, child); err != nil {
		t.Fatalf("legitimate original capability edge denied: %v", err)
	}
	for _, tc := range []struct {
		name  string
		alter func() runrelation.Relation
	}{
		{name: "foreign_owner_node", alter: func() runrelation.Relation {
			changed := relation
			changed.OwnerNodeID = "some-other-child-attempt"
			return changed
		}},
		{name: "foreign_parent_run", alter: func() runrelation.Relation {
			changed := relation
			changed.ParentRunID = "some-other-turn-parent-run"
			return changed
		}},
		{name: "foreign_child_run", alter: func() runrelation.Relation {
			changed := relation
			changed.ChildRunID = "some-other-child-run"
			return changed
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := f.runner.verifyCapabilityRelationOwner(t.Context(), tc.alter(), child); !errors.Is(err, ErrConflict) {
				t.Fatalf("forged capability edge accepted: %v", err)
			}
		})
	}
}
