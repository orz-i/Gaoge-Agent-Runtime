package context_test

import (
	stdcontext "context"
	"encoding/json"
	"errors"
	"testing"

	runtimectx "github.com/orz-i/Gaoge/sdk/go/agent-runtime/context"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/model"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

func TestManagerAppendsSourceDeltaWithoutRewritingStablePrefix(t *testing.T) {
	t.Parallel()
	manager := runtimectx.NewManager(runtimectx.Dependencies{})
	first, err := manager.Open(t.Context(), openRequest("m1", "m2"))
	if err != nil {
		t.Fatal(err)
	}
	secondRequest := openRequest("m1", "m2", "m3", "m4")
	secondRequest.Previous = &first
	second, err := manager.Open(t.Context(), secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	if first.Generation != second.Generation || second.Revision != first.Revision+1 || second.ParentCheckpointID != first.ID {
		t.Fatalf("append did not preserve generation: first=%s second=%s", runtimectx.DebugCheckpoint(first), runtimectx.DebugCheckpoint(second))
	}
	firstMessages := runtimectx.Materialize(first.Window)
	secondMessages := runtimectx.Materialize(second.Window)
	if !messagePrefix(firstMessages, secondMessages) {
		t.Fatalf("source append rewrote stable prefix:\nfirst=%#v\nsecond=%#v", firstMessages, secondMessages)
	}
}

func TestManagerResetsGenerationWhenBranchNoLongerContainsCheckpoint(t *testing.T) {
	t.Parallel()
	manager := runtimectx.NewManager(runtimectx.Dependencies{})
	first, err := manager.Open(t.Context(), openRequest("m1", "m2", "m3"))
	if err != nil {
		t.Fatal(err)
	}
	branch := openRequest("m1", "m2", "branch-user")
	branch.Previous = &first
	second, err := manager.Open(t.Context(), branch)
	if err != nil {
		t.Fatal(err)
	}
	if second.Generation != first.Generation+1 || second.Revision != 1 || second.ParentCheckpointID != "" || second.Trace.Reason != "lineage_reset" {
		t.Fatalf("branch must create an explicit generation reset: %#v", second)
	}
}

func TestCaptureRejectsStablePrefixMutationAndAcceptsRuntimeTail(t *testing.T) {
	t.Parallel()
	manager := runtimectx.NewManager(runtimectx.Dependencies{})
	checkpoint, err := manager.Open(t.Context(), openRequest("m1", "m2"))
	if err != nil {
		t.Fatal(err)
	}
	messages := runtimectx.Materialize(checkpoint.Window)
	messages = append(messages,
		model.Message{Role: model.RoleAssistant, ToolCalls: []tools.Call{{ID: "call-1", ToolKey: "lookup", Arguments: json.RawMessage(`{"q":"x"}`)}}},
		model.Message{Role: model.RoleTool, ToolCallID: "call-1", Content: `{"ok":true}`},
	)
	advanced, err := manager.Capture(t.Context(), runtimectx.CaptureRequest{
		Previous: checkpoint, StaticFingerprint: checkpoint.StaticFingerprint, RunID: "run-1", Messages: messages,
	})
	if err != nil {
		t.Fatal(err)
	}
	if advanced.Generation != checkpoint.Generation || advanced.Revision != checkpoint.Revision+1 || advanced.Trace.AppendedEntryCount != 2 {
		t.Fatalf("runtime tail was not captured: %#v", advanced)
	}
	mutated := model.CloneMessages(messages)
	mutated[1].Content = "rewritten"
	_, err = manager.Capture(t.Context(), runtimectx.CaptureRequest{
		Previous: checkpoint, StaticFingerprint: checkpoint.StaticFingerprint, RunID: "run-2", Messages: mutated,
	})
	if !errors.Is(err, runtimectx.ErrLineageConflict) {
		t.Fatalf("stable prefix mutation must be rejected, got %v", err)
	}
}

func TestRolloverCreatesNewGenerationWithDurableArtifactReference(t *testing.T) {
	t.Parallel()
	manager := runtimectx.NewManager(runtimectx.Dependencies{})
	checkpoint, err := manager.Open(t.Context(), openRequest("m1", "m2", "m3", "m4"))
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := runtimectx.NewArtifact(
		runtimectx.ArtifactCompaction, checkpoint.ScopeID, checkpoint.Generation+1, checkpoint.CoveredThroughSourceID,
		"goal=keep context; decisions=none", json.RawMessage(`{"strategy":"portable"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	window := runtimectx.Window{Instructions: checkpoint.Window.Instructions, Entries: []runtimectx.Entry{
		{ID: "compact", Required: true, Message: model.Message{Role: model.RoleSystem, Content: "<context_checkpoint>goal=keep context</context_checkpoint>"}},
		checkpoint.Window.Entries[len(checkpoint.Window.Entries)-1],
	}}
	next, err := manager.Rollover(t.Context(), runtimectx.RolloverRequest{
		Previous: checkpoint, Window: window, Artifacts: []runtimectx.Artifact{artifact}, Reason: "soft_limit",
		ModelWindowFingerprint: runtimectx.ModelWindowFingerprint(runtimectx.ModelWindow{ContextTokens: 4096}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.Generation != checkpoint.Generation+1 || next.Revision != 1 || next.ParentCheckpointID != checkpoint.ID ||
		len(next.ArtifactIDs) != 1 || next.ArtifactIDs[0] != artifact.ID {
		t.Fatalf("invalid rollover checkpoint: %#v", next)
	}
}

func TestAssessModelRequestRequiresRealModelWindow(t *testing.T) {
	t.Parallel()
	manager := runtimectx.NewManager(runtimectx.Dependencies{Counter: exactCounter{tokens: 500}})
	request := model.Request{RunID: "run", Model: "model", Messages: []model.Message{{Role: model.RoleUser, Content: "hello"}}}
	_, err := manager.AssessModelRequest(t.Context(), request, runtimectx.ModelWindow{}, runtimectx.Policy{})
	if !errors.Is(err, runtimectx.ErrModelWindowUnknown) {
		t.Fatalf("missing model window must fail, got %v", err)
	}
	assessment, err := manager.AssessModelRequest(t.Context(), request, runtimectx.ModelWindow{
		ContextTokens: 1000, MaxContextTokens: 900, EffectivePercent: 90, ReservedOutputTokens: 100,
	}, runtimectx.Policy{MaxInputTokens: 800})
	if err != nil {
		t.Fatal(err)
	}
	// min(1000,900)*90%% - 100 = 710, then service ceiling 800 leaves 710.
	if assessment.HardInputTokens != 710 || assessment.SoftInputTokens != 568 || assessment.RawTokenEstimate != 500 ||
		assessment.AdjustedTokenEstimate != 500 || assessment.TokenCountSource != runtimectx.CountExact {
		t.Fatalf("unexpected assessment: %#v", assessment)
	}
}

func openRequest(sourceIDs ...string) runtimectx.OpenRequest {
	entries := make([]runtimectx.Entry, 0, len(sourceIDs))
	for index, id := range sourceIDs {
		role := model.RoleUser
		if index%2 == 1 {
			role = model.RoleAssistant
		}
		entries = append(entries, runtimectx.Entry{
			ID: "entry-" + id, SourceID: id, TurnID: "turn-" + id,
			Message: model.Message{Role: role, Content: "content-" + id},
		})
	}
	return runtimectx.OpenRequest{
		ScopeID: "conversation:thread-1", StaticFingerprint: runtimectx.StaticFingerprint("instructions", "tools"),
		SourcePath: append([]string(nil), sourceIDs...), Entries: entries, Instructions: "system instructions",
	}
}

func messagePrefix(prefix, complete []model.Message) bool {
	if len(prefix) > len(complete) {
		return false
	}
	for index := range prefix {
		left, _ := json.Marshal(prefix[index])
		right, _ := json.Marshal(complete[index])
		if string(left) != string(right) {
			return false
		}
	}
	return true
}

type exactCounter struct{ tokens int64 }

func (counter exactCounter) Count(stdcontext.Context, []byte) (int64, error) {
	return counter.tokens, nil
}
