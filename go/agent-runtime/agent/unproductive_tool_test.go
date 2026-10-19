package agent

import (
	"encoding/json"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
	"slices"
	"testing"
)

func unsettledToolHistory(key string, outputs ...string) []model.Message {
	messages := []model.Message{{Role: model.RoleUser, Content: "Verify AI news published during the last 24 hours"}}
	for i, output := range outputs {
		callID := "call-" + string(rune('a'+i))
		args := json.RawMessage(`{"query":"AI news query ` + callID + `"}`)
		messages = append(messages, model.Message{Role: model.RoleAssistant, ToolCalls: []tools.Call{{ID: callID, ToolKey: key, Arguments: args}}}, model.Message{Role: model.RoleTool, ToolCallID: callID, Content: output})
	}
	return messages
}
func TestUnproductiveToolKeysStopEmptySearchAcrossDifferentQueries(t *testing.T) {
	t.Parallel()
	const empty = `{"toolKeys":[]}`
	if got := unproductiveToolKeys(unsettledToolHistory(ToolDiscoveryKey, empty)); len(got) != 0 {
		t.Fatalf("first empty search blocked: %v", got)
	}
	messages := unsettledToolHistory(ToolDiscoveryKey, empty, empty)
	if got := unproductiveToolKeys(messages); !slices.Equal(got, []string{ToolDiscoveryKey}) {
		t.Fatalf("repeated empty search not blocked: %v", got)
	}
	messages = append(messages, model.Message{Role: model.RoleAssistant, ToolCalls: []tools.Call{{ID: "call-c", ToolKey: "lookup", Arguments: json.RawMessage(`{}`)}}}, model.Message{Role: model.RoleTool, ToolCallID: "call-c", Content: `{"ok":true}`})
	if got := unproductiveToolKeys(messages); len(got) != 0 {
		t.Fatalf("valid tool success should end failed-search streak: %v", got)
	}
}
func TestUnproductiveToolKeysStopRecoverableFailuresWithoutBlanketBlocking(t *testing.T) {
	t.Parallel()
	const failure = `{"ok":false,"retryable":true,"error":{"code":"tool.upstream_unavailable"}}`
	for n := 1; n <= 3; n++ {
		messages := unsettledToolHistory("mcp.web_search", slices.Repeat([]string{failure}, n)...)
		blocked := unproductiveToolKeys(messages)
		if n < 3 && len(blocked) != 0 || n == 3 && !slices.Equal(blocked, []string{"mcp.web_search"}) {
			t.Fatalf("failure count %d blocked=%v", n, blocked)
		}
	}
	if got := unproductiveToolKeys(unsettledToolHistory("mcp.web_search", failure, `{"ok":true}`, failure)); len(got) != 0 {
		t.Fatalf("success broke streak: %v", got)
	}
	if got := unproductiveToolKeys(unsettledToolHistory(ToolDiscoveryKey, `{"toolKeys":["mcp.web_search"]}`, `{"toolKeys":[]}`)); len(got) != 0 {
		t.Fatalf("successful discovery broke streak: %v", got)
	}
}
