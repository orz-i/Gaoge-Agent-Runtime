package harnesshttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

func TestPR72PrivatePhysicalIDsAreAbsentFromTimelineAndFeedProjections(t *testing.T) {
	for _, raw := range []string{
		`{"phase":"resolve-subtask-approval","physicalRunID":"secret-child-run","nested":{"run_id":"secret-nested-run"}}`,
		`{"boundary":"cancel_scope_pending","runID":"secret-cancel-run","refs":[{"executionRefID":"secret-ref-run"}]}`,
	} {
		item := harness.Item{
			ID: "probe", TurnID: "probe-turn", Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
			Payload: json.RawMessage(raw),
		}
		public, err := itemResponse(item)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(public.Payload, []byte("secret-")) {
			t.Errorf("public Timeline leaked Kernel IDs: %s", public.Payload)
		}
		if !bytes.Equal(item.Payload, []byte(raw)) {
			t.Errorf("public read mutated durable diagnostic: %s", item.Payload)
		}
		data, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		publicEvent, err := turnEventResponse(harness.TurnEvent{Type: harness.EventItemStarted,
			ItemKind: harness.ItemDiagnostic, Data: data})
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(publicEvent.Data, []byte("secret-")) {
			t.Errorf("public Turn Feed leaked Kernel IDs: %s", publicEvent.Data)
		}
		delta, err := turnEventResponse(harness.TurnEvent{Type: harness.EventItemDelta,
			ItemKind: harness.ItemDiagnostic, Data: json.RawMessage(raw)})
		if err != nil || bytes.Contains(delta.Data, []byte("secret-")) {
			t.Errorf("public diagnostic progress Feed leaked Kernel IDs: %s err=%v", delta.Data, err)
		}
	}
}

func TestPR72UnknownPhysicalStartUsesSafeConflictHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	writeHarnessError(ctx, errors.Join(harness.ErrExecutionOutcomeUnknown, errors.New("upstream secret debug")))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "harness.execution_outcome_unknown") || strings.Contains(w.Body.String(), "upstream secret") {
		t.Fatalf("outcome-unknown HTTP classification/status leaked details: %d %s", w.Code, w.Body.String())
	}
}
