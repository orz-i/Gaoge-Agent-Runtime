package agent

import (
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestAutoAllowanceFingerprintIgnoresTransientToolIDsAndJSONKeyOrder(t *testing.T) {
	unchanged := func(callID string, arguments string, output string) []model.Message {
		return []model.Message{
			{
				Role:      model.RoleAssistant,
				ToolCalls: []tools.Call{{ID: callID, ToolKey: "read.manifest", Arguments: json.RawMessage(arguments)}},
			},
			{Role: model.RoleTool, Content: output, ToolCallID: callID},
		}
	}
	first := lastSettledWorkFingerprint(unchanged("call-a", `{"one":1,"two":2}`, `{"status":"unchanged"}`))
	replayed := lastSettledWorkFingerprint(unchanged("call-z", `{"two":2,"one":1}`, `{"status":"unchanged"}`))
	if first == "" || first != replayed {
		t.Fatalf("same settled work, different call IDs or key order must fingerprint equally: %q vs %q", first, replayed)
	}
	fresh := lastSettledWorkFingerprint(unchanged("call-z", `{"one":1,"two":2}`, `{"status":"changed"}`))
	if fresh == first {
		t.Fatal("meaningfully changed output must not trigger no-progress")
	}
	otherArgs := lastSettledWorkFingerprint(unchanged("call-z", `{"one":2,"two":2}`, `{"status":"unchanged"}`))
	if otherArgs == first {
		t.Fatal("changed authorized Tool arguments must be distinct work")
	}
}

func TestAutoAllowanceFingerprintKeepsMeaningfulJSONStringWhitespace(t *testing.T) {
	messages := func(output string) []model.Message {
		return []model.Message{
			{Role: model.RoleAssistant, ToolCalls: []tools.Call{{ID: "call-1", ToolKey: "read.text", Arguments: json.RawMessage(`{}`)}}},
			{Role: model.RoleTool, Content: output, ToolCallID: "call-1"},
		}
	}
	left := lastSettledWorkFingerprint(messages(`{"content":"word  space"}`))
	right := lastSettledWorkFingerprint(messages(`{"content":"word space"}`))
	if left == right {
		t.Fatal("no-progress must not collapse whitespace inside JSON string values")
	}
}

func TestAutoAllowanceFingerprintPreservesLargeJSONIntegerIdentity(t *testing.T) {
	build := func(arguments string) []model.Message {
		return []model.Message{
			{Role: model.RoleAssistant, ToolCalls: []tools.Call{{ID: "call-1", ToolKey: "read.id", Arguments: json.RawMessage(arguments)}}},
			{Role: model.RoleTool, Content: `{"status":"same"}`, ToolCallID: "call-1"},
		}
	}
	first := lastSettledWorkFingerprint(build(`{"id":9007199254740992}`))
	second := lastSettledWorkFingerprint(build(`{"id":9007199254740993}`))
	if first == second {
		t.Fatal("different precise Tool arguments must not be rounded into the same stalled-work fingerprint")
	}
}

func TestAutoAllowanceFingerprintPreservesLeadingAndTrailingToolOutput(t *testing.T) {
	build := func(output string) []model.Message {
		return []model.Message{
			{Role: model.RoleAssistant, ToolCalls: []tools.Call{{ID: "call-1", ToolKey: "read.text", Arguments: json.RawMessage(`{}`)}}},
			{Role: model.RoleTool, Content: output, ToolCallID: "call-1"},
		}
	}
	if lastSettledWorkFingerprint(build("  significant indentation")) ==
		lastSettledWorkFingerprint(build("significant indentation")) {
		t.Fatal("changing indentation must be considered possible progress")
	}
}
