package harnesspostgres

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestCheckpointStorageRejectsContradictoryOpaqueReceipts(t *testing.T) {
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	original, err := manager.Open(t.Context(), runtimecontext.OpenRequest{
		ScopeID: "original-context", StaticFingerprint: runtimecontext.StaticFingerprint("trusted"),
		Instructions: "original", SourcePath: []string{"user", "assistant", "tool", "replay"},
		Entries: []runtimecontext.Entry{
			{ID: "user", SourceID: "user", Message: model.Message{Role: model.RoleUser, Content: model.TextParts("query")}},
			{ID: "assistant", SourceID: "assistant", Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tools.Call{
				{ID: "call", ToolKey: "search", Arguments: json.RawMessage(`{ "html": "<>&" }`)},
			}}},
			{ID: "tool", SourceID: "tool", Message: model.Message{Role: model.RoleTool, ToolCallID: "call", Content: model.TextParts("result")}},
			{ID: "replay", SourceID: "replay", Message: model.Message{Role: model.RoleAssistant, ProviderReplay: &model.ProviderReplay{
				Provider: "openai", Format: "responses.output.v1", Data: json.RawMessage(`{ "reasoning": "<sealed>" }`),
			}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := marshalCheckpointStorage(original)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := unmarshalCheckpointStorage(encoded)
	if err != nil || !reflect.DeepEqual(recovered, original) {
		t.Fatalf("original receipts not preserved: %v", err)
	}
	var base checkpointStorage
	if err = json.Unmarshal([]byte(encoded), &base); err != nil {
		t.Fatal(err)
	}
	checksum := func(v *checkpointStorage) {
		raw, err := json.Marshal(v.Opaque)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(raw)
		v.OpaqueHash = hex.EncodeToString(digest[:])
	}
	for _, tc := range []struct {
		name string
		edit func(*checkpointStorage)
	}{
		{"unknown-format", func(v *checkpointStorage) { v.Format = "unknown" }},
		{"original-tool-byte-tampered", func(v *checkpointStorage) { v.Opaque[0].ToolArguments[0] = []byte(`{"html":"changed"}`) }},
		{"original-replay-byte-tampered", func(v *checkpointStorage) { v.Opaque[1].ProviderData = []byte(`{"reasoning":"forged"}`) }},
		{"checksum-tampered", func(v *checkpointStorage) { v.OpaqueHash = "untrusted" }},
		{"missing-original-evidence", func(v *checkpointStorage) { v.Opaque = v.Opaque[:1]; checksum(v) }},
		{"extra-evidence", func(v *checkpointStorage) {
			v.Opaque = append(v.Opaque, checkpointOpaqueEvidence{Entry: 2})
			checksum(v)
		}},
		{"foreign-entry-owner", func(v *checkpointStorage) { v.Opaque[0].Entry = 0; checksum(v) }},
		{"incomplete-tool-batch", func(v *checkpointStorage) { v.Opaque[0].ToolArguments = nil; checksum(v) }},
		{"invalid-tool-json", func(v *checkpointStorage) { v.Opaque[0].ToolArguments[0] = []byte(`{broken`); checksum(v) }},
		{"invalid-replay-json", func(v *checkpointStorage) { v.Opaque[1].ProviderData = []byte(`{broken`); checksum(v) }},
		{"placeholder-replaced", func(v *checkpointStorage) {
			v.Checkpoint.Window.Entries[1].Message.ToolCalls[0].Arguments = json.RawMessage(`{"html":"not-a-placeholder"}`)
		}},
		{"forged-checkpoint-identity", func(v *checkpointStorage) { v.Checkpoint.ID = "ctxc_forger" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var candidate checkpointStorage
			if err := json.Unmarshal([]byte(encoded), &candidate); err != nil {
				t.Fatal(err)
			}
			tc.edit(&candidate)
			raw, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = unmarshalCheckpointStorage(string(raw)); !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("contradictory original evidence admitted: %v", err)
			}
		})
	}
}

func TestCheckpointStorageWithoutOpaqueEvidenceRetainsOrdinaryContext(t *testing.T) {
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	original, err := manager.Open(t.Context(), runtimecontext.OpenRequest{
		ScopeID: "text-only", StaticFingerprint: runtimecontext.StaticFingerprint("original"),
		SourcePath: []string{"user"}, Instructions: "trusted",
		Entries: []runtimecontext.Entry{{ID: "user", SourceID: "user", Message: model.Message{Role: model.RoleUser, Content: model.TextParts("text")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := marshalCheckpointStorage(original)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := unmarshalCheckpointStorage(encoded)
	if err != nil || !reflect.DeepEqual(recovered, original) {
		t.Fatalf("text-only checkpoint roundtrip drift: %v", err)
	}
}
