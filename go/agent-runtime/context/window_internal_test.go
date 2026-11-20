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

func TestLosslessArtifactEncodingIsReservedForLegacyIncompatibleJSON(t *testing.T) {
	value := Artifact{
		Kind: ArtifactCompaction, ScopeID: "scope", Generation: 1,
		ContentJSON: json.RawMessage(`{"n":1}`), Encoding: ArtifactEncodingLosslessJSON,
	}
	if err := normalizeArtifactJSON(&value, false); err == nil {
		t.Fatal("accepted a second encoding identity for legacy-compatible JSON")
	}
}
