package harnesshttp

import (
	"encoding/json"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

func TestPublicHarnessSnapshotAndFeedRedactAllFailureDetails(t *testing.T) {
	t.Parallel()
	const private = "Bearer sk-PRIVATE-HARNESS-ERROR"
	snapshot := harness.Snapshot{
		Turn: harness.Turn{
			ID: testHarnessTurnID, Status: harness.TurnRunning,
			ErrorCode: "model.upstream_auth_failed", ErrorDetail: private,
		},
		Invocations: []harness.Invocation{{
			ID: testHarnessInvocationID, TurnID: testHarnessTurnID,
			Status: harness.InvocationFailed, ErrorCode: "model.upstream_auth_failed",
			ErrorDetail: private,
		}},
		Subtasks: []harness.Subtask{{
			ID: "child", Status: "failed", ErrorCode: "model.upstream_auth_failed",
			ErrorDetail: private, CancelStatus: "failed", CancelError: private,
		}},
		Items: []harness.Item{{
			ID: "tool-item", TurnID: testHarnessTurnID, Kind: harness.ItemAgentMessage,
			Payload: json.RawMessage(`{"content":"safe answer","upstreamDebug":{"body":"Bearer sk-PRIVATE-HARNESS-ERROR"}}`),
		}},
	}
	public, err := snapshotResponse(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), private) || !strings.Contains(string(body), "model.upstream_auth_failed") ||
		!strings.Contains(string(body), "safe answer") {
		t.Fatalf("owner-visible snapshot leaked diagnostic details: %s", body)
	}
	if snapshot.Turn.ErrorDetail != private || snapshot.Subtasks[0].CancelError != private {
		t.Fatal("public projection mutated Harness durable facts")
	}
	event, err := turnEventResponse(harness.TurnEvent{
		TurnID: testHarnessTurnID, Type: harness.EventTurnFailed, Status: "failed",
		Message: private, Data: json.RawMessage(`{"errorDetail":"Bearer sk-PRIVATE-HARNESS-ERROR","progress":5}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	eventJSON, _ := json.Marshal(event)
	if strings.Contains(string(eventJSON), private) || !strings.Contains(string(eventJSON), "progress") {
		t.Fatalf("Harness terminal feed leaked diagnostic detail: %s", eventJSON)
	}
}
