package handoff_test

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

type auditKernelChildren struct {
	runtime *kernel.Runtime
	starts  int
}

func (c *auditKernelChildren) LoadRun(ctx context.Context, id string) (kernel.Snapshot, error) {
	return c.runtime.Load(ctx, id)
}
func (c *auditKernelChildren) StartRun(ctx context.Context, r agent.StartRequest) (kernel.Snapshot, error) {
	c.starts++
	return c.runtime.Create(ctx, kernel.CreateRequest{ID: r.ID, Kind: agent.RunKind, Actor: r.Actor, Thread: r.Thread, RequestID: r.RequestID, Goal: r.Goal, State: []byte(`{}`)})
}
func TestAuditHandoffKernelCanonicalInputs(t *testing.T) {
	for _, field := range []string{"goal", "child-id", "delegation-id", "actor", "thread"} {
		t.Run(field, func(t *testing.T) {
			rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
			if err != nil {
				t.Fatal(err)
			}
			child := &auditKernelChildren{runtime: rt}
			coordinator, err := handoff.New(child)
			if err != nil {
				t.Fatal(err)
			}
			parent := kernel.Snapshot{Run: kernel.Run{ID: "parent", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "chat", ID: "thread"}}}
			d := handoff.Delegation{ID: "delegation", MemberID: "member", ChildRunID: "child", Goal: "task", Status: handoff.StatusQueued}
			switch field {
			case "goal":
				d.Goal = " task "
			case "child-id":
				d.ChildRunID = " child "
			case "delegation-id":
				d.ID = "delegation "
			case "actor":
				parent.Run.Actor.ActorID = " actor "
			case "thread":
				parent.Run.Thread.ID = " thread "
			}
			got, err := coordinator.StartOrLoad(t.Context(), parent, d)
			persisted, loadErr := rt.Load(t.Context(), d.ChildRunID)
			t.Logf("field=%s starts=%d result=%+v error=%v persisted=%+v loadError=%v", field, child.starts, got, err, persisted.Run, loadErr)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if !errors.Is(err, handoff.ErrChildPending) || got.ChildRunID == "" {
				t.Errorf("valid input performed physical Start but could not project persisted child")
			}
			_, refreshErr := coordinator.Refresh(t.Context(), parent, d)
			if !errors.Is(refreshErr, handoff.ErrChildPending) {
				t.Errorf("cannot recover persisted child: %v", refreshErr)
			}
		})
	}
}
