package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type toolSessionModelFunc func(context.Context, model.Request) (model.Response, error)

func (fn toolSessionModelFunc) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	return fn(ctx, request)
}

func toolSessionOptions(t *testing.T, client model.Client) harness.ToolAgentSessionOptions {
	t.Helper()
	base := textSessionOptions(mustTextRuntime(t, memory.NewStore()), harness.NewMemoryStore(), client,
		harness.TranscriptHistoryFunc(func(context.Context, harness.HostRef, harness.HostRef) ([]runtimecontext.Entry, error) {
			return nil, nil
		}))
	return harness.ToolAgentSessionOptions{
		Runtime: base.Runtime, Store: base.Store, Model: base.Model, Session: base.Session,
		ResolveModelWindow: func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
			return runtimecontext.ModelWindow{ContextTokens: 32768, ReservedOutputTokens: 1024}, nil
		},
		Tools: []harness.SessionTool{{
			Registration: tools.Registration{
				Definition: tools.Definition{Key: "lookup", Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)},
				Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
					return tools.ExecutionResult{Content: json.RawMessage(`{"code":"RELEASE-42"}`), Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}, nil
				}),
			},
			Policy: harness.ToolPolicySnapshot{DefinitionVersion: "v1", ApprovalMode: "never", SideEffectLevel: "read"},
		}},
	}
}

func TestToolAgentSessionComposesExplorationAndTerminalRecovery(t *testing.T) {
	t.Run("deprecated-facade", func(t *testing.T) { testSessionExplorationAndTerminalRecovery(t, false) })
	t.Run("recommended-facade", func(t *testing.T) { testSessionExplorationAndTerminalRecovery(t, true) })
}

func testSessionExplorationAndTerminalRecovery(t *testing.T, paired bool) {
	t.Helper()
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
	original := options.Tools[0].Registration.Handler
	options.Tools[0].Registration.Handler = tools.HandlerFunc(func(ctx context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
		toolCalls++
		if toolCalls <= 3 {
			return tools.RecoverableNoEffectResult(request.Call.ID, tools.NewRecoverableCallError("lookup.no_match", "Choose a different source", tools.ErrInvalidCall))
		}
		return original.Execute(ctx, request)
	})
	open := func() (*harness.SessionClient, error) {
		if !paired {
			return harness.OpenToolAgentSession(options)
		}
		settings := options.Session
		settings.Config.MaxModelOutputTokens = 128
		return harness.OpenAgentSession(harness.AgentSessionOptions{
			Runtime: options.Runtime, Store: options.Store, Model: client, Session: settings, LocalTools: options.Tools, ExploreReads: true,
			Context: harness.AgentSessionContextOptions{ResolveWindow: options.ResolveModelWindow, ReadArtifacts: true},
		})
	}
	session, err := open()
	if err != nil {
		t.Fatal(err)
	}
	if len(options.Session.Config.ToolKeys) != 0 || options.Tools[0].Policy.Key != "" {
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
	reopened, err := open()
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := reopened.Resume(t.Context(), "tool-turn")
	if err != nil || resumed.Turn.ID != result.Turn.ID || resumed.Turn.Status != harness.TurnCompleted || modelCalls != 5 || toolCalls != 4 {
		t.Fatalf("terminal recovery redispatched: %v models=%d tools=%d", err, modelCalls, toolCalls)
	}
}

func TestToolAgentSessionRejectsUncomposedOrConflictingCapabilities(t *testing.T) {
	client := &textSessionModel{}
	cases := []struct {
		name string
		edit func(*harness.ToolAgentSessionOptions)
	}{
		{"missing runtime", func(o *harness.ToolAgentSessionOptions) { o.Runtime = nil }},
		{"missing store", func(o *harness.ToolAgentSessionOptions) { o.Store = nil }},
		{"missing model", func(o *harness.ToolAgentSessionOptions) { o.Model = nil }},
		{"unknown window", func(o *harness.ToolAgentSessionOptions) { o.ResolveModelWindow = nil }},
		{"preselected tools", func(o *harness.ToolAgentSessionOptions) { o.Session.Config.ToolKeys = []string{"unregistered"} }},
		{"preselected policies", func(o *harness.ToolAgentSessionOptions) {
			o.Session.Config.ToolPolicies = []harness.ToolPolicySnapshot{{Key: "other"}}
		}},
		{"skills", func(o *harness.ToolAgentSessionOptions) {
			o.Session.Config.Skills = []harness.SkillSnapshot{{ID: "unloaded"}}
		}},
		{"memory", func(o *harness.ToolAgentSessionOptions) { o.Session.Config.MemoryPolicy = "external" }},
		{"shared budget", func(o *harness.ToolAgentSessionOptions) { o.Session.Config.SharedBudget = &budget.Limits{} }},
		{"automatic summary", func(o *harness.ToolAgentSessionOptions) {
			o.Session.Config.WorkingStateSummary = &agent.WorkingStateSummaryPolicy{MaxInputTokens: 8192, MaxSerializedBytes: 65536}
		}},
		{"negative output ceiling", func(o *harness.ToolAgentSessionOptions) { o.Session.Config.MaxModelOutputTokens = -1 }},
		{"delegation", func(o *harness.ToolAgentSessionOptions) { o.Session.Config.DelegationPolicy.MaxDepth = 2 }},
		{"sealed config", func(o *harness.ToolAgentSessionOptions) { o.Session.Config.ContentHash = "already-sealed" }},
		{"approval version", func(o *harness.ToolAgentSessionOptions) { o.Session.Config.ApprovalPolicyVersion = 1 }},
		{"approval needed", func(o *harness.ToolAgentSessionOptions) { o.Tools[0].Policy.ApprovalMode = "always" }},
		{"approval capability", func(o *harness.ToolAgentSessionOptions) { o.Tools[0].Policy.ApprovalCapability = "per_call" }},
		{"hosted tool", func(o *harness.ToolAgentSessionOptions) { o.Tools[0].Policy.ExecutionMode = "provider_hosted" }},
		{"unknown effect", func(o *harness.ToolAgentSessionOptions) { o.Tools[0].Policy.SideEffectLevel = "" }},
		{"mismatched key", func(o *harness.ToolAgentSessionOptions) { o.Tools[0].Policy.Key = "other" }},
		{"missing version", func(o *harness.ToolAgentSessionOptions) { o.Tools[0].Policy.DefinitionVersion = "" }},
		{"reserved reader", func(o *harness.ToolAgentSessionOptions) {
			o.Tools[0].Registration.Definition.Key = harness.ContextArtifactToolKey
		}},
		{"reserved delegate", func(o *harness.ToolAgentSessionOptions) {
			o.Tools[0].Registration.Definition.Key = harness.DelegationToolKey
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			options := toolSessionOptions(t, client)
			tc.edit(&options)
			if _, err := harness.OpenToolAgentSession(options); !errors.Is(err, harness.ErrInvalidRequest) {
				t.Fatalf("expected fail-closed: %v", err)
			}
		})
	}
	if len(client.requests) != 0 {
		t.Fatal("invalid composition called the model")
	}
}

func TestToolAgentSessionDoesNotInventModelCapacity(t *testing.T) {
	client := &textSessionModel{}
	options := toolSessionOptions(t, client)
	options.ResolveModelWindow = func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
		return runtimecontext.ModelWindow{}, errors.New("Host has not verified this model")
	}
	session, err := harness.OpenToolAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.Prompt(t.Context(), "unknown-window", "Find evidence"); err == nil || len(client.requests) != 0 {
		t.Fatalf("unverified model reached dispatch: err=%v calls=%d", err, len(client.requests))
	}
}

func TestToolAgentSessionArchivesAndRetrievesExactEvidence(t *testing.T) {
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
	options.Tools[0].Registration.Handler = tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
		content, _ := json.Marshal(map[string]string{"dossier": strings.Repeat("padding ", 2000) + "TRUTH=42;" + strings.Repeat("padding ", 2000)})
		return tools.ExecutionResult{Content: content, Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}, nil
	})
	session, err := harness.OpenToolAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.Prompt(t.Context(), "archive-turn", "Find the TRUTH value in the dossier")
	if err != nil || result.Turn.Status != harness.TurnCompleted || modelCalls != 3 || result.Turn.ContextRef.Generation < 2 {
		t.Fatalf("archive task failed: status=%s err=%v models=%d generation=%d", result.Turn.Status, err, modelCalls, result.Turn.ContextRef.Generation)
	}
}

func TestToolAgentSessionAppliesQueuedUserInputWithoutAnotherHostTurn(t *testing.T) {
	queue, err := agent.NewMemoryRunInputs("tool-session-inputs")
	if err != nil {
		t.Fatal(err)
	}
	var options harness.ToolAgentSessionOptions
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
	session, err := harness.OpenToolAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.Prompt(t.Context(), "input-turn", "Summarize the first source")
	if err != nil || result.Turn.Status != harness.TurnCompleted || calls != 2 || historyReads != 1 ||
		result.Output == nil || string(result.Output.Content) != `"second source summary"` {
		t.Fatalf("queued session input failed: status=%s err=%v calls=%d historyReads=%d", result.Turn.Status, err, calls, historyReads)
	}
}
