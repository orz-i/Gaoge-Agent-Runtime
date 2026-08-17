package harnesshttp

import (
	"encoding/json"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge/sdk/go/agent-runtime-harness"
)

const testHarnessTurnID = "ht-1"

func TestSnapshotResponseMasksTerminalHostOutputUntilProjectionAcknowledgement(t *testing.T) {
	now := time.Date(2026, time.August, 17, 0, 0, 0, 0, time.UTC)
	started := harness.Item{
		ID: "assistant-message", TurnID: testHarnessTurnID, Seq: 1,
		Kind: harness.ItemAgentMessage, Status: harness.ItemStarted,
		HostRef: &harness.HostRef{Kind: "conversation_message", ID: "assistant-1"},
		CreatedAt: now, UpdatedAt: now,
	}
	snapshot := harness.Snapshot{
		Turn: harness.Turn{
			ID: testHarnessTurnID, HostTurn: harness.HostRef{Kind: "conversation_turn", ID: "client-1"},
			Status: harness.TurnCompleted, Revision: 3, CreatedAt: now, UpdatedAt: now,
		},
		Items:  []harness.Item{started},
		Output: &harness.Output{ContentType: "text", Content: json.RawMessage(`"final"`)},
	}

	pending, err := snapshotResponse(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Turn.Status != harness.TurnRunning || pending.Output != nil {
		t.Fatalf("terminal host output leaked before projection acknowledgement: %#v", pending)
	}

	snapshot.Items = append(snapshot.Items, harness.Item{
		ID: "assistant-message-completed", TurnID: testHarnessTurnID, Seq: 2,
		Kind: harness.ItemAgentMessage, Status: harness.ItemCompleted,
		ParentItemID: started.ID, HostRef: started.HostRef,
		CreatedAt: now, UpdatedAt: now,
	})
	finalized, err := snapshotResponse(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if finalized.Turn.Status != harness.TurnCompleted || finalized.Output == nil || string(finalized.Output.Content) != `"final"` {
		t.Fatalf("acknowledged terminal host output was not exposed: %#v", finalized)
	}
}
