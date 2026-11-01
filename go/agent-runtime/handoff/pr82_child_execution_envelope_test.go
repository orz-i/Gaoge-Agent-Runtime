package handoff_test

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// An existing child with the correct ID, Actor, Thread and RunKind may still
// belong to another goal/delegation. Do not project its Result or authorize
// a Handoff completion receipt without the exact frozen Start identity.
func TestPR82HandoffChildMustMatchFrozenGoalAndRequestID(t *testing.T) {
	parent := verifiedHandoffParent()
	request := handoff.Delegation{
		ID: "delegation-1", MemberID: "analyst", ChildRunID: "child-1",
		Goal: "authorized work", Status: handoff.StatusQueued,
	}
	run := kernel.Run{
		ID: request.ChildRunID, Kind: agent.RunKind,
		Actor: parent.Run.Actor, Thread: parent.Run.Thread,
		RequestID: parent.Run.ID + ":" + request.ID, Goal: request.Goal,
		Status: kernel.RunStatusCompleted,
	}
	for _, tc := range []struct {
		name   string
		change func(*kernel.Run)
	}{
		{"goal", func(run *kernel.Run) { run.Goal = "other task" }},
		{"request", func(run *kernel.Run) { run.RequestID = "other-parent:other-delegation" }},
		{"blank-goal", func(run *kernel.Run) { run.Goal = "" }},
		{"blank-request", func(run *kernel.Run) { run.RequestID = "" }},
	} {
		for _, path := range []string{"load", "start", "refresh"} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				forged := run
				tc.change(&forged)
				child := &unverifiedChildRunner{
					loadSnapshot: kernel.Snapshot{Run: forged, Result: &kernel.Result{
						ContentType: "application/json", Content: []byte(`{"foreign":"result"}`),
					}},
					startSnapshot: kernel.Snapshot{Run: forged},
				}
				if path == "start" {
					child.loadErr = kernel.ErrNotFound
				}
				coordinator, err := handoff.New(child)
				if err != nil {
					t.Fatal(err)
				}
				var result handoff.Delegation
				if path == "refresh" {
					result, err = coordinator.Refresh(t.Context(), parent, request)
				} else {
					result, err = coordinator.StartOrLoad(t.Context(), parent, request)
				}
				if !errors.Is(err, handoff.ErrChildRunUnverified) || result.ID != "" || len(result.Result) != 0 {
					t.Fatalf("foreign execution was projected: %+v err=%v", result, err)
				}
			})
		}
	}
}
