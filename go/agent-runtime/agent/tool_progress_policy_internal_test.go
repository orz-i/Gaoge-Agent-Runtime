package agent

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestToolProgressKeepsExplicitDenialAndUnrelatedToolsBlocked(t *testing.T) {
	state := runState{ToolProgressPolicy: &ToolProgressPolicy{ExplorationToolKeys: []string{"lookup"}},
		Messages: unsettledToolHistory("lookup", `{"ok":false,"retryable":false,"error":{"code":"delegation.current_evidence_unavailable"}}`)}
	if got := progressBlocks(state, []string{"lookup", "write"}); !slices.Equal(got, []string{"lookup", "write"}) {
		t.Fatalf("explicit denial was weakened: %v", got)
	}
	state.Messages = nil
	if got := progressBlocks(state, []string{"lookup", "write"}); !slices.Equal(got, []string{"write"}) {
		t.Fatalf("policy escaped explicit allowlist: %v", got)
	}
}

func TestToolProgressRejectsInvalidPolicy(t *testing.T) {
	for _, keys := range [][]string{{"lookup", "lookup"}, {""}, {ToolDiscoveryKey}, {"harness.delegate_agent"}, {"terminal"}, {"missing"}} {
		if _, err := normalizeToolProgressPolicy(&ToolProgressPolicy{ExplorationToolKeys: keys}, progressCatalog{}); err == nil {
			t.Fatalf("invalid policy accepted: %v", keys)
		}
	}
	if validFrozenToolProgressPolicy(runState{ToolKeys: []string{"lookup"}, ToolProgressPolicy: &ToolProgressPolicy{ExplorationToolKeys: []string{"ungranted"}}}) {
		t.Fatal("frozen policy accepted an ungranted Tool")
	}
}

type progressCatalog struct{}

func (progressCatalog) Resolve(key string) (tools.Definition, bool) {
	return tools.Definition{Key: key, Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`), Terminal: key == "terminal"}, key != "missing"
}

func (progressCatalog) List([]string) ([]tools.Definition, error) { return nil, nil }

func TestToolProgressCountsSettledCallsOnly(t *testing.T) {
	state := runState{ToolProgressPolicy: &ToolProgressPolicy{ExplorationToolKeys: []string{"lookup"}}}
	call := tools.Call{ID: "pending", ToolKey: "lookup", Arguments: json.RawMessage(`{}`)}
	state.Messages = []model.Message{{Role: model.RoleAssistant, ToolCalls: []tools.Call{call, call}}}
	if repeatedExplorationCall(state, call) {
		t.Fatal("pending assistant calls fabricated settled evidence")
	}
}
