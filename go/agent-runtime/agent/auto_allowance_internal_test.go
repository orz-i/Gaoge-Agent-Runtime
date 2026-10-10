package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestAutomaticAllowanceHintKeepsSoftBoundaryDistinctFromHardLimits(t *testing.T) {
	state := runState{
		CallAllowance: &CallAllowance{LLMCalls: 16},
		AutoAllowance: &autoAllowanceState{Policy: AutoAllowancePolicy{ModelStep: 8}},
		Budget:        budget.Snapshot{Usage: budget.Usage{LLMCalls: 15}},
	}
	hint := automaticAllowanceHint(state)
	for _, guidance := range []string{
		"renewable soft allowance", "1 model call(s)",
		"not the task's hard call limit or the model's context limit",
		"Continue unfinished work", "Runtime actually pauses or denies execution",
	} {
		if !strings.Contains(hint, guidance) {
			t.Fatalf("missing boundary guidance %q in %q", guidance, hint)
		}
	}
	state.Budget.Usage.LLMCalls = 16
	if hint := automaticAllowanceHint(state); !strings.Contains(hint, "0 model call(s)") {
		t.Fatalf("last call should still describe a renewable segment: %q", hint)
	}
	state.Budget.Usage.LLMCalls = 14
	if hint := automaticAllowanceHint(state); hint != "" {
		t.Fatalf("early segment hint = %q", hint)
	}
	state.Budget.Usage.LLMCalls = 15
	state.AutoAllowance = nil
	if hint := automaticAllowanceHint(state); hint != "" {
		t.Fatalf("manual-only runs must not imply automatic renewal: %q", hint)
	}
}

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
