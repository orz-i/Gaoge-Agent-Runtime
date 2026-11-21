package context

import (
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestSameMessageComparesExactJSONNumbersWithoutFloatRounding(t *testing.T) {
	t.Parallel()
	message := func(arguments string) model.Message {
		return model.Message{Role: model.RoleAssistant, ToolCalls: []tools.Call{{
			ID: "call", ToolKey: "lookup", Arguments: json.RawMessage(arguments),
		}}}
	}
	for _, test := range []struct {
		name, left, right string
		want              bool
	}{
		{"equivalent decimal spelling", `{"n":1.00,"z":2e3}`, `{"z":2000,"n":1}`, true},
		{"adjacent large integers", `{"n":9007199254740992}`, `{"n":9007199254740993}`, false},
		{"close decimals", `{"n":0.1}`, `{"n":0.10000000000000001}`, false},
		{"negative zero", `{"n":-0}`, `{"n":0.00}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := sameMessage(message(test.left), message(test.right)); got != test.want {
				t.Fatalf("sameMessage(%s, %s)=%v, want %v", test.left, test.right, got, test.want)
			}
		})
	}
}

func TestCheckpointLosslessEncodingSeparatesLargeNumbersAndOpaqueReplay(t *testing.T) {
	manager := NewManager(Dependencies{})
	open := func(number string) Checkpoint {
		t.Helper()
		checkpoint, err := manager.Open(t.Context(), OpenRequest{
			ScopeID: "scope-lossless", StaticFingerprint: StaticFingerprint("lossless"),
			Instructions: "instructions", SourcePath: []string{"user", "assistant", "tool", "replay"},
			Entries: []Entry{
				{ID: "user", SourceID: "user", Message: model.Message{Role: model.RoleUser, Content: model.TextParts("question")}},
				{ID: "assistant", SourceID: "assistant", Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tools.Call{{
					ID: "call", ToolKey: "lookup", Arguments: json.RawMessage(`{"n":` + number + `}`),
				}}}},
				{ID: "tool", SourceID: "tool", Message: model.Message{Role: model.RoleTool, ToolCallID: "call", Content: model.TextParts("result")}},
				{ID: "replay", SourceID: "replay", Message: model.Message{Role: model.RoleAssistant, ProviderReplay: &model.ProviderReplay{
					Provider: "provider", Format: "opaque", Data: json.RawMessage(`{"n":` + number + `}`),
				}}},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return checkpoint
	}

	boundary := open("9007199254740992")
	adjacent := open("9007199254740993")
	if boundary.ContentHashEncoding != CheckpointContentHashEncodingLosslessJSON ||
		adjacent.ContentHashEncoding != CheckpointContentHashEncodingLosslessJSON ||
		!ValidCheckpoint(boundary) || !ValidCheckpoint(adjacent) ||
		boundary.ContentHash == adjacent.ContentHash || boundary.ID == adjacent.ID {
		t.Fatalf("lossless checkpoints collapsed adjacent integers: boundary=%+v adjacent=%+v", boundary, adjacent)
	}
	encoded, err := json.Marshal(adjacent)
	if err != nil {
		t.Fatal(err)
	}
	var restored Checkpoint
	if err = json.Unmarshal(encoded, &restored); err != nil || !ValidCheckpoint(restored) ||
		restored.ContentHashEncoding != CheckpointContentHashEncodingLosslessJSON ||
		string(restored.Window.Entries[1].Message.ToolCalls[0].Arguments) != `{"n":9007199254740993}` ||
		string(restored.Window.Entries[3].Message.ProviderReplay.Data) != `{"n":9007199254740993}` {
		t.Fatalf("JSON persistence changed the lossless checkpoint/replay: restored=%+v err=%v", restored, err)
	}

	mutated := CloneCheckpoint(boundary)
	mutated.Window.Entries[1].Message.ToolCalls[0].Arguments = json.RawMessage(`{"n":9007199254740993}`)
	mutated.Window.Entries[3].Message.ProviderReplay.Data = json.RawMessage(`{"n":9007199254740993}`)
	if ValidCheckpoint(mutated) {
		t.Fatal("lossless checkpoint accepted a changed Tool/ProviderReplay number")
	}
}

func TestCheckpointRuntimeEntryIDsPreserveNumbers(t *testing.T) {
	manager := NewManager(Dependencies{})
	base, err := manager.Open(t.Context(), OpenRequest{
		ScopeID: "scope", StaticFingerprint: StaticFingerprint("numeric-entry"), SourcePath: []string{"user"},
		Entries: []Entry{{ID: "user", SourceID: "user", Message: model.Message{Role: model.RoleUser, Content: model.TextParts("question")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	capture := func(number string) Entry {
		t.Helper()
		messages := append(Materialize(base.Window), model.Message{Role: model.RoleAssistant, ToolCalls: []tools.Call{{
			ID: "call", ToolKey: "lookup", Arguments: json.RawMessage(`{"n":` + number + `}`),
		}}}, model.Message{Role: model.RoleTool, ToolCallID: "call", Content: model.TextParts("answer")})
		next, err := manager.Capture(t.Context(), CaptureRequest{Previous: base, StaticFingerprint: base.StaticFingerprint, RunID: "run", Messages: messages})
		if err != nil || !ValidCheckpoint(next) {
			t.Fatalf("capture failed for %s: %v", number, err)
		}
		return next.Window.Entries[len(base.Window.Entries)]
	}
	if capture("9007199254740992").ID == capture("9007199254740993").ID {
		t.Fatal("adjacent argument values shared a runtime entry ID")
	}
}

func TestCheckpointRejectsMissingOrUnknownHashEncoding(t *testing.T) {
	manager := NewManager(Dependencies{})
	request := OpenRequest{
		ScopeID: "scope-legacy", StaticFingerprint: StaticFingerprint("legacy"), Instructions: "instructions",
		SourcePath: []string{"user", "assistant", "tool"}, Entries: []Entry{
			{ID: "user", SourceID: "user", Message: model.Message{Role: model.RoleUser, Content: model.TextParts("question")}},
			{ID: "assistant", SourceID: "assistant", Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tools.Call{{
				ID: "call", ToolKey: "lookup", Arguments: json.RawMessage(`{"n":9007199254740992}`),
			}}}},
			{ID: "tool", SourceID: "tool", Message: model.Message{Role: model.RoleTool, ToolCallID: "call", Content: model.TextParts("result")}},
		},
	}
	checkpoint, err := manager.Open(t.Context(), request)
	if err != nil || !ValidCheckpoint(checkpoint) {
		t.Fatalf("failed to create a lossless Checkpoint: %v", err)
	}
	legacy := checkpoint
	legacy.ContentHashEncoding = ""
	legacy.ID = "ctxc_ff1a2bec25e4c89cafc46c460e98295e"
	legacy.ContentHash = "c28117f476b510b734b9e5948f6866925411934297826737698423fa726773e3"
	unknown := checkpoint
	unknown.ContentHashEncoding = "unknown"
	unknown.ID = checkpointID(unknown)
	for _, value := range []Checkpoint{legacy, unknown} {
		if ValidCheckpoint(value) {
			t.Fatalf("accepted unsupported Checkpoint encoding %q", value.ContentHashEncoding)
		}
		request.Previous = &value
		if _, err := manager.Open(t.Context(), request); err == nil {
			t.Fatalf("reused unsupported Checkpoint encoding %q", value.ContentHashEncoding)
		}
	}
}

func TestCheckpointLosslessEncodingRejectsAmbiguousRawJSON(t *testing.T) {
	manager := NewManager(Dependencies{})
	for _, test := range []struct {
		name, arguments, replay string
	}{
		{name: "Tool arguments", arguments: `{"n":1,"n":2}`, replay: `{}`},
		{name: "ProviderReplay", arguments: `{}`, replay: `{"n":1,"n":2}`},
		{name: "case-folded members", arguments: `{"n":1,"N":2}`, replay: `{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := manager.Open(t.Context(), OpenRequest{
				ScopeID: "scope-ambiguous-" + test.name, StaticFingerprint: StaticFingerprint("ambiguous"), Instructions: "instructions",
				SourcePath: []string{"assistant", "tool"}, Entries: []Entry{
					{ID: "assistant", SourceID: "assistant", Message: model.Message{Role: model.RoleAssistant,
						ToolCalls:      []tools.Call{{ID: "call", ToolKey: "lookup", Arguments: json.RawMessage(test.arguments)}},
						ProviderReplay: &model.ProviderReplay{Provider: "provider", Format: "opaque", Data: json.RawMessage(test.replay)},
					}},
					{ID: "tool", SourceID: "tool", Message: model.Message{Role: model.RoleTool, ToolCallID: "call", Content: model.TextParts("result")}},
				},
			})
			if err == nil {
				t.Fatal("lossless checkpoint accepted ambiguous JSON members")
			}
		})
	}
}

func TestLosslessArtifactEncodingIsReservedForLegacyIncompatibleJSON(t *testing.T) {
	value := Artifact{
		Kind: ArtifactCompaction, ScopeID: "scope", Generation: 1,
		ContentJSON: json.RawMessage(`{"n":1}`), Encoding: ArtifactEncodingLosslessJSON,
	}
	if err := normalizeArtifactJSON(&value, false); err == nil {
		t.Fatal("accepted a second encoding identity for legacy-compatible JSON")
	}
}
