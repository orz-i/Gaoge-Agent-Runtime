package agent

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// The retained deep copies belong to the live Summary source/receipt boundary.
// Changing a Host port's owned copy must not rewrite an original signed model
// request, Source Window, derivation, or native execution observation.
func TestContextSummaryCopiesDoNotAliasOriginalEvidence(t *testing.T) {
	request := model.Request{ModelOptions: json.RawMessage(`{"n":9007199254740993}`),
		Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("frozen"),
			ProviderReplay: &model.ProviderReplay{Data: json.RawMessage(`{"reasoning":"signed"}`)}}},
		Tools: []tools.Definition{{Key: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}
	requestCopy := cloneSummaryRequest(request)
	if !reflect.DeepEqual(request, requestCopy) {
		t.Fatal("Summary request copy changed a value, including nil/empty collections")
	}
	requestCopy.ModelOptions[0] = '!'
	requestCopy.Messages[0].ProviderReplay.Data[0] = '!'
	requestCopy.Tools[0].InputSchema[0] = '!'
	if !reflect.DeepEqual(request.ModelOptions, json.RawMessage(`{"n":9007199254740993}`)) ||
		!reflect.DeepEqual(request.Messages[0].ProviderReplay.Data, json.RawMessage(`{"reasoning":"signed"}`)) ||
		!reflect.DeepEqual(request.Tools[0].InputSchema, json.RawMessage(`{"type":"object"}`)) {
		t.Fatal("Summary request copy aliased original provider proof or Tool schema")
	}

	checkpoint := runtimecontext.Checkpoint{ArtifactIDs: []string{"artifact"},
		Window: runtimecontext.Window{
			WorkingState: &runtimecontext.WorkingState{EvidenceRefs: []string{"original"},
				Derivation: &runtimecontext.WorkingStateDerivation{RunID: "original-run"}},
			Entries: []runtimecontext.Entry{{Message: model.Message{Role: model.RoleUser,
				ProviderReplay: &model.ProviderReplay{Data: json.RawMessage(`{"signed":true}`)}}}},
		},
	}
	cursor := SummaryCursor{Checkpoint: checkpoint}
	cursorCopy := cloneSummaryCursor(cursor)
	if !reflect.DeepEqual(cursor, cursorCopy) {
		t.Fatal("Summary cursor copy changed original Context witness")
	}
	cursorCopy.Checkpoint.Window.WorkingState.EvidenceRefs[0] = "foreign"
	cursorCopy.Checkpoint.Window.WorkingState.Derivation.RunID = "foreign"
	cursorCopy.Checkpoint.Window.Entries[0].Message.ProviderReplay.Data[0] = '!'
	cursorCopy.Checkpoint.ArtifactIDs[0] = "foreign"
	if !reflect.DeepEqual(cursor.Checkpoint, checkpoint) {
		t.Fatal("Summary cursor clone aliased frozen checkpoint or derivation")
	}

	deadline := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	source := kernel.Snapshot{Run: kernel.Run{ID: "original-run", DeadlineAt: &deadline},
		State:      json.RawMessage(`{"pending":"original"}`),
		Checkpoint: &kernel.Checkpoint{Payload: json.RawMessage(`{"original":true}`)},
		Result:     &kernel.Result{Content: json.RawMessage(`{"original":true}`)},
	}
	copy := cloneSummarySnapshot(source)
	if !reflect.DeepEqual(source, copy) {
		t.Fatal("Summary source snapshot was not copied exactly")
	}
	copy.State[0], copy.Checkpoint.Payload[0], copy.Result.Content[0] = '!', '!', '!'
	*copy.Run.DeadlineAt = time.Time{}
	if source.Run.DeadlineAt.IsZero() || source.State[0] == '!' ||
		source.Checkpoint.Payload[0] == '!' || source.Result.Content[0] == '!' {
		t.Fatal("Summary Snapshot clone aliased physical execution evidence")
	}
}
