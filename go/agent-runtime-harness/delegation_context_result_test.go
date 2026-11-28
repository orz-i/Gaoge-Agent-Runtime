package harness

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestDelegationForwardsAnswerWithoutEmbeddingNativeSearchPages(t *testing.T) {
	f := newPR82HandoffFixture(t)
	delegation := f.delegation()
	if err := f.runner.prepareHandoffChild(t.Context(), f.turn, f.parent, delegation, f.startedItemID); err != nil {
		t.Fatal(err)
	}
	coordinator, err := handoff.New(handoff.Config{Local: &pr82BoundKernelChild{runtime: f.runtime}})
	if err != nil {
		t.Fatal(err)
	}
	f.runner.handoffs = coordinator
	if _, err = coordinator.StartOrLoad(t.Context(), f.parentRun, delegation); !errors.Is(err, handoff.ErrChildPending) {
		t.Fatal(err)
	}
	raw, err := json.Marshal(agent.Result{Content: "verified answer https://example.org/source", Citations: []string{"https://example.org/source"}, HostedToolCalls: []model.HostedToolCall{{ID: "native-receipt", ToolKey: "native.search", Status: "completed", Output: json.RawMessage(`{"page":"` + strings.Repeat("large web page ", 20_000) + `"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	delegation.Status, delegation.Result = handoff.StatusCompleted, raw
	receipt, err := f.runner.delegationToolReceipt(t.Context(), tools.ExecutionRequest{Call: tools.Call{ID: "result-call"}}, delegation, "")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct{ Result json.RawMessage }
	if err := json.Unmarshal(receipt.Content, &payload); err != nil {
		t.Fatal(err)
	}
	forwarded := payload.Result
	if len(forwarded) > 1024 || !strings.Contains(string(forwarded), "verified answer") || !strings.Contains(string(forwarded), "native-receipt") || !strings.Contains(string(forwarded), "https://example.org/source") {
		t.Fatalf("invalid supervisor projection: %d bytes", len(forwarded))
	}
	if !strings.Contains(string(raw), "large web page") {
		t.Fatal("exact child receipt was mutated")
	}
}
