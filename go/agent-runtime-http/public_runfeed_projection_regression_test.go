package http

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runfeed"
)

// The same writer serves historical SSE replay and live Run Feed events.
// Assert the actual over-the-wire response, not only the projection helper.
func TestRunFeedSSEWriterNeverEmitsRequestedToolOrApprovalArguments(t *testing.T) {
	const private = "DUMMY_PRIVATE_SSE_PR56"
	events := []runfeed.Event{
		{Seq: 1, RunID: "child", Type: runfeed.EventToolRequested,
			Data: json.RawMessage(`{"id":"tool-id","toolKey":"mcp.web_search","arguments":{"query":"DUMMY_PRIVATE_SSE_PR56"}}`)},
		{Seq: 2, RunID: "child", Type: runfeed.EventInteractionRequired,
			Data: json.RawMessage(`{"id":"checkpoint-one","kind":"tool_approval","status":"pending","payload":{"toolCallID":"tool-id","toolKey":"mcp.web_search","arguments":{"token":"DUMMY_PRIVATE_SSE_PR56"}}}`)},
	}
	writer := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(writer)
	cursor, closed := writeRunFeedReplay(context, events, 0)
	encoded := writer.Body.String()
	if cursor != 2 || closed || strings.Contains(encoded, private) || strings.Contains(encoded, `"arguments"`) ||
		!strings.Contains(encoded, `"status":"requested"`) || !strings.Contains(encoded, `"id":"checkpoint-one"`) {
		t.Fatalf("public SSE replay exposes private Tool context: last=%d stopped=%v SSE=%s", cursor, closed, encoded)
	}
	for _, event := range events {
		if err := writeRunFeedEvent(context, event); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(writer.Body.String(), private) {
		t.Fatal("live feed writer leaked private parameters")
	}
}
