package harnesspostgres_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

func TestStoreProjectsChildWaitBeforeParentSegmentYields(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		status       harness.TurnStatus
		parentItemID string
		wantConflict bool
	}{
		{name: "accepted parent", status: harness.TurnAccepted, parentItemID: "parent-tool"},
		{name: "running parent", status: harness.TurnRunning, parentItemID: "parent-tool"},
		{name: "unrelated root mismatch", status: harness.TurnRunning, wantConflict: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			store := newStore(t)
			now := time.Now().UTC()
			turn, _, err := store.CreateTurn(t.Context(), harness.Turn{
				ID: "child-wait-turn", SessionID: "child-wait-session", HostTurn: harness.HostRef{Kind: "conversation_turn", ID: "child-wait-host"},
				ConfigSnapshotID: "child-wait-config", Status: scenario.status, Revision: 1, CreatedAt: now, UpdatedAt: now,
			})
			if err != nil {
				t.Fatal(err)
			}
			invocation, _, err := store.CreateInvocation(t.Context(), harness.Invocation{
				ID: "child-workflow", TurnID: turn.ID, ParentItemID: scenario.parentItemID, CapabilityKey: "runtime.workflow", DefinitionVersion: "v1",
				ExecutionClass: harness.ExecutionWorkflow, ExecutionRefID: "child-workflow-run", Status: harness.InvocationWaitingInput,
				Attempt: 1, OutputRefs: []harness.HostRef{}, Revision: 1, CreatedAt: now, UpdatedAt: now,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, fresh, err := store.CreateInteraction(t.Context(), harness.Interaction{
				ID: "child-question", TurnID: turn.ID, InvocationID: invocation.ID, Key: "wait", Kind: harness.InteractionInput,
				Schema: json.RawMessage(`{"type":"object"}`), Status: harness.InteractionWaiting, Revision: 1, CreatedAt: now, UpdatedAt: now,
			}, turn.Revision, invocation.Revision)
			if scenario.wantConflict {
				if !errors.Is(err, harness.ErrConflict) {
					t.Fatalf("root mismatch error=%v", err)
				}
			} else if err != nil || !fresh {
				t.Fatalf("child wait fresh=%v err=%v", fresh, err)
			}
		})
	}
}
