package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Physical Tool/Provider bytes have to survive *every* typed State field,
// including model-intent receipts and recovered Tools, not just Messages.
func TestAgentStateOneSealedOpaquePathForAllOriginalEvidence(t *testing.T) {
	const raw = `{ "html": "<>&", "n": 9007199254740993, "escape": "\\u003c" }`
	call := tools.Call{ID: "tool", ToolKey: "lookup", Name: "lookup", Arguments: json.RawMessage(raw)}
	message := model.Message{Role: model.RoleAssistant, ToolCalls: []tools.Call{call},
		ProviderReplay: &model.ProviderReplay{Provider: "openai", Format: "responses.output.v1", Data: json.RawMessage(`{ "cipher": "<>&" }`)}}
	original := runState{
		Messages:       []model.Message{message},
		ModelOptions:   json.RawMessage(`{ "name": "<>&" }`),
		PendingCalls:   []tools.Call{call},
		RecoveredTools: []rootToolReceipt{{Call: call, Result: tools.ExecutionResult{Content: json.RawMessage(`{ "original": "<>&" }`)}}},
		ModelInvocations: []ModelInvocation{{
			Request: model.Request{
				ModelOptions: json.RawMessage(raw),
				Messages:     []model.Message{message},
				Tools:        []tools.Definition{{Key: "lookup", Name: "lookup", InputSchema: json.RawMessage(`{ "type": "object" }`)}},
				HostedTools:  []model.HostedTool{{Key: "native", Target: json.RawMessage(`{ "target": "<>&" }`)}},
			},
			Response: model.Response{
				ToolCalls:       []tools.Call{call},
				ProviderReplay:  model.CloneProviderReplay(message.ProviderReplay),
				HostedToolCalls: []model.HostedToolCall{{ID: "native-1", ToolKey: "native", Input: json.RawMessage(raw), Output: json.RawMessage(raw)}},
			},
		}},
	}
	encoded, err := encodeOriginalRunState(original)
	if err != nil {
		t.Fatal(err)
	}
	after, err := decodeOriginalRunState(encoded)
	if err != nil || !reflect.DeepEqual(after, original) {
		t.Fatalf("original Agent fields were not byte-preserving: err=%v\nwant=%+v\ngot=%+v", err, original, after)
	}
	if string(original.Messages[0].ToolCalls[0].Arguments) != raw {
		t.Fatal("encoding mutated original caller-owned Tool bytes")
	}
	again, err := encodeOriginalRunState(after)
	if err != nil || !bytes.Equal(again, encoded) {
		t.Fatalf("opaque Agent State re-encoding was not stable after process reload: %v", err)
	}
	var stored persistedRunState
	if err := json.Unmarshal(encoded, &stored); err != nil || stored.OriginalStateEvidence == nil ||
		len(stored.OriginalStateEvidence.Fields) < 8 {
		t.Fatalf("nested originals did not cross one Agent State encoding boundary: %v %+v", err, stored.OriginalStateEvidence)
	}
}

func TestAgentStateMissingOrContradictoryOriginalEvidenceFailsClosed(t *testing.T) {
	original := runState{Messages: []model.Message{{Role: model.RoleAssistant,
		ToolCalls:      []tools.Call{{ID: "call", ToolKey: "lookup", Arguments: json.RawMessage(`{"html":"<>&"}`)}},
		ProviderReplay: &model.ProviderReplay{Provider: "openai", Format: "responses.output.v1", Data: json.RawMessage(`{"signed":"<>&"}`)},
	}}}
	encoded, err := encodeOriginalRunState(original)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeOriginalRunState(json.RawMessage(`{"messages":[]}`)); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unsealed pre-cut Agent state gained model authorization: %v", err)
	}
	for _, tc := range []struct {
		name string
		edit func(*persistedRunState)
	}{
		{"missing-original", func(v *persistedRunState) { v.OriginalStateEvidence = nil }},
		{"changed-original", func(v *persistedRunState) { v.OriginalStateEvidence.Fields[0].Raw = []byte(`{"html":"forged"}`) }},
		{"stripped-field", func(v *persistedRunState) { v.OriginalStateEvidence.Fields = v.OriginalStateEvidence.Fields[:1] }},
		{"duplicate-field", func(v *persistedRunState) {
			v.OriginalStateEvidence.Fields = append(v.OriginalStateEvidence.Fields, v.OriginalStateEvidence.Fields[0])
		}},
		{"wrong-path", func(v *persistedRunState) { v.OriginalStateEvidence.Fields[0].Path = ".Foreign" }},
		{"different-typed-state", func(v *persistedRunState) { v.Messages[0].ToolCalls[0].ID = "forged-call" }},
		{"missing-checksum", func(v *persistedRunState) { v.OriginalStateEvidence.Digest = "" }},
		{"missing-physical-message", func(v *persistedRunState) { v.Messages = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mutated persistedRunState
			if err := json.Unmarshal(encoded, &mutated); err != nil {
				t.Fatal(err)
			}
			tc.edit(&mutated)
			value, err := json.Marshal(mutated)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeOriginalRunState(value); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("foreign physical State proof was accepted: %v", err)
			}
		})
	}
	for _, tc := range []struct {
		name string
		edit func(*persistedRunState)
	}{
		{"forged-current-original", func(v *persistedRunState) { v.OriginalStateEvidence.Fields[0].Raw = []byte(`{"html":"forged"}`) }},
		{"forged-owner-path", func(v *persistedRunState) {
			v.OriginalStateEvidence.Fields[0].Path = ".Messages[12].ToolCalls[0].Arguments"
		}},
	} {
		t.Run(tc.name+"-checksum-recomputed", func(t *testing.T) {
			var mutated persistedRunState
			if err := json.Unmarshal(encoded, &mutated); err != nil {
				t.Fatal(err)
			}
			tc.edit(&mutated)
			canonical, err := json.Marshal(mutated.runState)
			if err != nil {
				t.Fatal(err)
			}
			seal, err := sealOriginalState(canonical, mutated.OriginalStateEvidence.Fields)
			if err != nil {
				t.Fatal(err)
			}
			mutated.OriginalStateEvidence = &seal
			value, err := json.Marshal(mutated)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeOriginalRunState(value); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("recomputed checksum promoted foreign Agent evidence: %v", err)
			}
		})
	}
}

func TestAgentStateWithoutOpaqueCallsStillRequiresOriginalSeal(t *testing.T) {
	original := runState{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("text")}}}
	encoded, err := encodeOriginalRunState(original)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeOriginalRunState(encoded)
	if err != nil || !reflect.DeepEqual(decoded, original) {
		t.Fatalf("ordinary text Run State lost identity: %v", err)
	}
}

func FuzzAgentOriginalEvidenceStateRoundTrip(f *testing.F) {
	for _, original := range []string{
		`{"html":"<>&","n":1}`,
		`{ "a": 1, "z": [2, 1] }`,
		`{"number":9007199254740993}`,
		`{"number":1e1000}`,
		`{"text":"\\u003c&🌱"}`,
		`[]`, `null`, `42`, `true`,
	} {
		f.Add(original)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) == 0 || len(raw) > 4096 || !json.Valid([]byte(raw)) {
			return
		}
		state := runState{
			Messages: []model.Message{{Role: model.RoleAssistant, ToolCalls: []tools.Call{{ID: "call", ToolKey: "lookup", Arguments: json.RawMessage(raw)}},
				ProviderReplay: &model.ProviderReplay{Provider: "openai", Format: "replay", Data: json.RawMessage(raw)}}},
			PendingCalls: []tools.Call{{ID: "call", ToolKey: "lookup", Arguments: json.RawMessage(raw)}},
		}
		encoded, err := encodeOriginalRunState(state)
		if err != nil {
			t.Fatalf("valid original JSON rejected by Agent state encoder: %v", err)
		}
		decoded, err := decodeOriginalRunState(encoded)
		if err != nil || !reflect.DeepEqual(decoded, state) {
			t.Fatalf("original physical bytes changed on Run State replay: err=%v input=%q", err, raw)
		}
	})
}
