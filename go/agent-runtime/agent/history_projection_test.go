package agent

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func referencePublicInvocations(values []ModelInvocation) []ModelInvocation {
	result := cloneModelInvocations(values)
	for i := range result {
		result[i].Response.ProviderReplay = nil
		result[i].Request.Messages = model.CloneMessages(result[i].Request.Messages)
		for j := range result[i].Request.Messages {
			result[i].Request.Messages[j].ProviderReplay = nil
		}
	}
	return result
}

func projectionHistory(count int) []ModelInvocation {
	values := make([]ModelInvocation, count)
	replay := &model.ProviderReplay{Provider: "fixture", Format: "opaque", Data: json.RawMessage(`"` + strings.Repeat("x", 65536) + `"`)}
	for i := range values {
		values[i] = ModelInvocation{ID: fmt.Sprint(i),
			Status:   ModelInvocationConsumed,
			Response: model.Response{Content: "answer", ProviderReplay: replay, Usage: &model.Usage{InputTokens: 1}},
		}
		if i == count-1 {
			// Only the active invocation retains its request. Consumed requests
			// are already released by the existing Runtime.
			values[i].Status = ModelInvocationCompleted
			values[i].Request = model.Request{Messages: []model.Message{{Role: model.RoleAssistant, Content: model.TextParts("visible"), ProviderReplay: replay,
				ToolCalls: []tools.Call{{ID: "read", ToolKey: "lookup", Arguments: json.RawMessage(`{"id":42}`)}}}}}
		}
	}
	return values
}

func TestPublicHistoryProjectionIsEquivalentAndIsolated(t *testing.T) {
	values := projectionHistory(8)
	want := referencePublicInvocations(values)
	got := publicModelInvocations(values)
	if !reflect.DeepEqual(got, want) {
		t.Fatal("public projection behavior changed")
	}
	last := len(got) - 1
	got[last].Request.Messages[0].ToolCalls[0].Arguments[0] = '['
	got[0].Response.Usage.InputTokens = 99
	if values[last].Request.Messages[0].ToolCalls[0].Arguments[0] != '{' || values[0].Response.Usage.InputTokens != 1 ||
		values[last].Request.Messages[0].ProviderReplay == nil || values[0].Response.ProviderReplay == nil {
		t.Fatal("projection mutated the authoritative receipt or transcript")
	}
}

func TestConsumedInvocationReleasesRequestAndPreservesReceiptIsolation(t *testing.T) {
	now := time.Now().UTC()
	invocation := projectionHistory(1)[0]
	invocation.Status = ModelInvocationCompleted
	invocation.CompletedAt = &now
	state := runState{ModelInvocations: []ModelInvocation{invocation}}
	consumed, ok := consumeLastModelInvocation(&state, now)
	if !ok || len(state.ModelInvocations[0].Request.Messages) != 0 || len(consumed.Request.Messages) != 1 {
		t.Fatal("consumed request was retained or needed input was lost")
	}
	consumed.Response.ProviderReplay.Data[1] = 'y'
	consumed.Response.Usage.InputTokens = 99
	if state.ModelInvocations[0].Response.ProviderReplay.Data[1] != 'x' || state.ModelInvocations[0].Response.Usage.InputTokens != 1 {
		t.Fatal("returned invocation aliases the original stored receipt")
	}
}

func BenchmarkPublicHistoryProjection(b *testing.B) {
	for _, count := range []int{8, 64, 256} {
		values := projectionHistory(count)
		for _, implementation := range []struct {
			name string
			fn   func([]ModelInvocation) []ModelInvocation
		}{{"legacy", referencePublicInvocations}, {"filtered", publicModelInvocations}} {
			b.Run(fmt.Sprintf("steps_%d/%s", count, implementation.name), func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					if len(implementation.fn(values)) != count {
						b.Fatal("incomplete projection")
					}
				}
			})
		}
	}
}
