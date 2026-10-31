package handoff_test

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Matching ID and terminal status do not prove the ChildRunner returned an
// Agent-compatible Run. A Workflow/Team Run must never close the Handoff grant
// or masquerade as a successfully delegated specialist.
func TestPR82HandoffRejectsWrongChildRunKind(t *testing.T) {
	const id = "pr82-typed-child"
	for _, kind := range []kernel.RunKind{"", "workflow", "team", "group_chat", "unrecognized"} {
		for _, source := range []string{"load", "start", "refresh"} {
			t.Run(string(kind)+"/"+source, func(t *testing.T) {
				childSnapshot := kernel.Snapshot{Run: kernel.Run{
					ID: id, Kind: kind, Status: kernel.RunStatusCompleted,
				}}
				child := &unverifiedChildRunner{
					loadSnapshot: childSnapshot, startSnapshot: childSnapshot,
				}
				if source == "start" {
					child.loadErr = kernel.ErrNotFound
				}
				coordinator, err := handoff.New(child)
				if err != nil {
					t.Fatal(err)
				}
				delegation := handoff.Delegation{
					ID: "pr82-kind-check", MemberID: "member", ChildRunID: id,
					Goal: "specialist analysis", Status: handoff.StatusQueued,
				}
				var result handoff.Delegation
				if source == "refresh" {
					result, err = coordinator.Refresh(t.Context(), verifiedHandoffParent(), delegation)
				} else {
					result, err = coordinator.StartOrLoad(t.Context(), verifiedHandoffParent(), delegation)
				}
				if !errors.Is(err, handoff.ErrChildRunUnverified) || result.ID != "" {
					t.Fatalf("accepted child Run kind %q from %s: result=%+v err=%v", kind, source, result, err)
				}
			})
		}
	}
}
