package agent

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestToolProgressSkipsHistoryWithoutPolicyOrCandidates(t *testing.T) {
	state := runState{Messages: uniqueToolPolicyTranscript(500)}
	keys := []string{"lookup"}
	for _, tc := range []struct {
		name   string
		policy *ToolProgressPolicy
		keys   []string
	}{
		{"disabled", nil, keys},
		{"no candidates", &ToolProgressPolicy{ExplorationToolKeys: keys}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state.ToolProgressPolicy = tc.policy
			var got []string
			allocs := testing.AllocsPerRun(5, func() { got = progressBlocks(state, tc.keys) })
			if !slices.Equal(got, tc.keys) || allocs != 0 {
				t.Fatalf("blocked=%v allocations=%v", got, allocs)
			}
		})
	}
}

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
	for _, keys := range [][]string{{"lookup", "lookup"}, {""}, {ToolDiscoveryKey}, {"harness.delegate_agent"}, {"terminal"}, {"missing"}, {"invalid-\xff"}} {
		if _, err := normalizeToolProgressPolicy(&ToolProgressPolicy{ExplorationToolKeys: keys}, progressCatalog{}); err == nil {
			t.Fatalf("invalid policy accepted: %v", keys)
		}
	}
	if _, err := normalizeToolProgressPolicy(&ToolProgressPolicy{ExplorationToolKeys: []string{"lookup"}}, mismatchedProgressCatalog{}); err == nil {
		t.Fatal("catalog resolved a different Tool identity")
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

type mismatchedProgressCatalog struct{ progressCatalog }

func (mismatchedProgressCatalog) Resolve(string) (tools.Definition, bool) {
	return tools.Definition{Key: "other", Name: "other", InputSchema: json.RawMessage(`{"type":"object"}`)}, true
}

func TestToolProgressCountsSettledCallsOnly(t *testing.T) {
	state := runState{ToolProgressPolicy: &ToolProgressPolicy{ExplorationToolKeys: []string{"lookup"}}}
	call := tools.Call{ID: "pending", ToolKey: "lookup", Arguments: json.RawMessage(`{}`)}
	state.Messages = []model.Message{{Role: model.RoleAssistant, ToolCalls: []tools.Call{call, call}}}
	if repeatedExplorationCall(state, call) {
		t.Fatal("pending assistant calls fabricated settled evidence")
	}
}

func TestToolProgressArgumentIdentityPreservesLargeNumbersAndArrayOrder(t *testing.T) {
	left := json.RawMessage(`{"id":9007199254740992,"pages":[1,2]}`)
	reordered := json.RawMessage(`{ "pages": [1,2], "id": 9007199254740992 }`)
	if progressArguments(left) != progressArguments(reordered) {
		t.Fatal("object ordering changed argument identity")
	}
	for _, other := range []json.RawMessage{
		json.RawMessage(`{"id":9007199254740993,"pages":[1,2]}`),
		json.RawMessage(`{"id":9007199254740992,"pages":[2,1]}`),
	} {
		if progressArguments(left) == progressArguments(other) {
			t.Fatal("different read arguments collapsed to the same identity")
		}
	}
}
