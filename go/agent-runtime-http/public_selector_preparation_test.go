package http

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runfeed"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workbench"
)

const selectorPrivateInput = "PRIVATE_SELECTOR_PREPARED_INPUT"
const selectorSensitivePayload = `{
	"selectorInvocation":{"request":{"goal":"visible selector goal","preparedInput":{"route":"PRIVATE_SELECTOR_PREPARED_INPUT"}}},
	"result":{"content":{"text":"visible selector result","prepared_input":"PRIVATE_SELECTOR_PREPARED_INPUT"}},
	"input":{"prepared-input":"PRIVATE_SELECTOR_PREPARED_INPUT"},
	"PreparedInput":{"raw":"PRIVATE_SELECTOR_PREPARED_INPUT"}
}`

func assertNoSelectorPreparedInput(t *testing.T, payload []byte) {
	t.Helper()
	for _, private := range []string{selectorPrivateInput, "preparedInput", "prepared_input", "prepared-input", "PreparedInput"} {
		if bytes.Contains(payload, []byte(private)) {
			t.Errorf("public projection leaked private selector input marker %q", private)
		}
	}
	for _, visible := range []string{"visible selector goal", "visible selector result"} {
		if !bytes.Contains(payload, []byte(visible)) {
			t.Errorf("public projection removed authored selector content %q", visible)
		}
	}
}

func TestSelectorPreparedInputRemovedFromPublicDiagnosticSurfaces(t *testing.T) {
	payload := json.RawMessage(selectorSensitivePayload)
	original := bytes.Clone(payload)
	public := PublicDiagnosticPayload(payload)
	assertNoSelectorPreparedInput(t, public)
	assertNoSelectorPreparedInput(t, PublicDiagnosticPayload(public))
	snapshot := kernel.Snapshot{Run: kernel.Run{Kind: "group_chat", ID: "selector-private"}, State: payload}
	assertNoSelectorPreparedInput(t, SnapshotResponse(snapshot).State)
	detail := workbench.Detail{
		Run:      snapshot.Run,
		Sections: []workbench.Section{{Name: "state", Available: true, Content: payload}},
		Timeline: []workbench.TimelineItem{{ID: "selector-event", Kind: "event", Data: payload}},
		Topology: &workbench.TopologyV1{
			Nodes: []workbench.TopologyNode{{ID: "selector-node", Kind: "selector", Data: payload}},
			Edges: []workbench.TopologyEdge{{ID: "selector-edge", Data: payload}},
		},
	}
	encoded, err := json.Marshal(publicWorkbench(detail))
	if err != nil {
		t.Fatal(err)
	}
	assertNoSelectorPreparedInput(t, encoded)
	encoded, err = json.Marshal(publicRunFeedEvent(runfeed.Event{RunID: snapshot.Run.ID, Type: runfeed.EventRunStarted, Data: payload}))
	if err != nil {
		t.Fatal(err)
	}
	assertNoSelectorPreparedInput(t, encoded)
	if !bytes.Equal(payload, original) || !bytes.Equal(snapshot.State, original) || !bytes.Equal(detail.Sections[0].Content, original) {
		t.Fatal("public projection mutated authoritative selector input")
	}
}
