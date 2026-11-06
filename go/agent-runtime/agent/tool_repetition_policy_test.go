package agent

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// This deliberately mirrors the previous nested-scan policy, independently
// of the optimized implementation. It is the behavior-preservation oracle.
func referenceRepeatedUnchangedToolKeys(messages []model.Message) []string {
	batches := completedToolBatches(messages)
	keys := make([]string, 0)
	seen := make(map[string]struct{})
	for index, batch := range batches {
		for _, current := range batch {
			if _, exists := seen[current.call.ToolKey]; exists || recoverableToolResult(current.result) {
				continue
			}
			found := false
			for _, older := range batches[index+1:] {
				for _, candidate := range older {
					if candidate.result == current.result &&
						candidate.call.ToolKey == current.call.ToolKey &&
						strings.TrimSpace(string(candidate.call.Arguments)) == strings.TrimSpace(string(current.call.Arguments)) {
						found = true
						break
					}
				}
				if found {
					break
				}
			}
			if found {
				seen[current.call.ToolKey] = struct{}{}
				keys = append(keys, current.call.ToolKey)
			}
		}
	}
	return keys
}

func makeToolPolicyTranscript(seed int64, batches int, withInterruptions bool) []model.Message {
	random := rand.New(rand.NewSource(seed))
	messages := make([]model.Message, 0, 3*batches)
	for index := 0; index < batches; index++ {
		count := 1 + random.Intn(4)
		calls := make([]tools.Call, 0, count)
		results := make([]model.Message, 0, count)
		for j := 0; j < count; j++ {
			callID := fmt.Sprintf("call-%d-%d", index, j)
			key := fmt.Sprintf("tool-%d", random.Intn(6))
			arg := fmt.Sprintf(`{"value":%d}`, random.Intn(4))
			if random.Intn(4) == 0 {
				arg = " " + arg + " "
			}
			calls = append(calls, tools.Call{
				ID: callID, ToolKey: key, Arguments: json.RawMessage(arg),
			})
			result := fmt.Sprintf(`{"ok":true,"value":%d}`, random.Intn(6))
			switch random.Intn(8) {
			case 0:
				result = `{"ok":false,"retryable":true}`
			case 1:
				result = `{"ok":false,"retryable":false}`
			case 2:
				result = "not-json"
			}
			results = append(results, model.Message{Role: model.RoleTool, ToolCallID: callID, Content: model.TextParts(result)})
		}
		messages = append(messages, model.Message{Role: model.RoleAssistant, ToolCalls: calls})
		messages = append(messages, results...)
		if withInterruptions && random.Intn(20) == 0 {
			messages = append(messages, model.Message{Role: model.RoleUser, Content: model.TextParts("new user turn")})
		}
	}
	return messages
}

func TestRepeatedToolPolicyMatchesIndependentNestedOracle(t *testing.T) {
	for seed := int64(1); seed <= 160; seed++ {
		messages := makeToolPolicyTranscript(seed, 1+int(seed%90), true)
		got := repeatedUnchangedToolKeys(messages)
		want := referenceRepeatedUnchangedToolKeys(messages)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("seed=%d got=%q want=%q", seed, got, want)
		}
	}
	// Equal signatures in the *same* batch cannot act as older evidence.
	onlyOne := makeToolPolicyTranscript(17, 1, false)
	if got := repeatedUnchangedToolKeys(onlyOne); len(got) != 0 {
		t.Fatalf("same-batch history fabricated repeat: %q", got)
	}
}

func uniqueToolPolicyTranscript(n int) []model.Message {
	messages := make([]model.Message, 0, 2*n)
	for i := range n {
		id := fmt.Sprintf("call-%d", i)
		messages = append(messages,
			model.Message{Role: model.RoleAssistant, ToolCalls: []tools.Call{
				{ID: id, ToolKey: "lookup", Arguments: json.RawMessage(fmt.Sprintf(`{"id":%d}`, i))},
			}},
			model.Message{Role: model.RoleTool, ToolCallID: id,
				Content: model.TextParts(fmt.Sprintf(`{"ok":true,"unique":%d}`, i))},
		)
	}
	return messages
}

func BenchmarkRepeatedToolPolicyUniqueTranscript(b *testing.B) {
	for _, n := range []int{32, 256, 1024} {
		b.Run(fmt.Sprintf("batches-%d", n), func(b *testing.B) {
			messages := uniqueToolPolicyTranscript(n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = repeatedUnchangedToolKeys(messages)
			}
		})
	}
}

func BenchmarkRepeatedToolPolicyLongTranscript(b *testing.B) {
	for _, n := range []int{32, 256, 1024} {
		b.Run(fmt.Sprintf("batches-%d", n), func(b *testing.B) {
			messages := makeToolPolicyTranscript(31, n, false)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = repeatedUnchangedToolKeys(messages)
			}
		})
	}
}
