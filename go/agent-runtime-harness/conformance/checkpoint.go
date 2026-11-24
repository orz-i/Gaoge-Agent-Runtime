package conformance

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// CheckpointOpaqueEvidence checks a real Store, not a replacement serializer.
// These bytes belong to Provider replay and original Tool intents, whose
// comparison is stricter than the Checkpoint's semantic window hash.
func CheckpointOpaqueEvidence(t *testing.T, store harness.Store, reopen func() harness.Store) {
	t.Helper()
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	cases := []struct {
		name string
		raw  string
	}{
		{"html-escaping", `{"markup":"<>&","z":1,"a":2}`},
		{"whitespace", `{ "z": 1, "a": 2 }`},
		{"key-order", `{"z":1,"a":{"b":2,"a":1}}`},
		{"large-integer", `{"n":9007199254740993}`},
		{"escaped-string", `{"s":"\\u003c\\u003e","n":1.00}`},
	}
	originals := make([]runtimecontext.Checkpoint, 0, len(cases))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scope := "opaque-evidence-" + tc.name
			checkpoint, err := manager.Open(t.Context(), runtimecontext.OpenRequest{
				ScopeID: scope, StaticFingerprint: runtimecontext.StaticFingerprint("opaque-source"),
				SourcePath:   []string{"user", "assistant", "tool", "tool2", "replay"},
				Instructions: "frozen", Entries: []runtimecontext.Entry{
					{ID: "user", SourceID: "user", Message: model.Message{Role: model.RoleUser, Content: model.TextParts("question")}},
					{ID: "assistant", SourceID: "assistant", Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tools.Call{
						{ID: "call-1", ToolKey: "lookup", Arguments: json.RawMessage(tc.raw)},
						{ID: "call-2", ToolKey: "lookup", Arguments: json.RawMessage(`{ "second": [2, 1] }`)},
					}}},
					{ID: "tool", SourceID: "tool", Message: model.Message{Role: model.RoleTool, ToolCallID: "call-1", Content: model.TextParts("ok")}},
					{ID: "tool2", SourceID: "tool2", Message: model.Message{Role: model.RoleTool, ToolCallID: "call-2", Content: model.TextParts("ok")}},
					{ID: "replay", SourceID: "replay", Message: model.Message{Role: model.RoleAssistant, ProviderReplay: &model.ProviderReplay{
						Provider: "openai", Format: "responses.output.v1", Data: json.RawMessage(tc.raw),
					}}},
				},
			})
			if err != nil || !runtimecontext.ValidCheckpoint(checkpoint) {
				t.Fatalf("open original valid checkpoint: %v", err)
			}
			if got, fresh, err := store.PutContextCheckpoint(t.Context(), checkpoint); err != nil || !fresh || !reflect.DeepEqual(got, checkpoint) {
				t.Fatalf("first write changed original: fresh=%t err=%v", fresh, err)
			}
			originals = append(originals, checkpoint)
		})
	}
	if reopen != nil {
		store = reopen()
	}
	// The public ContentHash is semantic JSON identity. A duplicate physical
	// Checkpoint ID with different original Provider/Tool bytes must NOT be
	// silently treated as the same committed source and replace its evidence.
	changed := runtimecontext.CloneCheckpoint(originals[0])
	changed.Window.Entries[1].Message.ToolCalls[0].Arguments = json.RawMessage(`{"markup":"\u003c\u003e\u0026","z":1,"a":2}`)
	if !runtimecontext.ValidCheckpoint(changed) || changed.ID != originals[0].ID {
		t.Fatal("test fixture did not preserve semantic Checkpoint identity")
	}
	if _, fresh, err := store.PutContextCheckpoint(t.Context(), changed); fresh || !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("same-hash but foreign original Tool bytes were accepted: fresh=%t err=%v", fresh, err)
	}
	for _, original := range originals {
		got, err := store.GetContextCheckpoint(t.Context(), original.ID)
		if err != nil || !runtimecontext.ValidCheckpoint(got) || !reflect.DeepEqual(got, original) {
			t.Fatalf("Store changed original opaque Checkpoint on reopen: id=%s err=%v original=%+v got=%+v", original.ID, err, original, got)
		}
		if _, fresh, err := store.PutContextCheckpoint(t.Context(), original); err != nil || fresh {
			t.Fatalf("replaying same physical Checkpoint is not idempotent: id=%s fresh=%t err=%v", original.ID, fresh, err)
		}
		// This check also rejects stores that return an equivalent semantic
		// window with silently reordered or escaped original Provider bytes.
		if again, err := store.GetContextCheckpoint(t.Context(), original.ID); err != nil || !reflect.DeepEqual(again, original) {
			t.Fatalf("replay changed the original immutable Checkpoint: %v", err)
		}
	}
}
