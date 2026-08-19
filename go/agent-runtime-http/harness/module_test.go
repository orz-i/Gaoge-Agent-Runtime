package harnesshttp

import (
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	harness "github.com/orz-i/Gaoge/sdk/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runfeed"
)

const testHarnessTurnID = "ht-1"

func TestSnapshotResponseMasksTerminalHostOutputUntilProjectionAcknowledgement(t *testing.T) {
	now := time.Date(2026, time.August, 17, 0, 0, 0, 0, time.UTC)
	started := harness.Item{
		ID: "assistant-message", TurnID: testHarnessTurnID, Seq: 1,
		Kind: harness.ItemAgentMessage, Status: harness.ItemStarted,
		HostRef:   &harness.HostRef{Kind: "conversation_message", ID: "assistant-1"},
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

func TestSnapshotResponseProjectsInvocationWithoutRuntimeExecutionIdentity(t *testing.T) {
	now := time.Date(2026, time.August, 17, 0, 0, 0, 0, time.UTC)
	invocation := harness.Invocation{
		ID: "hiv-1", TurnID: testHarnessTurnID, CapabilityKey: "runtime.agent", DefinitionVersion: "v1",
		ExecutionClass: harness.ExecutionAgent, InputHash: strings.Repeat("a", 64), ExecutionRefID: "private-run-1",
		Status: harness.InvocationRunning, Attempt: 1, OutputRefs: []harness.HostRef{}, Revision: 2,
		CreatedAt: now, UpdatedAt: now,
	}
	payload := json.RawMessage(`{"capabilityKey":"runtime.agent","executionClass":"agent","executionRefID":"private-run-1","attempt":1}`)
	snapshot := harness.Snapshot{
		Turn: harness.Turn{
			ID: testHarnessTurnID, HostTurn: harness.HostRef{Kind: "conversation_turn", ID: "client-1"},
			Status: harness.TurnRunning, Revision: 2, CreatedAt: now, UpdatedAt: now,
		},
		Invocations: []harness.Invocation{invocation},
		Items: []harness.Item{{
			ID: "hiv-item-1", TurnID: testHarnessTurnID, Seq: 1, Kind: harness.ItemInvocation,
			Status: harness.ItemStarted, RunID: "private-run-1", InvocationID: invocation.ID,
			Payload: payload, CreatedAt: now, UpdatedAt: now,
		}},
	}

	response, err := snapshotResponse(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Invocations) != 1 || response.Invocations[0].ID != invocation.ID ||
		len(response.Items) != 1 || response.Items[0].InvocationID != invocation.ID {
		t.Fatalf("invocation projection = %#v", response)
	}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-run-1") || strings.Contains(string(response.Items[0].Payload), "executionRefID") {
		t.Fatalf("Runtime execution identity leaked through Harness HTTP projection: %s", raw)
	}
}

func TestWriteTurnFeedCursorExpiredReturnsRecoveryHead(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)

	if !writeTurnFeedCursorExpired(context, &runfeed.CursorExpiredError{AfterSeq: 3, HeadSeq: 9}) {
		t.Fatal("cursor expiry was not handled")
	}
	if recorder.Code != stdhttp.StatusConflict || recorder.Header().Get("X-Harness-Feed-Head") != "9" ||
		!strings.Contains(recorder.Body.String(), `"code":"harness.feed_cursor_expired"`) {
		t.Fatalf("cursor response = %d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}
