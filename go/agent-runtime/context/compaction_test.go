package context_test

import (
	stdcontext "context"
	"encoding/json"
	"strings"
	"testing"

	runtimectx "github.com/orz-i/Gaoge/sdk/go/agent-runtime/context"
)

func TestBuilderSealsOversizedToolResult(t *testing.T) {
	t.Parallel()
	builder := runtimectx.NewBuilder(runtimectx.Dependencies{})
	request := baseRequest()
	fullOutput := strings.Repeat("tool-output-", 600)
	request.Prompt.Items = []runtimectx.Item{
		{
			ID: "current-user", TurnID: testCurrentTurnID, Kind: runtimectx.ItemMessage,
			Role: runtimectx.RoleUser, Content: "Run the tool",
		},
		{
			ID: "current-call", TurnID: testCurrentTurnID, Kind: runtimectx.ItemToolCall,
			Role: runtimectx.RoleAssistant, Content: testCallingLookup,
			ToolCallID: "call_1", ToolName: testToolNameLookup,
		},
		{
			ID: "current-result", TurnID: testCurrentTurnID, Kind: runtimectx.ItemToolResult,
			Role: runtimectx.RoleTool, Content: fullOutput,
			ToolCallID: "call_1", ToolName: testToolNameLookup,
		},
	}
	request.Budget.MaxInputTokens = 4_096
	request.Budget.EffectiveModelTokens = 4_096
	request.Budget.MaxToolResultBytes = 512

	result, err := builder.Build(stdcontext.Background(), request)
	if err != nil {
		t.Fatalf("build compacted context: %v", err)
	}
	assertToolArtifact(t, result, fullOutput)
	assertCompactedToolPrompt(t, result, fullOutput)
}

func assertToolArtifact(t *testing.T, result runtimectx.BuildResult, fullOutput string) {
	t.Helper()
	if len(result.Artifacts) != 1 {
		t.Fatalf("unexpected tool artifacts: %#v", result.Artifacts)
	}
	artifact := result.Artifacts[0]
	if artifact.Kind != runtimectx.ArtifactToolResult || artifact.Content != fullOutput || artifact.ContentHash == "" {
		t.Fatalf("unexpected tool artifact: %#v", artifact)
	}
}

func assertCompactedToolPrompt(t *testing.T, result runtimectx.BuildResult, fullOutput string) {
	t.Helper()
	prompt := decodePrompt(t, result.Snapshot.Content)
	if len(prompt.Items) != 3 {
		t.Fatalf("tool turn was split: %#v", prompt.Items)
	}
	replacement := prompt.Items[2].Content
	artifact := result.Artifacts[0]
	if strings.Contains(replacement, fullOutput) || !strings.Contains(replacement, artifact.ID) ||
		!strings.Contains(replacement, artifact.ContentHash) {
		t.Fatalf("unexpected compacted replacement: %s", replacement)
	}
	if result.Snapshot.Trace.ArtifactCount != 1 || !hasAction(result.Snapshot.Trace.Actions, "compact_tool_result") {
		t.Fatalf("missing compaction trace: %#v", result.Snapshot.Trace)
	}
}

func assertSummarizedPrompt(t *testing.T, result runtimectx.BuildResult) {
	t.Helper()
	prompt := decodePrompt(t, result.Snapshot.Content)
	for _, oldTurn := range []string{"turn-1", testTurn2, "turn-3"} {
		if turnPresent(prompt.Items, oldTurn) {
			t.Fatalf("old summarized turn remains inline: %s", oldTurn)
		}
	}
	if !turnPresent(prompt.Items, "turn-4") || !turnPresent(prompt.Items, testCurrentTurnID) {
		t.Fatalf("recent or current turn was removed: %#v", prompt.Items)
	}
	summaryItem, ok := itemByKind(prompt.Items, runtimectx.ItemSummary)
	if !ok || !summaryItem.Required || !strings.HasPrefix(summaryItem.Content, "<sum>") {
		t.Fatalf("summary not injected as required untrusted history: %#v", summaryItem)
	}
}

func assertSummaryTrace(t *testing.T, result runtimectx.BuildResult) {
	t.Helper()
	summary := result.Snapshot.Trace.Summary
	if summary == nil || summary.CoveredTurns != 3 || summary.CoveredItems != 7 || summary.CoveredThrough != "turn-3" {
		t.Fatalf("unexpected summary coverage: %#v", summary)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].Kind != runtimectx.ArtifactSummary ||
		result.Artifacts[0].ID != summary.ArtifactID {
		t.Fatalf("unexpected summary artifact: %#v", result.Artifacts)
	}
	managed := result.Snapshot.Trace.Managed
	if managed.AdjustedTokenEstimate > managed.HardInputTokens {
		t.Fatalf("managed prompt exceeds hard budget: %#v", managed)
	}
}

func TestBuilderSummarizesOnlyOldCompleteTurns(t *testing.T) {
	t.Parallel()
	builder := runtimectx.NewBuilder(runtimectx.Dependencies{})
	request := baseRequest()
	request.Prompt.Items = []runtimectx.Item{
		message("t1-user", "turn-1", runtimectx.RoleUser, repeated("old question one ", 20)),
		message("t1-answer", "turn-1", runtimectx.RoleAssistant, repeated("old answer one ", 20)),
		message("t2-user", testTurn2, runtimectx.RoleUser, repeated("old question two ", 20)),
		{
			ID: "t2-call", TurnID: testTurn2, Kind: runtimectx.ItemToolCall,
			Role: runtimectx.RoleAssistant, Content: "Call archive", ToolCallID: "call-old", ToolName: "archive",
		},
		{
			ID: "t2-result", TurnID: testTurn2, Kind: runtimectx.ItemToolResult,
			Role: runtimectx.RoleTool, Content: repeated("old tool result ", 20), ToolCallID: "call-old", ToolName: "archive",
		},
		message("t3-user", "turn-3", runtimectx.RoleUser, repeated("old question three ", 20)),
		message("t3-answer", "turn-3", runtimectx.RoleAssistant, repeated("old answer three ", 20)),
		message("t4-user", "turn-4", runtimectx.RoleUser, repeated("recent question ", 20)),
		message("t4-answer", "turn-4", runtimectx.RoleAssistant, repeated("recent answer ", 20)),
		message("current", testCurrentTurnID, runtimectx.RoleUser, repeated("current request ", 12)),
	}
	request.Budget.MaxInputTokens = 1_100
	request.Budget.EffectiveModelTokens = 1_100
	request.Budget.SoftLimitPercent = 35
	request.Budget.PreserveRecentTurns = 2
	request.Budget.MaxSummaryTokens = 96
	request.Budget.MaxToolResultBytes = 4_096

	result, err := builder.Build(stdcontext.Background(), request)
	if err != nil {
		t.Fatalf("build summarized context: %v", err)
	}
	assertSummarizedPrompt(t, result)
	assertSummaryTrace(t, result)
}

func TestArtifactIdentityIsStableAcrossRetries(t *testing.T) {
	t.Parallel()
	builder := runtimectx.NewBuilder(runtimectx.Dependencies{})
	request := baseRequest()
	request.Prompt.Items = []runtimectx.Item{
		message("current", testCurrentTurnID, runtimectx.RoleUser, "Run lookup"),
		{
			ID: "call", TurnID: testCurrentTurnID, Kind: runtimectx.ItemToolCall,
			Role: runtimectx.RoleAssistant, Content: testCallingLookup,
			ToolCallID: "call-stable", ToolName: testToolNameLookup,
		},
		{
			ID: testResultID, TurnID: testCurrentTurnID, Kind: runtimectx.ItemToolResult,
			Role: runtimectx.RoleTool, Content: strings.Repeat("x", 3_000),
			ToolCallID: "call-stable", ToolName: testToolNameLookup,
		},
	}
	request.Budget.MaxToolResultBytes = 512
	first, err := builder.Build(stdcontext.Background(), request)
	if err != nil {
		t.Fatalf("build first context: %v", err)
	}
	second, err := builder.Build(stdcontext.Background(), request)
	if err != nil {
		t.Fatalf("build second context: %v", err)
	}
	if len(first.Artifacts) != 1 || len(second.Artifacts) != 1 ||
		first.Artifacts[0].ID != second.Artifacts[0].ID || first.Artifacts[0].ContentHash != second.Artifacts[0].ContentHash ||
		first.Snapshot.ID != second.Snapshot.ID {
		t.Fatalf("retry identities are unstable: first=%#v second=%#v", first, second)
	}
}

func decodePrompt(t *testing.T, content json.RawMessage) runtimectx.Prompt {
	t.Helper()
	var prompt runtimectx.Prompt
	if err := json.Unmarshal(content, &prompt); err != nil {
		t.Fatalf("decode prompt: %v", err)
	}
	return prompt
}

func message(id string, turnID string, role runtimectx.Role, content string) runtimectx.Item {
	return runtimectx.Item{ID: id, TurnID: turnID, Kind: runtimectx.ItemMessage, Role: role, Content: content}
}

func turnPresent(items []runtimectx.Item, turnID string) bool {
	for _, item := range items {
		if item.TurnID == turnID {
			return true
		}
	}
	return false
}

func itemByKind(items []runtimectx.Item, kind runtimectx.ItemKind) (runtimectx.Item, bool) {
	for _, item := range items {
		if item.Kind == kind {
			return item, true
		}
	}
	return runtimectx.Item{}, false
}

func hasAction(actions []runtimectx.TrimAction, kind string) bool {
	for _, action := range actions {
		if action.Kind == kind {
			return true
		}
	}
	return false
}
