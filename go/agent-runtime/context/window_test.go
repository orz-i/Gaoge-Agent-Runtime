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

const (
	testLargeToolCallID = "call-large"
	testLookupToolKey   = "lookup"
)

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func requireEqual[T comparable](t *testing.T, got, want T, message string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got=%v want=%v", message, got, want)
	}
}

func requireTrue(t *testing.T, condition bool, message string) {
	t.Helper()
	if !condition {
		t.Fatal(message)
	}
}

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
		model.Message{Role: model.RoleAssistant, ToolCalls: []tools.Call{{ID: testLargeToolCallID, ToolKey: testLookupToolKey, Arguments: json.RawMessage(`{}`)}}},
		model.Message{Role: model.RoleTool, ToolCallID: testLargeToolCallID, Content: repeatedText("tool-payload-", 80)},
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
		{Role: model.RoleUser, Content: testLookupToolKey},
		{Role: model.RoleAssistant, ToolCalls: []tools.Call{{ID: testLargeToolCallID, ToolKey: testLookupToolKey, Arguments: json.RawMessage(`{}`)}}},
		{Role: model.RoleTool, ToolCallID: testLargeToolCallID, Content: content},
	}
	first, err := manager.CompactToolResults("conversation:thread-1", 3, messages, 256)
	requireNoError(t, err)
	second, err := manager.CompactToolResults("conversation:thread-1", 3, messages, 256)
	requireNoError(t, err)
	requireEqual(t, len(first.Artifacts), 1, "Tool artifact count")
	requireTrue(t, runtimectx.ValidArtifact(first.Artifacts[0]), "Tool artifact is invalid")
	requireEqual(t, first.Artifacts[0].Content, content, "Tool artifact exact content")
	requireEqual(t, len(second.Artifacts), 1, "deterministic Tool artifact count")
	requireEqual(t, second.Artifacts[0].ID, first.Artifacts[0].ID, "deterministic Tool artifact ID")
	requireEqual(t, second.Messages[2].Content, first.Messages[2].Content, "deterministic Tool replacement")
	requireTrue(t, first.Messages[2].Content != content, "Tool result was not compacted")
	requireTrue(t, containsAll(first.Messages[2].Content, first.Artifacts[0].ID, first.Artifacts[0].ContentHash, "<head>", "<tail>"), "Tool replacement did not carry durable identity")
	replayed, err := manager.CompactToolResults("conversation:thread-1", 3, first.Messages, 256)
	requireNoError(t, err)
	requireEqual(t, len(replayed.Artifacts), 0, "replayed Tool artifact count")
	requireEqual(t, replayed.Messages[2].Content, first.Messages[2].Content, "replayed Tool replacement")
}

func TestCompactPortablePreservesRecentTurnsAndSealsRemovedTranscript(t *testing.T) {
	t.Parallel()
	manager := runtimectx.NewManager(runtimectx.Dependencies{})
	checkpoint, err := manager.Open(t.Context(), openRequest("u1", "a1", "u2", "a2", "u3"))
	requireNoError(t, err)
	messages := runtimectx.Materialize(checkpoint.Window)
	compacted, err := manager.CompactPortable(runtimectx.PortableCompactionRequest{
		Previous: checkpoint, RunID: "run-portable", Messages: messages,
		Policy: runtimectx.Policy{PreserveRecentTurns: 2, MaxCompactionTokens: 128},
	})
	requireNoError(t, err)
	requireEqual(t, compacted.RemovedMessages, 2, "portable removed message count")
	requireTrue(t, runtimectx.ValidArtifact(compacted.Artifact), "portable compaction artifact is invalid")
	requireEqual(t, len(compacted.Window.Entries), 4, "portable active entry count")
	var removed []model.Message
	requireNoError(t, json.Unmarshal(compacted.Artifact.ContentJSON, &removed))
	requireEqual(t, len(removed), 2, "portable artifact removed transcript count")
	requireEqual(t, removed[0].Content, "content-u1", "portable artifact first removed message")
	requireEqual(t, removed[1].Content, "content-a1", "portable artifact second removed message")
	active := runtimectx.Materialize(compacted.Window)
	requireTrue(t, containsAll(active[1].Content, compacted.Artifact.ID, compacted.Artifact.ContentHash), "portable checkpoint did not identify its durable artifact")
	requireEqual(t, active[len(active)-3].Content, "content-u2", "portable preserved older recent user turn")
	requireEqual(t, active[len(active)-1].Content, "content-u3", "portable preserved current user turn")
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
	requireNoError(t, err)
	firstFingerprint := runtimectx.ModelWindowFingerprint(runtimectx.ModelWindow{ContextTokens: 4096})
	bound, err := manager.BindModelWindow(t.Context(), checkpoint, firstFingerprint)
	requireNoError(t, err)
	requireEqual(t, bound.Generation, checkpoint.Generation, "initial model binding generation")
	requireEqual(t, bound.Revision, checkpoint.Revision+1, "initial model binding revision")
	requireEqual(t, bound.ParentCheckpointID, checkpoint.ID, "initial model binding parent")
	requireEqual(t, bound.ModelWindowFingerprint, firstFingerprint, "initial model binding fingerprint")
	replayed, err := manager.BindModelWindow(t.Context(), bound, firstFingerprint)
	requireNoError(t, err)
	requireEqual(t, replayed.ID, bound.ID, "same model window idempotency")
	secondFingerprint := runtimectx.ModelWindowFingerprint(runtimectx.ModelWindow{ContextTokens: 8192})
	changed, err := manager.BindModelWindow(t.Context(), bound, secondFingerprint)
	requireNoError(t, err)
	requireEqual(t, changed.Generation, bound.Generation+1, "changed model window generation")
	requireEqual(t, changed.Revision, 1, "changed model window revision")
	requireEqual(t, changed.ParentCheckpointID, bound.ID, "changed model window parent")
	requireEqual(t, changed.ModelWindowFingerprint, secondFingerprint, "changed model window fingerprint")
	requireEqual(t, changed.Trace.Reason, "model_window_changed", "changed model window reason")
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
		model.Message{Role: model.RoleAssistant, ToolCalls: []tools.Call{{ID: "call-1", ToolKey: testLookupToolKey, Arguments: json.RawMessage(`{"q":"x"}`)}}},
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
