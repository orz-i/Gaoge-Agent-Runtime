package handoff_test

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func verifiedHandoffParent() kernel.Snapshot {
	return kernel.Snapshot{Run: kernel.Run{
		ID: "parent", Actor: kernel.ActorRef{TenantID: "tenant-A", ActorID: "actor-A"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread-A"},
	}}
}

// A matching child Run ID/status/kind alone does not authorize projecting a
// different tenant's execution. The parent is the frozen owner of this
// delegation; start/load/refresh must reject any foreign Actor or Thread.
func TestPR82HandoffRejectsForeignChildExecutionOrigin(t *testing.T) {
	parent := verifiedHandoffParent()
	expected := kernel.Run{
		ID: "child", Kind: agent.RunKind, Status: kernel.RunStatusCompleted,
		Actor: parent.Run.Actor, Thread: parent.Run.Thread,
	}
	for _, test := range []struct {
		name   string
		mutate func(*kernel.Run)
	}{
		{"tenant", func(r *kernel.Run) { r.Actor.TenantID = "tenant-B" }},
		{"actor", func(r *kernel.Run) { r.Actor.ActorID = "actor-B" }},
		{"thread-kind", func(r *kernel.Run) { r.Thread.Kind = "different" }},
		{"thread-id", func(r *kernel.Run) { r.Thread.ID = "thread-B" }},
		{"missing-tenant", func(r *kernel.Run) { r.Actor.TenantID = "" }},
	} {
		for _, source := range []string{"load", "start"} {
			t.Run(test.name+"/"+source, func(t *testing.T) {
				run := expected
				test.mutate(&run)
				child := &unverifiedChildRunner{
					loadSnapshot:  kernel.Snapshot{Run: run},
					startSnapshot: kernel.Snapshot{Run: run},
				}
				if source == "start" {
					child.loadErr = kernel.ErrNotFound
				}
				coordinator, err := handoff.New(child)
				if err != nil {
					t.Fatal(err)
				}
				request := handoff.Delegation{
					ID: "delegation", MemberID: "member", ChildRunID: "child",
					Goal: "task", Status: handoff.StatusQueued,
				}
				projected, err := coordinator.StartOrLoad(t.Context(), parent, request)
				if !errors.Is(err, handoff.ErrChildRunUnverified) ||
					projected.ID != "" || child.starts != map[bool]int{false: 0, true: 1}[source == "start"] {
					t.Fatalf("foreign Run was projected: %+v err=%v starts=%d", projected, err, child.starts)
				}
			})
		}
	}
}
