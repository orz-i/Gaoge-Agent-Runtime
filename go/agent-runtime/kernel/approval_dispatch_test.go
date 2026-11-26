package kernel_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

func TestApprovalDispatchEvidenceRequiresNativeControl(t *testing.T) {
	rt, source := newApplyProofRun(t, memory.NewStore())
	ctx := t.Context()
	a := kernel.ManagedApprovalDecision{CheckpointID: "original-checkpoint", Decision: "approve", Comment: "original"}
	id, err := kernel.OwnedPhysicalDispatchGrantID(source.Run.ID, "approval", "foreign-dispatch")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(struct {
		SourceRevision uint64                         `json:"sourceRevision"`
		Decision       kernel.ManagedApprovalDecision `json:"decision"`
	}{source.Run.Revision, a})
	if err != nil {
		t.Fatal(err)
	}
	event := kernel.EventDraft{Type: "kernel.approval.dispatch_committed", Message: id, Data: raw}
	m := kernel.Mutation{Status: kernel.RunStatusRunning, State: json.RawMessage(`{}`), Events: []kernel.EventDraft{event}}
	if _, err = rt.Apply(ctx, source.Run.ID, source.Run.Revision, m); !errors.Is(err, kernel.ErrInvalidInput) {
		t.Fatalf("ordinary mutation manufactured native approval: %v", err)
	}
	if _, err = rt.ApplyIdentified(ctx, source.Run.ID, source.Run.Revision, "forged-approval", m); !errors.Is(err, kernel.ErrInvalidInput) {
		t.Fatalf("identified mutation manufactured native approval: %v", err)
	}
	if _, err = rt.Create(ctx, kernel.CreateRequest{ID: "forged", Kind: source.Run.Kind, Actor: source.Run.Actor, Thread: source.Run.Thread, Goal: source.Run.Goal, State: source.State, Events: []kernel.EventDraft{event}}); !errors.Is(err, kernel.ErrInvalidInput) {
		t.Fatalf("create manufactured native approval: %v", err)
	}
	if applied, err := rt.CommittedApprovalDispatch(ctx, source.Run.ID, "foreign-dispatch", a); err != nil || applied {
		t.Fatalf("uncommitted marker granted proof: %t %v", applied, err)
	}
	after, err := rt.Load(ctx, source.Run.ID)
	if err != nil || kernel.ManagedDigest(after) != kernel.ManagedDigest(source) {
		t.Fatalf("unqualified marker mutated source: %v", err)
	}
}
