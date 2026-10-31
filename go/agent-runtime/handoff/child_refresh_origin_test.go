package handoff_test

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestPR82HandoffRefreshRequiresParentOriginProof(t *testing.T) {
	parent := verifiedHandoffParent()
	valid := kernel.Snapshot{Run: kernel.Run{
		ID: "child", Kind: agent.RunKind, Status: kernel.RunStatusCompleted,
		Actor: parent.Run.Actor, Thread: parent.Run.Thread,
	}, Result: &kernel.Result{ContentType: "application/json", Content: []byte(`{"answer":"correct"}`)}}
	child := &unverifiedChildRunner{loadSnapshot: valid}
	coordinator, err := handoff.New(child)
	if err != nil {
		t.Fatal(err)
	}
	d := handoff.Delegation{ID: "delegate", MemberID: "member", ChildRunID: "child", Goal: "inspect", Status: handoff.StatusRunning}
	got, err := coordinator.Refresh(t.Context(), parent, d)
	if err != nil || got.Status != handoff.StatusCompleted || string(got.Result) != `{"answer":"correct"}` {
		t.Fatalf("valid parent/child refresh rejected: result=%+v err=%v", got, err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*kernel.Run)
	}{
		{"foreign-tenant", func(r *kernel.Run) { r.Actor.TenantID = "foreign" }},
		{"foreign-actor", func(r *kernel.Run) { r.Actor.ActorID = "foreign" }},
		{"foreign-thread", func(r *kernel.Run) { r.Thread.ID = "foreign" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			foreign := valid
			foreign.Run = valid.Run
			test.mutate(&foreign.Run)
			child.loadSnapshot = foreign
			projected, err := coordinator.Refresh(t.Context(), parent, d)
			if !errors.Is(err, handoff.ErrChildRunUnverified) || projected.ID != "" || len(projected.Result) != 0 {
				t.Fatalf("unrelated child refresh projected external result: %+v err=%v", projected, err)
			}
		})
	}
	_, err = coordinator.Refresh(t.Context(), kernel.Snapshot{}, d)
	if !errors.Is(err, handoff.ErrInvalidDelegation) {
		t.Fatalf("unbound parent unexpectedly authorized refresh: %v", err)
	}
}
