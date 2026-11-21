package harness_test

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type toolSessionModelFunc func(context.Context, model.Request) (model.Response, error)

func (fn toolSessionModelFunc) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	return fn(ctx, request)
}

func toolSessionOptions(t *testing.T, client model.Client) harness.AgentSessionOptions {
	t.Helper()
	base := textSessionOptions(mustTextRuntime(t, memory.NewStore()), harness.NewMemoryStore(), client,
		harness.TranscriptHistoryFunc(func(context.Context, harness.HostRef, harness.HostRef) ([]runtimecontext.Entry, error) {
			return nil, nil
		}))
	base.Context.ReadArtifacts = true
	base.ExploreReads = true
	base.LocalTools = []harness.SessionTool{{
		Registration: tools.Registration{
			Definition: tools.Definition{Key: "lookup", Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)},
			Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
				return tools.ExecutionResult{Content: json.RawMessage(`{"code":"RELEASE-42"}`), Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}, nil
			}),
		},
		Policy: harness.ToolPolicySnapshot{DefinitionVersion: "v1", ApprovalMode: "never", SideEffectLevel: "read"},
	}}
	return base
}

func TestAgentSessionComposesExplorationAndTerminalRecovery(t *testing.T) {
	modelCalls, toolCalls := 0, 0
	client := toolSessionModelFunc(func(_ context.Context, request model.Request) (model.Response, error) {
		modelCalls++
		if !slices.ContainsFunc(request.Tools, func(def tools.Definition) bool { return def.Key == harness.ContextArtifactToolKey }) {
			t.Fatal("bounded Context has no archive reader")
		}
		if modelCalls <= 4 {
			if !slices.ContainsFunc(request.Tools, func(def tools.Definition) bool { return def.Key == "lookup" }) {
				t.Fatal("read tool was disabled before finding the correct source")
			}
			return model.Response{ToolCalls: []tools.Call{{ID: fmt.Sprintf("lookup-%d", modelCalls), ToolKey: "lookup", Arguments: json.RawMessage(fmt.Sprintf(`{"source":%d}`, modelCalls))}}}, nil
		}
		last := model.TextValue(request.Messages[len(request.Messages)-1].Content)
		if last != `{"code":"RELEASE-42"}` {
			t.Fatalf("answer has no original evidence: %s", last)
		}
		return model.Response{Content: "Verified RELEASE-42"}, nil
	})
	options := toolSessionOptions(t, client)
	original := options.LocalTools[0].Registration.Handler
	options.LocalTools[0].Registration.Handler = tools.HandlerFunc(func(ctx context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
		toolCalls++
		if toolCalls <= 3 {
			return tools.RecoverableNoEffectResult(request.Call.ID, tools.NewRecoverableCallError("lookup.no_match", "Choose a different source", tools.ErrInvalidCall))
		}
		return original.Execute(ctx, request)
	})
	session, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(options.Session.Config.ToolKeys) != 0 || options.LocalTools[0].Policy.Key != "" {
		t.Fatal("facade mutated Host options")
	}
	result, err := session.Prompt(t.Context(), "tool-turn", "Find the verified release code")
	if err != nil || result.Turn.Status != harness.TurnCompleted || modelCalls != 5 || toolCalls != 4 || result.Output == nil || string(result.Output.Content) != `"Verified RELEASE-42"` {
		t.Fatalf("task did not complete: status=%s err=%v models=%d tools=%d output=%+v", result.Turn.Status, err, modelCalls, toolCalls, result.Output)
	}
	if _, err = session.Prompt(t.Context(), "tool-turn", "Find the verified release code"); err != nil || modelCalls != 5 || toolCalls != 4 {
		t.Fatalf("idempotent Prompt redispatched: %v models=%d tools=%d", err, modelCalls, toolCalls)
	}
	options.Session.Transcript = nil
	reopened, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := reopened.Resume(t.Context(), "tool-turn")
	if err != nil || resumed.Turn.ID != result.Turn.ID || resumed.Turn.Status != harness.TurnCompleted || modelCalls != 5 || toolCalls != 4 {
		t.Fatalf("terminal recovery redispatched: %v models=%d tools=%d", err, modelCalls, toolCalls)
	}
}

func TestAgentSessionArchivesLargeToolResultsAndRetrievesExactEvidence(t *testing.T) {
	modelCalls := 0
	marker := regexp.MustCompile(`\[tool_result_compacted artifact_id=([^\s]+)`)
	client := toolSessionModelFunc(func(_ context.Context, request model.Request) (model.Response, error) {
		modelCalls++
		switch modelCalls {
		case 1:
			return model.Response{ToolCalls: []tools.Call{{ID: "large-lookup", ToolKey: "lookup", Arguments: json.RawMessage(`{}`)}}}, nil
		case 2:
			var id string
			for _, message := range request.Messages {
				text := model.TextValue(message.Content)
				if strings.Contains(text, "TRUTH=42") {
					t.Fatal("deep evidence was not removed from inline context")
				}
				if match := marker.FindStringSubmatch(text); len(match) == 2 {
					id = match[1]
				}
			}
			if id == "" {
				t.Fatal("large tool result was not archived")
			}
			arguments, _ := json.Marshal(map[string]any{"artifactID": id, "query": "TRUTH=", "limit": 128})
			return model.Response{ToolCalls: []tools.Call{{ID: "read-original", ToolKey: harness.ContextArtifactToolKey, Arguments: arguments}}}, nil
		case 3:
			last := model.TextValue(request.Messages[len(request.Messages)-1].Content)
			if !strings.Contains(last, "TRUTH=42") {
				t.Fatalf("archive reader did not recover original evidence: %s", last)
			}
			return model.Response{Content: "Verified TRUTH=42 from the original archive"}, nil
		default:
			t.Fatal("unexpected additional model call")
			return model.Response{}, nil
		}
	})
	options := toolSessionOptions(t, client)
	options.Session.Config.ContextPolicy = runtimecontext.Policy{
		MaxInputTokens: 8192, MaxToolResultBytes: 512, MaxCompactionTokens: 80,
		PreserveRecentTurns: 1, MinimumRecentTurns: 1,
	}
	options.LocalTools[0].Registration.Handler = tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
		content, _ := json.Marshal(map[string]string{"dossier": strings.Repeat("padding ", 2000) + "TRUTH=42;" + strings.Repeat("padding ", 2000)})
		return tools.ExecutionResult{Content: content, Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}, nil
	})
	session, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.Prompt(t.Context(), "archive-turn", "Find the TRUTH value in the dossier")
	if err != nil || result.Turn.Status != harness.TurnCompleted || modelCalls != 3 || result.Turn.ContextRef.Generation < 2 {
		t.Fatalf("archive task failed: status=%s err=%v models=%d generation=%d", result.Turn.Status, err, modelCalls, result.Turn.ContextRef.Generation)
	}
}

func TestAgentSessionAppliesQueuedUserInputWithoutAnotherHostTurn(t *testing.T) {
	queue, err := agent.NewMemoryRunInputs("tool-session-inputs")
	if err != nil {
		t.Fatal(err)
	}
	var options harness.AgentSessionOptions
	calls, historyReads := 0, 0
	client := toolSessionModelFunc(func(ctx context.Context, request model.Request) (model.Response, error) {
		calls++
		if calls == 1 {
			snapshot, err := options.Runtime.Load(ctx, request.RunID)
			if err != nil {
				return model.Response{}, err
			}
			if _, err = queue.Submit(ctx, snapshot.Run, "change-1", "Summarize the second source instead"); err != nil {
				return model.Response{}, err
			}
			return model.Response{Content: "first direction"}, nil
		}
		last := request.Messages[len(request.Messages)-1]
		if last.Role != model.RoleUser || model.TextValue(last.Content) != "Summarize the second source instead" {
			t.Errorf("bounded Context lost queued user input: %+v", request.Messages)
		}
		return model.Response{Content: "second source summary"}, nil
	})
	options = toolSessionOptions(t, client)
	options.Inputs = queue
	options.Session.Transcript = harness.TranscriptHistoryFunc(func(context.Context, harness.HostRef, harness.HostRef) ([]runtimecontext.Entry, error) {
		historyReads++
		return nil, nil
	})
	session, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.Prompt(t.Context(), "input-turn", "Summarize the first source")
	if err != nil || result.Turn.Status != harness.TurnCompleted || calls != 2 || historyReads != 1 ||
		result.Output == nil || string(result.Output.Content) != `"second source summary"` {
		t.Fatalf("queued session input failed: status=%s err=%v calls=%d historyReads=%d", result.Turn.Status, err, calls, historyReads)
	}
}
