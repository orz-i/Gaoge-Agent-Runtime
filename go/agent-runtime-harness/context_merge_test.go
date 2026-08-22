package harness

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	runtimecontext "github.com/orz-i/Gaoge/sdk/go/agent-runtime/context"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/model"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

const (
	parentGoal                  = "parent goal"
	contextFingerprintTestTurn = "turn-1"
)

func TestMergeContextRuntimeMessagesDirectGoalDoesNotDuplicateCurrentTurn(t *testing.T) {
	t.Parallel()
	contextMessages := []model.Message{
		{Role: model.RoleSystem, Content: "frozen instructions"},
		{Role: model.RoleUser, Content: parentGoal},
	}
	runtimeMessages := []model.Message{
		{Role: model.RoleSystem, Content: "runtime guidance"},
		{Role: model.RoleUser, Content: parentGoal},
		{Role: model.RoleAssistant, Content: "live continuation"},
	}

	merged, err := mergeContextRuntimeMessages(contextMessages, runtimeMessages)
	if err != nil {
		t.Fatalf("merge direct goal: %v", err)
	}
	want := []model.Message{
		{Role: model.RoleSystem, Content: "frozen instructions"},
		{Role: model.RoleUser, Content: parentGoal},
		{Role: model.RoleSystem, Content: "runtime guidance"},
		{Role: model.RoleAssistant, Content: "live continuation"},
	}
	assertRuntimeMessagesEqual(t, merged, want)
}

func TestContextStaticFingerprintIgnoresSamplingOnlyModelOptions(t *testing.T) {
	t.Parallel()
	seed := &ContextSeed{
		SourcePath: []string{"message-1"},
		Entries: []runtimecontext.Entry{{
			ID: "entry-1", SourceID: "message-1", TurnID: contextFingerprintTestTurn,
			Message: model.Message{Role: model.RoleUser, Content: "keep the immutable prefix stable"},
		}},
	}
	base := ConfigSnapshot{
		Environment: VersionRef{ID: "general", Revision: 7}, Instructions: "stable system instructions",
		Model: "model-a", ModelOptions: json.RawMessage(`{"temperature":0.2,"reasoning":{"effort":"medium"}}`),
	}
	first, err := contextStaticFingerprint(base, seed, nil)
	if err != nil {
		t.Fatal(err)
	}
	changedOptions := base
	changedOptions.ModelOptions = json.RawMessage(`{"temperature":0.9,"reasoning":{"effort":"high"},"max_output_tokens":2048}`)
	second, err := contextStaticFingerprint(changedOptions, seed, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("sampling-only ModelOptions reset Context fingerprint: %q -> %q", first, second)
	}
	changedModel := base
	changedModel.Model = "model-b"
	third, err := contextStaticFingerprint(changedModel, seed, nil)
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Fatalf("model identity change did not reset Context fingerprint: %q", third)
	}
}

func TestMergeContextRuntimeMessagesNestedGoalKeepsParentContextAndChildGoal(t *testing.T) {
	t.Parallel()
	contextMessages := []model.Message{
		{Role: model.RoleSystem, Content: "frozen instructions"},
		{Role: model.RoleUser, Content: "original conversation request"},
	}
	runtimeMessages := []model.Message{
		{Role: model.RoleSystem, Content: "execute this bounded step"},
		{Role: model.RoleUser, Content: "child plan step goal"},
	}

	merged, err := mergeContextRuntimeMessages(contextMessages, runtimeMessages)
	if err != nil {
		t.Fatalf("merge nested goal: %v", err)
	}
	want := []model.Message{
		{Role: model.RoleSystem, Content: "frozen instructions"},
		{Role: model.RoleUser, Content: "original conversation request"},
		{Role: model.RoleSystem, Content: "execute this bounded step"},
		{Role: model.RoleUser, Content: "child plan step goal"},
	}
	assertRuntimeMessagesEqual(t, merged, want)
}

func TestMergeContextRuntimeMessagesStillRejectsRuntimeWithoutUserGoal(t *testing.T) {
	t.Parallel()
	_, err := mergeContextRuntimeMessages(
		[]model.Message{{Role: model.RoleUser, Content: parentGoal}},
		[]model.Message{{Role: model.RoleSystem, Content: "guidance only"}},
	)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("expected invalid request, got %v", err)
	}
}

func TestMergeContextRuntimeMessagesAfterRolloverDoesNotDuplicateToolTranscript(t *testing.T) {
	t.Parallel()
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	call := tools.Call{ID: "call-1", ToolKey: "lookup", Arguments: json.RawMessage(`{"q":"x"}`)}
	first, err := manager.Open(t.Context(), runtimecontext.OpenRequest{
		ScopeID: "session-tool-overlap", StaticFingerprint: runtimecontext.StaticFingerprint("stable"),
		SourcePath: []string{"m1"}, Instructions: "frozen instructions",
		Entries: []runtimecontext.Entry{{
			ID: "entry-m1", SourceID: "m1", TurnID: "turn-1", Message: model.Message{Role: model.RoleUser, Content: parentGoal},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	fullToolResult := strings.Repeat(`{"result":"large"}`, 64)
	modelVisible := append(runtimecontext.Materialize(first.Window),
		model.Message{Role: model.RoleAssistant, ToolCalls: []tools.Call{call}},
		model.Message{Role: model.RoleTool, ToolCallID: call.ID, Content: fullToolResult},
	)
	compacted, err := manager.CompactToolResults(first.ScopeID, first.Generation, modelVisible, 256)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := manager.Capture(t.Context(), runtimecontext.CaptureRequest{
		Previous: first, StaticFingerprint: first.StaticFingerprint, RunID: "run-tool",
		Messages: compacted.Messages, Artifacts: compacted.Artifacts,
	})
	if err != nil {
		t.Fatal(err)
	}
	contextMessages := runtimecontext.Materialize(checkpoint.Window)
	runtimeMessages := []model.Message{
		{Role: model.RoleUser, Content: parentGoal},
		{Role: model.RoleAssistant, ToolCalls: []tools.Call{call}},
		{Role: model.RoleTool, ToolCallID: call.ID, Content: fullToolResult},
		{Role: model.RoleAssistant, Content: "continue after Tool"},
	}

	merged, err := mergeContextRuntimeMessagesForCheckpoint(checkpoint, contextMessages, runtimeMessages)
	if err != nil {
		t.Fatalf("merge rollover Tool transcript: %v", err)
	}
	want := append(model.CloneMessages(contextMessages), model.Message{Role: model.RoleAssistant, Content: "continue after Tool"})
	if len(merged) != len(want) {
		t.Fatalf("rollover merge duplicated transcript: got=%#v want=%#v", merged, want)
	}
	for index := range want {
		if !sameContextModelMessage(merged[index], want[index]) {
			t.Fatalf("message[%d] = %#v want %#v", index, merged[index], want[index])
		}
	}
}

func TestReplaceContextCheckpointUsesExecutionScopedCAS(t *testing.T) {
	t.Parallel()
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	first, err := manager.Open(t.Context(), runtimecontext.OpenRequest{
		ScopeID: "session-cas", StaticFingerprint: runtimecontext.StaticFingerprint("stable"),
		SourcePath: []string{"m1"},
		Entries: []runtimecontext.Entry{{
			ID: "entry-m1", SourceID: "m1", TurnID: "turn-1", Message: model.Message{Role: model.RoleUser, Content: parentGoal},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	next, err := manager.Rollover(t.Context(), runtimecontext.RolloverRequest{
		Previous: first, Window: first.Window, Reason: "test_rollover",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withContextCheckpoint(t.Context(), first)
	if err = ReplaceContextCheckpoint(ctx, "stale-checkpoint", next); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale checkpoint CAS must fail, got %v", err)
	}
	if err = ReplaceContextCheckpoint(ctx, first.ID, next); err != nil {
		t.Fatalf("replace checkpoint: %v", err)
	}
	current, ok := CurrentContextCheckpoint(ctx)
	if !ok || current.ID != next.ID || current.Generation != next.Generation {
		t.Fatalf("active checkpoint was not advanced: %#v ok=%v", current, ok)
	}
}

func assertRuntimeMessagesEqual(t *testing.T, got, want []model.Message) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("message count = %d, want %d: %#v", len(got), len(want), got)
	}
	for index := range want {
		if got[index].Role != want[index].Role || got[index].Content != want[index].Content {
			t.Fatalf("message[%d] = %#v, want %#v", index, got[index], want[index])
		}
	}
}
