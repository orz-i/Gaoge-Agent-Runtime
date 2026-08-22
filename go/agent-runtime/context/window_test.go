package context_test

import (
	stdcontext "context"
	"encoding/json"
	"errors"
	"strings"
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
	if first.CacheIdentity == "" || second.CacheIdentity != first.CacheIdentity {
		t.Fatalf("ordinary source append changed cache identity: %q -> %q", first.CacheIdentity, second.CacheIdentity)
	}
	firstMessages := runtimectx.Materialize(first.Window)
	secondMessages := runtimectx.Materialize(second.Window)
	if !messagePrefix(firstMessages, secondMessages) {
		t.Fatalf("source append rewrote stable prefix:\nfirst=%#v\nsecond=%#v", firstMessages, secondMessages)
	}
}

func TestCaptureCarriesToolArtifactLineageWithStablePrefix(t *testing.T) {
	t.Parallel()
	manager := runtimectx.NewManager(runtimectx.Dependencies{})
	checkpoint, err := manager.Open(t.Context(), openRequest("m1", "m2"))
	if err != nil {
		t.Fatal(err)
	}
	messages := append(
		runtimectx.Materialize(checkpoint.Window),
		model.Message{Role: model.RoleAssistant, ToolCalls: []tools.Call{{ID: "call-large", ToolKey: "lookup", Arguments: json.RawMessage(`{}`)}}},
		model.Message{Role: model.RoleTool, ToolCallID: "call-large", Content: repeatedText("tool-payload-", 80)},
	)
	compacted, err := manager.CompactToolResults(checkpoint.ScopeID, checkpoint.Generation, messages, 256)
	if err != nil {
		t.Fatal(err)
	}
	next, err := manager.Capture(t.Context(), runtimectx.CaptureRequest{
		Previous: checkpoint, StaticFingerprint: checkpoint.StaticFingerprint, RunID: "run-capture",
		Messages: compacted.Messages, Artifacts: compacted.Artifacts,
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.Generation != checkpoint.Generation || next.Revision != checkpoint.Revision+1 ||
		len(next.ArtifactIDs) != 1 || next.ArtifactIDs[0] != compacted.Artifacts[0].ID ||
		next.Trace.ArtifactCount != 1 {
		t.Fatalf("capture did not carry Tool artifact lineage: %#v", next)
	}
	if !messagePrefix(runtimectx.Materialize(checkpoint.Window), runtimectx.Materialize(next.Window)) {
		t.Fatalf("capture rewrote the stable prefix: %#v", next.Window)
	}
}

func repeatedText(value string, count int) string {
	result := ""
	for index := 0; index < count; index++ {
		result += value
	}
	return result
}

func containsAll(value string, needles ...string) bool {
	for _, needle := range needles {
		if needle == "" || !strings.Contains(value, needle) {
			return false
		}
	}
	return true
}

func TestCompactToolResultsSealsExactArtifactAndIsDeterministic(t *testing.T) {
	t.Parallel()
	manager := runtimectx.NewManager(runtimectx.Dependencies{})
	content := repeatedText("large-tool-result-", 80)
	messages := []model.Message{
		{Role: model.RoleUser, Content: "lookup"},
		{Role: model.RoleAssistant, ToolCalls: []tools.Call{{ID: "call-large", ToolKey: "lookup", Arguments: json.RawMessage(`{}`)}}},
		{Role: model.RoleTool, ToolCallID: "call-large", Content: content},
	}
	first, err := manager.CompactToolResults("conversation:thread-1", 3, messages, 256)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.CompactToolResults("conversation:thread-1", 3, messages, 256)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Artifacts) != 1 || !runtimectx.ValidArtifact(first.Artifacts[0]) || first.Artifacts[0].Content != content {
		t.Fatalf("invalid Tool artifact: %#v", first.Artifacts)
	}
	if len(second.Artifacts) != 1 || second.Artifacts[0].ID != first.Artifacts[0].ID ||
		second.Messages[2].Content != first.Messages[2].Content {
		t.Fatalf("Tool compaction is not deterministic: first=%#v second=%#v", first, second)
	}
	if first.Messages[2].Content == content ||
		!containsAll(first.Messages[2].Content, first.Artifacts[0].ID, first.Artifacts[0].ContentHash, "<head>", "<tail>") {
		t.Fatalf("Tool replacement did not carry durable identity: %q", first.Messages[2].Content)
	}
	replayed, err := manager.CompactToolResults("conversation:thread-1", 3, first.Messages, 256)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed.Artifacts) != 0 || replayed.Messages[2].Content != first.Messages[2].Content {
		t.Fatalf("already compacted Tool result was compacted again: %#v", replayed)
	}
}

func TestCompactPortablePreservesRecentTurnsAndSealsRemovedTranscript(t *testing.T) {
	t.Parallel()
	manager := runtimectx.NewManager(runtimectx.Dependencies{})
	checkpoint, err := manager.Open(t.Context(), openRequest("u1", "a1", "u2", "a2", "u3"))
	if err != nil {
		t.Fatal(err)
	}
	messages := runtimectx.Materialize(checkpoint.Window)
	compacted, err := manager.CompactPortable(runtimectx.PortableCompactionRequest{
		Previous: checkpoint, RunID: "run-portable", Messages: messages,
		Policy: runtimectx.Policy{PreserveRecentTurns: 2, MaxCompactionTokens: 128},
	})
	if err != nil {
		t.Fatal(err)
	}
	if compacted.RemovedMessages != 2 || !runtimectx.ValidArtifact(compacted.Artifact) || len(compacted.Window.Entries) != 4 {
		t.Fatalf("unexpected portable compaction: %#v", compacted)
	}
	var removed []model.Message
	if err := json.Unmarshal(compacted.Artifact.ContentJSON, &removed); err != nil {
		t.Fatalf("decode exact removed transcript: %v", err)
	}
	if len(removed) != 2 || removed[0].Content != "content-u1" || removed[1].Content != "content-a1" {
		t.Fatalf("artifact did not preserve exact removed transcript: %#v", removed)
	}
	active := runtimectx.Materialize(compacted.Window)
	if !containsAll(active[1].Content, compacted.Artifact.ID, compacted.Artifact.ContentHash) ||
		active[len(active)-3].Content != "content-u2" || active[len(active)-1].Content != "content-u3" {
		t.Fatalf("recent turns were not preserved: %#v", active)
	}
}

func TestCompactPortableRefusesToDropProtectedHistory(t *testing.T) {
	t.Parallel()
	manager := runtimectx.NewManager(runtimectx.Dependencies{})
	checkpoint, err := manager.Open(t.Context(), openRequest("u1", "a1", "u2"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.CompactPortable(runtimectx.PortableCompactionRequest{
		Previous: checkpoint, RunID: "run-protected", Messages: runtimectx.Materialize(checkpoint.Window),
		Policy: runtimectx.Policy{PreserveRecentTurns: 3, MaxCompactionTokens: 128},
	})
	if !errors.Is(err, runtimectx.ErrBudgetExceeded) {
		t.Fatalf("protected recent history must not be dropped, got %v", err)
	}
}

func TestBindModelWindowCreatesRevisionThenGenerationOnChange(t *testing.T) {
	t.Parallel()
	manager := runtimectx.NewManager(runtimectx.Dependencies{})
	checkpoint, err := manager.Open(t.Context(), openRequest("m1", "m2"))
	if err != nil {
		t.Fatal(err)
	}
	firstFingerprint := runtimectx.ModelWindowFingerprint(runtimectx.ModelWindow{ContextTokens: 4096})
	bound, err := manager.BindModelWindow(t.Context(), checkpoint, firstFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if bound.Generation != checkpoint.Generation || bound.Revision != checkpoint.Revision+1 ||
		bound.ParentCheckpointID != checkpoint.ID || bound.ModelWindowFingerprint != firstFingerprint {
		t.Fatalf("initial model window binding must advance only revision: %#v", bound)
	}
	replayed, err := manager.BindModelWindow(t.Context(), bound, firstFingerprint)
	if err != nil || replayed.ID != bound.ID {
		t.Fatalf("same model window must be idempotent: %#v err=%v", replayed, err)
	}
	secondFingerprint := runtimectx.ModelWindowFingerprint(runtimectx.ModelWindow{ContextTokens: 8192})
	changed, err := manager.BindModelWindow(t.Context(), bound, secondFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Generation != bound.Generation+1 || changed.Revision != 1 || changed.ParentCheckpointID != bound.ID ||
		changed.ModelWindowFingerprint != secondFingerprint || changed.Trace.Reason != "model_window_changed" {
		t.Fatalf("changed model window must create explicit generation: %#v", changed)
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
	if second.CacheIdentity == first.CacheIdentity {
		t.Fatalf("branch reset must invalidate cache identity: %q", second.CacheIdentity)
	}
}

func TestManagerExplicitCacheResetInvalidatesIdentityWithoutChangingSourceShape(t *testing.T) {
	t.Parallel()
	manager := runtimectx.NewManager(runtimectx.Dependencies{})
	first, err := manager.Open(t.Context(), openRequest("m1", "m2"))
	if err != nil {
		t.Fatal(err)
	}
	request := openRequest("m1", "m2", "m3")
	request.Previous = &first
	request.ResetCacheIdentity = true
	second, err := manager.Open(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second.Generation != first.Generation+1 || second.Trace.Reason != "lineage_reset" || second.CacheIdentity == first.CacheIdentity {
		t.Fatalf("explicit cache reset did not create a new identity: first=%#v second=%#v", first, second)
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
	if next.CacheIdentity != checkpoint.CacheIdentity {
		t.Fatalf("context rollover must preserve cache identity: %q -> %q", checkpoint.CacheIdentity, next.CacheIdentity)
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
