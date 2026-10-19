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
			Approval: &harness.SubtaskApproval{CheckpointID: "checkpoint-1", ToolKey: "example.read", ToolName: "Read data", Arguments: json.RawMessage(`{"apiKey":"PRIVATE_SUBTASK_TOOL_ARGUMENT","request":{"personal":"PRIVATE_SUBTASK_DATA"}}`)},
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
	if strings.Contains(string(body), private) || strings.Contains(string(body), "PRIVATE_SUBTASK_TOOL_ARGUMENT") ||
		strings.Contains(string(body), "PRIVATE_SUBTASK_DATA") || !strings.Contains(string(body), "checkpoint-1") ||
		!strings.Contains(string(body), "model.upstream_auth_failed") ||
		!strings.Contains(string(body), "safe answer") {
		t.Fatalf("owner-visible snapshot leaked diagnostic details: %s", body)
	}
	if string(snapshot.Subtasks[0].Approval.Arguments) != `{"apiKey":"PRIVATE_SUBTASK_TOOL_ARGUMENT","request":{"personal":"PRIVATE_SUBTASK_DATA"}}` {
		t.Fatal("public approval projection mutated durable arguments")
	}
	subtaskPayload, err := json.Marshal(snapshot.Subtasks[0])
	if err != nil {
		t.Fatal(err)
	}
	itemPayload, err := publicItemPayload(harness.ItemSubtask, subtaskPayload)
	if err != nil || strings.Contains(string(itemPayload), "PRIVATE_SUBTASK_TOOL_ARGUMENT") || strings.Contains(string(itemPayload), "PRIVATE_SUBTASK_DATA") {
		t.Fatalf("subtask feed item leaked raw approval arguments: %s, %v", itemPayload, err)
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

// Approval request arguments belong to the durable internal checkpoint, not
// the owner-visible Harness timeline. The public snapshot and streamed item
// lifecycle must agree, while retaining the exact approval identity and the
// explicitly authored approve/reject decision.
func TestPublicHarnessApprovalItemOmitsRawArgumentsInSnapshotAndFeed(t *testing.T) {
	t.Parallel()
	const secret = "DUMMY_PRIVATE_APPROVAL_PASSWORD"
	pending := harness.Item{
		ID: "approval-pending", TurnID: testHarnessTurnID, Kind: harness.ItemApproval,
		Status:  harness.ItemWaiting,
		Payload: json.RawMessage(`{"subtask":true,"checkpointID":"checkpoint-1","toolCallID":"call-1","toolKey":"example.write","toolName":"Write file","arguments":{"password":"DUMMY_PRIVATE_APPROVAL_PASSWORD"},"unexpectedProviderData":{"token":"DUMMY_PRIVATE_APPROVAL_PASSWORD"}}`),
	}
	decision := harness.Item{
		ID: "approval-decision", TurnID: testHarnessTurnID, Kind: harness.ItemApproval,
		Status: harness.ItemCompleted, ParentItemID: pending.ID,
		Payload: json.RawMessage(`{"subtask":true,"checkpointID":"checkpoint-1","decision":"reject","comment":"Rejected by owner"}`),
	}
	response, err := snapshotResponse(harness.Snapshot{Items: []harness.Item{pending, decision}})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range response.Items {
		assertPublicApprovalItem(t, item.Payload, secret)
	}
	for _, test := range []struct {
		name     string
		typeName string
		item     harness.Item
	}{
		{name: "started", typeName: harness.EventItemStarted, item: pending},
		{name: "completed", typeName: harness.EventItemCompleted, item: decision},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(test.item)
			if err != nil {
				t.Fatal(err)
			}
			projected, err := turnEventResponse(harness.TurnEvent{
				TurnID: testHarnessTurnID, Type: test.typeName,
				ItemID: test.item.ID, ItemKind: harness.ItemApproval, Data: raw, Message: secret, Delta: secret,
			})
			if err != nil {
				t.Fatal(err)
			}
			var eventItem ItemResponse
			if err := json.Unmarshal(projected.Data, &eventItem); err != nil {
				t.Fatal(err)
			}
			if eventItem.ID != test.item.ID || eventItem.Kind != harness.ItemApproval {
				t.Fatalf("public approval event lost its item identity: %#v", eventItem)
			}
			if projected.Message != "" || projected.Delta != "" {
				t.Fatalf("approval event leaked untrusted text: %#v", projected)
			}
			assertPublicApprovalItem(t, eventItem.Payload, secret)
		})
	}
	// Approval events never have a public text delta. A malformed or future
	// producer must not bypass ItemApproval's typed projection via event fields.
	delta, err := turnEventResponse(harness.TurnEvent{
		TurnID: testHarnessTurnID, Type: harness.EventItemDelta,
		ItemID: pending.ID, ItemKind: harness.ItemApproval,
		Delta: secret, Message: secret,
		Data: json.RawMessage(`{"arguments":{"password":"DUMMY_PRIVATE_APPROVAL_PASSWORD"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if delta.Delta != "" || delta.Message != "" || len(delta.Data) != 0 {
		t.Fatalf("approval item delta leaked through its alternate fields: %#v", delta)
	}
	if !strings.Contains(string(pending.Payload), secret) {
		t.Fatal("projection mutated the original internal approval request")
	}
}

func assertPublicApprovalItem(t *testing.T, payload json.RawMessage, secret string) {
	t.Helper()
	if strings.Contains(string(payload), secret) {
		t.Fatalf("private approval argument escaped through public projection: %s", payload)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["checkpointID"]) != `"checkpoint-1"` || string(fields["subtask"]) != "true" {
		t.Fatalf("public approval identity or subtask marker lost: %s", payload)
	}
	if _, unexpected := fields["unexpectedProviderData"]; unexpected {
		t.Fatalf("unrecognized private approval field exported: %s", payload)
	}
	if string(fields["decision"]) == `"reject"` {
		if string(fields["comment"]) != `"Rejected by owner"` {
			t.Fatalf("approval decision content lost: %s", payload)
		}
		return
	}
	if string(fields["toolCallID"]) != `"call-1"` || string(fields["toolKey"]) != `"example.write"` ||
		string(fields["toolName"]) != `"Write file"` || string(fields["arguments"]) != "{}" {
		t.Fatalf("safe approval summary missing or private arguments not empty: %s", payload)
	}
}
