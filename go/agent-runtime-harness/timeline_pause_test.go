package harness

import (
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestHostedPendingReceiptCanCompleteInLaterModelResponse(t *testing.T) {
	store, turn, _, invocation := contextRunMiddlewareFixture(t)
	call := model.HostedToolCall{ID: "paused_native_search", ToolKey: "native.search", Status: "in_progress", Input: json.RawMessage(`{"query":"news"}`)}
	request := model.Request{RunID: invocation.ExecutionRefID}
	if err := appendHostedToolTimelineItem(t.Context(), store, groupChatHandoffSystemClock{}, nil, turn, invocation, request, call, nil, 0); err != nil {
		t.Fatal(err)
	}
	call.Input = json.RawMessage(`{"query":"news","freshness":"day"}`)
	if err := appendHostedToolTimelineItem(t.Context(), store, groupChatHandoffSystemClock{}, nil, turn, invocation, request, call, nil, 0); err != nil {
		t.Fatalf("reobserving a pending search must not rewrite its started fact: %v", err)
	}
	call.Status, call.Output = "completed", json.RawMessage(`{"source":"https://example.org/news"}`)
	if err := appendHostedToolTimelineItem(t.Context(), store, groupChatHandoffSystemClock{}, nil, turn, invocation, request, call, nil, 0); err != nil {
		t.Fatalf("pending receipt must not conflict with the actual completion: %v", err)
	}
	items, err := listAllItems(t.Context(), store, turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	started, completed := 0, 0
	for _, item := range items {
		if item.Kind != ItemTool {
			continue
		}
		if item.Status == ItemStarted {
			started++
		}
		if item.Status == ItemCompleted {
			completed++
		}
	}
	if started != 1 || completed != 1 {
		t.Fatalf("invalid paused search lifecycle: started=%d completed=%d", started, completed)
	}
}
