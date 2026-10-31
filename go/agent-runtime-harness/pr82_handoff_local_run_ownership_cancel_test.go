package harness

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// The same local RunID is not sufficient cancellation authority for a routed
// Handoff. Every frozen execution-origin field must match the child task.
func TestPR82HandoffCancellationRejectsForeignLocalRunCollision(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*kernel.CreateRequest)
	}{
		{"tenant", func(v *kernel.CreateRequest) { v.Actor.TenantID = "foreign-tenant" }},
		{"actor", func(v *kernel.CreateRequest) { v.Actor.ActorID = "foreign-actor" }},
		{"thread-kind", func(v *kernel.CreateRequest) { v.Thread.Kind = "foreign-kind" }},
		{"thread-id", func(v *kernel.CreateRequest) { v.Thread.ID = "foreign-thread" }},
		{"goal", func(v *kernel.CreateRequest) { v.Goal = "foreign-goal" }},
		{"request-id", func(v *kernel.CreateRequest) { v.RequestID = "foreign-request" }},
		{"run-kind", func(v *kernel.CreateRequest) { v.Kind = kernel.RunKind("foreign.agent") }},
	}
	for _, role := range []bool{false, true} {
		routeName := "member"
		if role {
			routeName = "role"
		}
		for _, tt := range tests {
			t.Run(routeName+"/"+tt.name, func(t *testing.T) {
				f, child := pr82StartedRemoteHandoff(t, role)
				delegation := f.delegation()
				req := kernel.CreateRequest{
					ID: child.ExecutionRefID, Kind: agent.RunKind,
					Actor: f.parentRun.Run.Actor, Thread: f.parentRun.Run.Thread,
					Goal: delegation.Goal, RequestID: f.parentRun.Run.ID + ":" + delegation.ID, State: []byte(`{}`),
				}
				tt.mutate(&req)
				if _, err := f.runtime.Create(t.Context(), req); err != nil {
					t.Fatalf("seed foreign local Run: %v", err)
				}
				_, cancelErr := f.runner.CancelInvocation(t.Context(), child.ID, "stop routed child")
				if !errors.Is(cancelErr, ErrHandoffCancellationUnverified) {
					t.Fatalf("foreign local Run accepted as delegated cancellation proof: %v", cancelErr)
				}
				foreign, err := f.runtime.Load(t.Context(), child.ExecutionRefID)
				if err != nil || foreign.Run.Status != kernel.RunStatusRunning {
					t.Fatalf("foreign Kernel Run was altered: status=%s err=%v", foreign.Run.Status, err)
				}
				current, err := f.store.GetInvocation(t.Context(), child.ID)
				if err != nil || current.Status != child.Status || current.Revision != child.Revision {
					t.Fatalf("Handoff was falsely terminalized: got=%+v err=%v", current, err)
				}
				items, err := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
				if err != nil || !ActiveCancellationFence(items) {
					t.Fatalf("unverified cancellation did not retain intent: %v", err)
				}
			})
		}
	}
}
