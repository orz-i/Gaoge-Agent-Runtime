package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type agentSessionModel struct {
	requests []model.Request
	generate func(model.Request) (model.Response, error)
}

func (m *agentSessionModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	m.requests = append(m.requests, model.CloneRequest(request))
	if m.generate != nil {
		return m.generate(request)
	}
	return model.Response{Content: "answer"}, nil
}

func agentSessionOptions(t *testing.T, runStore kernel.Store, store harness.Store, client model.Client) harness.AgentSessionOptions {
	t.Helper()
	rt, err := kernel.New(kernel.Dependencies{Store: runStore})
	if err != nil {
		t.Fatal(err)
	}
	return harness.AgentSessionOptions{
		Runtime: rt, Store: store, Model: client,
		Session: harness.SessionOptions{
			HostThread: harness.HostRef{Kind: "conversation", ID: "tenant/tool-session"},
			Actor:      kernel.ActorRef{TenantID: "tenant", ActorID: "operator"},
			Thread:     kernel.ThreadRef{Kind: "conversation", ID: "tenant/tool-session"},
			Config:     harness.ConfigSnapshot{Model: "fixture", Instructions: "answer accurately", MaxModelOutputTokens: 128},
			Transcript: harness.TranscriptHistoryFunc(func(context.Context, harness.HostRef, harness.HostRef) ([]runtimecontext.Entry, error) {
				return nil, nil
			}),
		},
		Context: harness.AgentSessionContextOptions{ResolveWindow: func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
			return runtimecontext.ModelWindow{ContextTokens: 16384, ReservedOutputTokens: 128}, nil
		}},
	}
}

func agentSessionTool(key string, calls *int) tools.Registration {
	return tools.Registration{
		Definition: tools.Definition{Key: key, Name: key, InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`)},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			*calls++
			return tools.ExecutionResult{Content: json.RawMessage(`{"evidence":"verified"}`), Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "read"}}, nil
		}),
	}
}

func addAgentSessionTool(options *harness.AgentSessionOptions, key string, calls *int) {
	options.LocalTools = append(options.LocalTools, harness.SessionTool{
		Registration: agentSessionTool(key, calls),
		Policy:       harness.ToolPolicySnapshot{Key: key, DefinitionVersion: "fixture-v1", ApprovalMode: "never", SideEffectLevel: "read"},
	})
}

func TestAgentSessionMatchesExplicitCompositionAndAuthorizesOnlySelectedTools(t *testing.T) {
	var original []model.Request
	var originalIDs [6]string
	for _, name := range []string{"explicit", "session"} {
		t.Run(name, func(t *testing.T) {
			toolCalls, unselectedCalls, historyCalls, windowCalls := 0, 0, 0, 0
			client := &agentSessionModel{generate: func(request model.Request) (model.Response, error) {
				if request.MaxOutputTokens != 128 || len(request.Tools) != 1 || request.Tools[0].Key != "lookup" {
					t.Fatalf("selection or output ceiling lost: %+v", request)
				}
				for _, message := range request.Messages {
					if message.Role == model.RoleTool {
						return model.Response{Content: "answer"}, nil
					}
				}
				return model.Response{ToolCalls: []tools.Call{{ID: "lookup-call", ToolKey: "lookup", Arguments: json.RawMessage(`{}`)}}}, nil
			}}
			options := agentSessionOptions(t, memory.NewStore(), harness.NewMemoryStore(), client)
			addAgentSessionTool(&options, "lookup", &toolCalls)
			options.Session.Transcript = harness.TranscriptHistoryFunc(func(context.Context, harness.HostRef, harness.HostRef) ([]runtimecontext.Entry, error) {
				historyCalls++
				return nil, nil
			})
			options.Context.ResolveWindow = func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
				windowCalls++
				return runtimecontext.ModelWindow{ContextTokens: 16384, ReservedOutputTokens: 128}, nil
			}
			var session *harness.SessionClient
			var err error
			if name == "session" {
				session, err = harness.OpenAgentSession(options)
			} else {
				selected := options.LocalTools[0]
				options.Session.Config.ToolKeys = []string{selected.Registration.Definition.Key}
				options.Session.Config.ToolPolicies = []harness.ToolPolicySnapshot{selected.Policy}
				registry, e := tools.NewRegistry([]tools.Registration{selected.Registration, agentSessionTool("unselected", &unselectedCalls)})
				if e != nil {
					t.Fatal(e)
				}
				manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
				bounded, e := harness.NewBoundedContextAdapter(options.Store, manager, options.Context.ResolveWindow, nil)
				if e != nil {
					t.Fatal(e)
				}
				approval, e := harness.NewFrozenApprovalPolicy(options.Store)
				if e != nil {
					t.Fatal(e)
				}
				loop, e := agent.NewRunner(agent.Dependencies{Runtime: options.Runtime, Model: client, Catalog: registry, Executor: registry,
					PrepareModelRequest: bounded.Prepare, RunMiddleware: []plugin.RunMiddleware{bounded}, ApprovalPolicies: []plugin.ApprovalPolicy{approval}})
				if e != nil {
					t.Fatal(e)
				}
				host, e := harness.NewRunner(harness.Dependencies{Runtime: options.Runtime, Agent: loop, Store: options.Store, Context: manager, Catalog: registry})
				if e != nil {
					t.Fatal(e)
				}
				session, err = host.OpenSession(options.Session)
			}
			if err != nil {
				t.Fatal(err)
			}
			if historyCalls != 0 || windowCalls != 0 || len(client.requests) != 0 || toolCalls != 0 {
				t.Fatal("constructor executed history, window, model or Tool callbacks")
			}
			result, err := session.Prompt(t.Context(), "first", "find the evidence")
			if err != nil || result.Turn.Status != harness.TurnCompleted || len(client.requests) != 2 || toolCalls != 1 || unselectedCalls != 0 || windowCalls != 2 || historyCalls != 1 {
				t.Fatalf("tool turn: status=%s err=%v models=%d tools=%d unselected=%d windows=%d history=%d", result.Turn.Status, err, len(client.requests), toolCalls, unselectedCalls, windowCalls, historyCalls)
			}
			if _, err = session.Prompt(t.Context(), "first", "find the evidence"); err != nil || len(client.requests) != 2 || toolCalls != 1 || windowCalls != 2 {
				t.Fatalf("replay redispatched: %v models=%d tools=%d", err, len(client.requests), toolCalls)
			}
			root, ok := harness.TopLevelInvocation(result)
			if !ok {
				t.Fatal("missing root execution")
			}
			ids := [6]string{result.Session.ID, result.Turn.ID, result.Config.ID, result.Turn.ContextCheckpointID, root.ID, root.ExecutionRefID}
			if name == "explicit" {
				original = client.requests
				originalIDs = ids
			} else if !reflect.DeepEqual(client.requests, original) || ids != originalIDs {
				t.Fatalf("facade changed explicit requests or durable identities: got %v, want %v", ids, originalIDs)
			}
		})
	}
}

func TestAgentSessionPairedSelectionRejectsConflictingToolAuthority(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*harness.AgentSessionOptions)
	}{
		{"preselected keys", func(o *harness.AgentSessionOptions) { o.Session.Config.ToolKeys = []string{"lookup"} }},
		{"preselected policies", func(o *harness.AgentSessionOptions) {
			o.Session.Config.ToolPolicies = []harness.ToolPolicySnapshot{o.LocalTools[0].Policy}
		}},
		{"duplicate grants", func(o *harness.AgentSessionOptions) { o.LocalTools = append(o.LocalTools, o.LocalTools[0]) }},
		{"foreign policy", func(o *harness.AgentSessionOptions) { o.LocalTools[0].Policy.Key = "other" }},
		{"unclassified tool", func(o *harness.AgentSessionOptions) { o.LocalTools[0].Policy.SideEffectLevel = "" }},
		{"approval required", func(o *harness.AgentSessionOptions) { o.LocalTools[0].Policy.ApprovalMode = "always" }},
		{"approval capability", func(o *harness.AgentSessionOptions) { o.LocalTools[0].Policy.ApprovalCapability = "per_call" }},
		{"reserved delegate", func(o *harness.AgentSessionOptions) {
			o.LocalTools[0].Registration.Definition.Key = harness.DelegationToolKey
		}},
		{"custom archive reader", func(o *harness.AgentSessionOptions) {
			o.LocalTools[0].Registration.Definition.Key = harness.ContextArtifactToolKey
		}},
		{"inputs with managed recovery", func(o *harness.AgentSessionOptions) { o.Context.ManagedRecovery = true }},
		{"inputs with native summary recovery", func(o *harness.AgentSessionOptions) { o.Context.SummaryNativeRecovery = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &agentSessionModel{}
			store := harness.NewMemoryStore()
			options := agentSessionOptions(t, memory.NewStore(), store, client)
			bound, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), ManagedRecoveryHost: store})
			if err != nil {
				t.Fatal(err)
			}
			options.Runtime = bound
			options.Inputs = struct{ agent.RunInputSource }{} // Any BindingID/queue access would panic.
			options.LocalTools = []harness.SessionTool{{Registration: agentSessionTool("lookup", &calls), Policy: harness.ToolPolicySnapshot{
				DefinitionVersion: "v1", ApprovalMode: "never", SideEffectLevel: "read",
			}}}
			tc.edit(&options)
			if _, err := harness.OpenAgentSession(options); !errors.Is(err, harness.ErrInvalidRequest) || len(client.requests) != 0 || calls != 0 {
				t.Fatalf("invalid selection granted authority or called collaborators: %v", err)
			}
		})
	}
}

func TestAgentSessionRejectsUncomposedOrUnselectedConfiguration(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*harness.AgentSessionOptions)
	}{
		{"missing runtime", func(o *harness.AgentSessionOptions) { o.Runtime = nil }},
		{"missing store", func(o *harness.AgentSessionOptions) { o.Store = nil }},
		{"missing model", func(o *harness.AgentSessionOptions) { o.Model = nil }},
		{"missing window resolver", func(o *harness.AgentSessionOptions) { o.Context.ResolveWindow = nil }},
		{"missing output ceiling", func(o *harness.AgentSessionOptions) { o.Session.Config.MaxModelOutputTokens = 0 }},
		{"preselected keys without local tools", func(o *harness.AgentSessionOptions) {
			o.LocalTools = nil
			o.Session.Config.ToolKeys = []string{"lookup"}
		}},
		{"preselected policies without local tools", func(o *harness.AgentSessionOptions) {
			o.Session.Config.ToolPolicies = []harness.ToolPolicySnapshot{o.LocalTools[0].Policy}
			o.LocalTools = nil
		}},
		{"missing policy", func(o *harness.AgentSessionOptions) { o.LocalTools[0].Policy = harness.ToolPolicySnapshot{} }},
		{"unknown version", func(o *harness.AgentSessionOptions) { o.LocalTools[0].Policy.DefinitionVersion = "" }},
		{"approval needed", func(o *harness.AgentSessionOptions) { o.LocalTools[0].Policy.ApprovalMode = "always" }},
		{"unknown approval", func(o *harness.AgentSessionOptions) { o.LocalTools[0].Policy.ApprovalMode = "unknown" }},
		{"provider hosted tool", func(o *harness.AgentSessionOptions) {
			o.LocalTools[0].Policy.ExecutionMode = "provider_hosted"
		}},
		{"unknown execution mode", func(o *harness.AgentSessionOptions) { o.LocalTools[0].Policy.ExecutionMode = "unknown" }},
		{"foreign policy", func(o *harness.AgentSessionOptions) { o.LocalTools[0].Policy.Key = "foreign" }},
		{"skills", func(o *harness.AgentSessionOptions) { o.Session.Config.Skills = []harness.SkillSnapshot{{ID: "skill"}} }},
		{"shared budget and managed recovery", func(o *harness.AgentSessionOptions) {
			o.Session.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 1}
			o.Context.ManagedRecovery = true
		}},
		{"delegation", func(o *harness.AgentSessionOptions) { o.Session.Config.DelegationPolicy.MaxDepth = 1 }},
		{"override reader", func(o *harness.AgentSessionOptions) {
			o.LocalTools[0].Registration.Definition.Key = harness.ContextArtifactToolKey
		}},
		{"invalid schema", func(o *harness.AgentSessionOptions) {
			o.LocalTools[0].Registration.Definition.InputSchema = json.RawMessage(`{"type":"unknown"}`)
		}},
		{"missing handler", func(o *harness.AgentSessionOptions) { o.LocalTools[0].Registration.Handler = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &agentSessionModel{}
			options := agentSessionOptions(t, memory.NewStore(), harness.NewMemoryStore(), client)
			addAgentSessionTool(&options, "lookup", &calls)
			options.Session.Transcript = harness.TranscriptSourceFunc(func(context.Context, harness.HostRef, harness.HostRef, string) (*harness.ContextSeed, error) {
				t.Fatal("constructor read history")
				return nil, nil
			})
			options.Context.ResolveWindow = func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
				t.Fatal("constructor resolved window")
				return runtimecontext.ModelWindow{}, nil
			}
			tc.mutate(&options)
			if _, err := harness.OpenAgentSession(options); !errors.Is(err, harness.ErrInvalidRequest) || len(client.requests) != 0 || calls != 0 {
				t.Fatalf("invalid config accepted or executed: %v", err)
			}
		})
	}
}

func TestAgentSessionChecksModelWindowBeforeGeneration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		window runtimecontext.ModelWindow
		cause  error
		want   error
	}{
		{"insufficient reservation", runtimecontext.ModelWindow{ContextTokens: 8192, ReservedOutputTokens: 127}, nil, harness.ErrInvalidRequest},
		{"unknown capacity", runtimecontext.ModelWindow{ReservedOutputTokens: 128}, nil, runtimecontext.ErrModelWindowUnknown},
		{"resolver error", runtimecontext.ModelWindow{}, runtimecontext.ErrModelWindowUnknown, runtimecontext.ErrModelWindowUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &agentSessionModel{}
			options := agentSessionOptions(t, memory.NewStore(), harness.NewMemoryStore(), client)
			options.Context.ResolveWindow = func(context.Context, model.Request) (runtimecontext.ModelWindow, error) { return tc.window, tc.cause }
			session, err := harness.OpenAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = session.Prompt(t.Context(), "first", "question"); !errors.Is(err, tc.want) || len(client.requests) != 0 {
				t.Fatalf("window denial: %v calls=%d", err, len(client.requests))
			}
		})
	}
}

func TestAgentSessionArchiveReaderCompactsAndReadsExactEvidence(t *testing.T) {
	store := harness.NewMemoryStore()
	old := strings.Repeat("old evidence ", 900)
	const exactFact = `"id":9007199254740993`
	client := &agentSessionModel{generate: func(request model.Request) (model.Response, error) {
		for _, message := range request.Messages {
			if message.Role == model.RoleTool {
				if !strings.Contains(model.TextValue(message.Content), `\"id\":9007199254740993`) {
					t.Fatalf("reader lost original evidence: %+v", message)
				}
				return model.Response{Content: "verified"}, nil
			}
		}
		for _, message := range request.Messages {
			match := regexp.MustCompile(`artifact_id=([^\s>]+)`).FindStringSubmatch(model.TextValue(message.Content))
			if len(match) == 2 {
				args, _ := json.Marshal(map[string]string{"artifactID": match[1], "query": exactFact})
				return model.Response{ToolCalls: []tools.Call{{ID: "archive-read", ToolKey: harness.ContextArtifactToolKey, Arguments: args}}}, nil
			}
		}
		t.Fatal("model did not receive an archive marker")
		return model.Response{}, nil
	}}
	options := agentSessionOptions(t, memory.NewStore(), store, client)
	options.Context.ReadArtifacts = true
	options.Session.Config.ContextPolicy = runtimecontext.Policy{MaxInputTokens: 5000, PreserveRecentTurns: 1, MinimumRecentTurns: 1, MaxCompactionTokens: 80}
	options.Session.Transcript = harness.TranscriptSourceFunc(func(_ context.Context, _, turn harness.HostRef, prompt string) (*harness.ContextSeed, error) {
		assistant := contextEntry("old-answer", "old", model.RoleAssistant, old, false)
		assistant.Message.ToolCalls = []tools.Call{{ID: "historic-call", ToolKey: "lookup", Arguments: json.RawMessage(`{` + exactFact + `}`)}}
		historic := contextEntry("old-tool", "old", model.RoleTool, "found", false)
		historic.Message.ToolCallID = "historic-call"
		current := contextEntry("current", turn.ID, model.RoleUser, prompt, true)
		return &harness.ContextSeed{SourcePath: []string{"old-user", "old-answer", "old-tool", "current"}, Entries: []runtimecontext.Entry{
			contextEntry("old-user", "old", model.RoleUser, "previous question", false), assistant, historic, current,
		}}, nil
	})
	session, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	done, err := session.Prompt(t.Context(), "first", "find the exact fact")
	if err != nil || done.Turn.Status != harness.TurnCompleted || done.Turn.ContextRef.Generation < 2 || len(client.requests) != 2 {
		t.Fatalf("archive task: status=%s err=%v generation=%d calls=%d", done.Turn.Status, err, done.Turn.ContextRef.Generation, len(client.requests))
	}
	checkpoint, err := store.GetContextCheckpoint(t.Context(), done.Turn.ContextCheckpointID)
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoint.ArtifactIDs) == 0 {
		t.Fatal("no durable archive")
	}
	for _, message := range client.requests[0].Messages {
		if strings.Contains(model.TextValue(message.Content), old) {
			t.Fatal("oversized archived history was re-expanded")
		}
	}
	options.Session.Transcript = nil
	reopened, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.Load(t.Context(), "first"); err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.Resume(t.Context(), "first"); err != nil || len(client.requests) != 2 {
		t.Fatalf("terminal reopening dispatched: %v", err)
	}
}

func TestAgentSessionManagedRecoveryRequiresExplicitMatchingStores(t *testing.T) {
	store := harness.NewMemoryStore()
	options := agentSessionOptions(t, memory.NewStore(), store, &agentSessionModel{})
	options.Context.ManagedRecovery = true
	if _, err := harness.OpenAgentSession(options); !errors.Is(err, harness.ErrInvalidRequest) {
		t.Fatalf("unbound managed Host accepted: %v", err)
	}
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), ManagedRecoveryHost: store})
	if err != nil {
		t.Fatal(err)
	}
	options.Runtime = runtime
	session, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatalf("matched managed stores rejected: %v", err)
	}
	if done, startErr := session.Prompt(t.Context(), "managed-first", "question"); startErr != nil || done.Turn.Status != harness.TurnCompleted {
		t.Fatalf("explicit managed composition failed: status=%s err=%v", done.Turn.Status, startErr)
	}
	options.Store = harness.NewMemoryStore()
	if _, err = harness.OpenAgentSession(options); !errors.Is(err, harness.ErrInvalidRequest) {
		t.Fatalf("foreign managed Host accepted: %v", err)
	}
}

func TestAgentSessionWithoutArchiveAuthorizationRejectsOversizedHistory(t *testing.T) {
	client := &agentSessionModel{}
	options := agentSessionOptions(t, memory.NewStore(), harness.NewMemoryStore(), client)
	options.Session.Config.ContextPolicy = runtimecontext.Policy{MaxInputTokens: 2000, PreserveRecentTurns: 1, MinimumRecentTurns: 1}
	options.Session.Transcript = harness.TranscriptHistoryFunc(func(context.Context, harness.HostRef, harness.HostRef) ([]runtimecontext.Entry, error) {
		return []runtimecontext.Entry{
			contextEntry("old-user", "old", model.RoleUser, "previous question", false),
			contextEntry("old-answer", "old", model.RoleAssistant, strings.Repeat("private old evidence ", 800), false),
		}, nil
	})
	session, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	_, err = session.Prompt(t.Context(), "first", "current question")
	var pressure *agent.BoundedContextBudgetError
	if !errors.Is(err, runtimecontext.ErrBudgetExceeded) || !errors.As(err, &pressure) || pressure.Reason != "no_artifact_reader" || len(client.requests) != 0 {
		t.Fatalf("history silently compacted without an authorized reader: err=%v pressure=%+v models=%d", err, pressure, len(client.requests))
	}
}

func TestAgentSessionConstructionDoesNotAccessStoreOrCallbacks(t *testing.T) {
	// A nil embedded Store panics on every promoted I/O method. Constructing
	// this facade must not need any of those methods, even for read-only opens.
	store := struct{ harness.Store }{}
	options := agentSessionOptions(t, memory.NewStore(), store, &agentSessionModel{})
	options.Session.Transcript = harness.TranscriptSourceFunc(func(context.Context, harness.HostRef, harness.HostRef, string) (*harness.ContextSeed, error) {
		t.Fatal("constructor read Host history")
		return nil, nil
	})
	options.Context.ResolveWindow = func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
		t.Fatal("constructor resolved a model request")
		return runtimecontext.ModelWindow{}, nil
	}
	options.Context.ReadArtifacts = true
	if _, err := harness.OpenAgentSession(options); err != nil {
		t.Fatal(err)
	}
}

func TestAgentSessionFreezesPairedRegistrationsAndContinuesHostHistory(t *testing.T) {
	runStore, store := memory.NewStore(), harness.NewMemoryStore()
	client := &agentSessionModel{}
	options := agentSessionOptions(t, runStore, store, client)
	calls := 0
	options.LocalTools = []harness.SessionTool{{Registration: agentSessionTool("lookup", &calls), Policy: harness.ToolPolicySnapshot{
		DefinitionVersion: "v1", ApprovalMode: "never", SideEffectLevel: "read",
	}}}
	history := []runtimecontext.Entry{}
	options.Session.Transcript = harness.TranscriptHistoryFunc(func(context.Context, harness.HostRef, harness.HostRef) ([]runtimecontext.Entry, error) {
		return history, nil
	})
	session, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(options.Session.Config.ToolKeys) != 0 || len(options.Session.Config.ToolPolicies) != 0 || options.LocalTools[0].Policy.Key != "" {
		t.Fatal("constructor added private config or policy metadata to Host input")
	}
	options.LocalTools[0].Registration.Definition.InputSchema[0] = '!'
	options.LocalTools[0].Registration.Definition.Name = "changed"
	options.LocalTools[0].Policy.ApprovalMode = "always"
	options.Session.Config.MaxModelOutputTokens = 512
	first, err := session.Prompt(t.Context(), "first", "first prompt")
	if err != nil || first.Turn.Status != harness.TurnCompleted || len(client.requests) != 1 || client.requests[0].MaxOutputTokens != 128 || client.requests[0].Tools[0].Name != "lookup" || string(client.requests[0].Tools[0].InputSchema) != `{"type":"object","additionalProperties":false}` {
		t.Fatalf("caller mutation changed composition: status=%s err=%v requests=%+v", first.Turn.Status, err, client.requests)
	}
	history = []runtimecontext.Entry{
		contextEntry("first-user", "first", model.RoleUser, "first prompt", false),
		contextEntry("first-answer", "first", model.RoleAssistant, "answer", false),
	}
	second, err := session.Prompt(t.Context(), "second", "second prompt")
	if err != nil || second.Turn.Status != harness.TurnCompleted || len(client.requests) != 2 || len(client.requests[1].Messages) != 4 || model.TextValue(client.requests[1].Messages[2].Content) != "answer" || model.TextValue(client.requests[1].Messages[3].Content) != "second prompt" {
		t.Fatalf("next Turn lost Host history: status=%s err=%v requests=%+v", second.Turn.Status, err, client.requests)
	}
	history = nil // Replay the original ancestry with a changed current input.
	if _, err = session.Prompt(t.Context(), "first", "changed prompt"); !errors.Is(err, harness.ErrConflict) || len(client.requests) != 2 || calls != 0 {
		t.Fatalf("changed admitted Turn dispatched: %v models=%d tools=%d", err, len(client.requests), calls)
	}
}

func TestAgentSessionReopensFrozenIntentWithoutTranscriptOrPreparation(t *testing.T) {
	base, store := memory.NewStore(), harness.NewMemoryStore()
	fault := &resumeTurnFaultStore{Store: base}
	firstClient := &agentSessionModel{}
	options := agentSessionOptions(t, fault, store, firstClient)
	first, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = first.Prompt(t.Context(), "first", "original question"); !errors.Is(err, errResumeTurnInterrupted) || len(firstClient.requests) != 0 {
		t.Fatalf("fixture did not stop at committed intent: err=%v calls=%d", err, len(firstClient.requests))
	}
	pending, err := first.Load(t.Context(), "first")
	if err != nil {
		t.Fatal(err)
	}
	root, ok := harness.TopLevelInvocation(pending)
	if !ok {
		t.Fatal("missing original root")
	}
	secondClient := &agentSessionModel{}
	options = agentSessionOptions(t, base, store, secondClient)
	options.Session.Transcript = nil
	options.Session.Config.MaxModelOutputTokens = 512
	options.Session.Config.Instructions = "changed handle configuration"
	options.Context.ResolveWindow = func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
		t.Fatal("committed invocation was prepared a second time")
		return runtimecontext.ModelWindow{}, nil
	}
	reopened, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.Prompt(t.Context(), "new", "requires history"); !errors.Is(err, harness.ErrInvalidRequest) {
		t.Fatalf("missing transcript admitted a new Turn: %v", err)
	}
	done, err := reopened.Resume(t.Context(), "first")
	if err != nil || done.Turn.Status != harness.TurnCompleted || len(secondClient.requests) != 1 || secondClient.requests[0].MaxOutputTokens != 128 {
		t.Fatalf("frozen resume: status=%s err=%v requests=%+v", done.Turn.Status, err, secondClient.requests)
	}
	completedRoot, ok := harness.TopLevelInvocation(done)
	if !ok || completedRoot.ID != root.ID || completedRoot.ExecutionRefID != root.ExecutionRefID {
		t.Fatal("resume changed original Run or Invocation")
	}
	if _, err = reopened.Resume(t.Context(), "first"); err != nil || len(secondClient.requests) != 1 {
		t.Fatalf("terminal Resume dispatched: %v", err)
	}
}

func TestAgentSessionResumeRejectsOriginalUnsupportedCompositionBeforeGeneration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*harness.ConfigSnapshot)
	}{
		{"legacy unbounded output", func(c *harness.ConfigSnapshot) { c.MaxModelOutputTokens = 0 }},
		{"shared budget", func(c *harness.ConfigSnapshot) { c.SharedBudget = &budget.Limits{MaxLLMCalls: 2} }},
		{"hosted execution", func(c *harness.ConfigSnapshot) {
			c.ToolKeys = []string{"lookup"}
			c.ToolPolicies = []harness.ToolPolicySnapshot{{Key: "lookup", DefinitionVersion: "v1", ApprovalMode: "never", ExecutionMode: "provider_hosted"}}
		}},
		{"delegation tool", func(c *harness.ConfigSnapshot) {
			c.ToolKeys = []string{harness.DelegationToolKey}
			c.ToolPolicies = []harness.ToolPolicySnapshot{{Key: harness.DelegationToolKey, DefinitionVersion: "v1", ApprovalMode: "never"}}
		}},
		{"nonstandard reader policy", func(c *harness.ConfigSnapshot) {
			c.ToolKeys = []string{harness.ContextArtifactToolKey}
			policy := harness.ContextArtifactToolPolicySnapshot()
			policy.DefinitionVersion = "host-custom-reader"
			c.ToolPolicies = []harness.ToolPolicySnapshot{policy}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, store := memory.NewStore(), harness.NewMemoryStore()
			fault := &resumeTurnFaultStore{Store: base}
			clock := newResumeTurnClock()
			originalModel := &agentSessionModel{}
			originalOptions := resumeTurnSessionOptions()
			originalOptions.Config.MaxModelOutputTokens = 128
			tc.mutate(&originalOptions.Config)
			toolCalls := 0
			registry, err := tools.NewRegistry([]tools.Registration{
				agentSessionTool("lookup", &toolCalls), agentSessionTool(harness.DelegationToolKey, &toolCalls),
				harness.ContextArtifactToolRegistration(harness.NewContextArtifactToolHandler(store, nil)),
			})
			if err != nil {
				t.Fatal(err)
			}
			var originalHost *harness.Runner
			if originalOptions.Config.SharedBudget == nil {
				originalHost, _ = newResumeTurnHarness(t, fault, store, originalModel, clock, func(d *agent.Dependencies) {
					d.Catalog, d.Executor = registry, registry
				})
			} else {
				// Admit a real budgeted Turn through the original explicit recipe.
				// The injected crash precedes model dispatch, so no Meter is needed.
				rt, err := kernel.New(kernel.Dependencies{Store: fault, Clock: clock})
				if err != nil {
					t.Fatal(err)
				}
				shared, err := harness.NewBudgetAuthority(harness.BudgetAuthorityDependencies{Store: store, Clock: clock})
				if err != nil {
					t.Fatal(err)
				}
				loop, err := agent.NewRunner(agent.Dependencies{
					Runtime: rt, Model: originalModel, Clock: clock, PrepareModelRequest: harness.MaterializeContextWindowRequest,
					Catalog: registry, Executor: registry,
					RunMiddleware: []plugin.RunMiddleware{mustBudgetAdapter(t, shared)}, ModelMiddleware: []plugin.ModelMiddleware{mustBudgetAdapter(t, shared)}, ToolMiddleware: []plugin.ToolMiddleware{mustBudgetAdapter(t, shared)},
				})
				if err != nil {
					t.Fatal(err)
				}
				originalHost, err = harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: loop, Store: store, Clock: clock,
					Context: runtimecontext.NewManager(runtimecontext.Dependencies{}), Catalog: registry, Budget: shared})
				if err != nil {
					t.Fatal(err)
				}
			}
			original, err := originalHost.OpenSession(originalOptions)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = original.Prompt(t.Context(), "original", "original task"); !errors.Is(err, errResumeTurnInterrupted) || len(originalModel.requests) != 0 {
				t.Fatalf("old recipe did not leave its original pending intent: %v models=%d", err, len(originalModel.requests))
			}
			pending, err := original.Load(t.Context(), "original")
			if err != nil {
				t.Fatal(err)
			}
			freshModel := &agentSessionModel{}
			freshOptions := agentSessionOptions(t, base, store, freshModel)
			addAgentSessionTool(&freshOptions, "lookup", &toolCalls)
			freshOptions.Session.Actor = originalOptions.Actor
			freshOptions.Session.Thread = originalOptions.Thread
			freshOptions.Session.HostThread = originalOptions.HostThread
			freshOptions.Session.Transcript = nil
			freshOptions.Context.ResolveWindow = func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
				t.Fatal("unsupported original composition reached preparation")
				return runtimecontext.ModelWindow{}, nil
			}
			reopened, err := harness.OpenAgentSession(freshOptions)
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := reopened.Load(t.Context(), "original")
			if err != nil || loaded.Turn.ID != pending.Turn.ID || loaded.Config.ID != pending.Config.ID {
				t.Fatalf("unsupported original Turn must remain readable: %v", err)
			}
			if _, err = reopened.Refresh(t.Context(), "original"); err != nil || len(freshModel.requests) != 0 {
				t.Fatalf("read-only Refresh failed or dispatched: %v models=%d", err, len(freshModel.requests))
			}
			if _, err = reopened.Resume(t.Context(), "original"); !errors.Is(err, harness.ErrInvalidRequest) || len(freshModel.requests) != 0 || toolCalls != 0 {
				t.Fatalf("simple handle resumed unsupported original config: %v models=%d", err, len(freshModel.requests))
			}
		})
	}
}

func TestAgentSessionResumeRejectsDelegationFactsDespiteOrdinaryDefaultDepth(t *testing.T) {
	for _, kind := range []harness.ItemKind{harness.ItemDelegation, harness.ItemSubtask} {
		t.Run(string(kind), func(t *testing.T) {
			base, store := memory.NewStore(), harness.NewMemoryStore()
			fault := &resumeTurnFaultStore{Store: base}
			options := agentSessionOptions(t, fault, store, &agentSessionModel{})
			first, err := harness.OpenAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = first.Prompt(t.Context(), "first", "original task"); !errors.Is(err, errResumeTurnInterrupted) {
				t.Fatal(err)
			}
			pending, err := first.Load(t.Context(), "first")
			if err != nil {
				t.Fatal(err)
			}
			if pending.Config.DelegationPolicy.MaxDepth <= 0 {
				t.Fatal("fixture did not freeze the ordinary default delegation depth")
			}
			if _, _, err = store.AppendItem(t.Context(), harness.Item{ID: "existing-child-fact", TurnID: pending.Turn.ID, Kind: kind,
				Status: harness.ItemStarted, Payload: json.RawMessage(`{}`), CreatedAt: pending.Turn.CreatedAt, UpdatedAt: pending.Turn.UpdatedAt}); err != nil {
				t.Fatal(err)
			}
			client := &agentSessionModel{}
			options = agentSessionOptions(t, base, store, client)
			options.Session.Transcript = nil
			reopened, err := harness.OpenAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = reopened.Resume(t.Context(), "first"); !errors.Is(err, harness.ErrInvalidRequest) || len(client.requests) != 0 {
				t.Fatalf("existing child evidence was ignored: %v models=%d", err, len(client.requests))
			}
		})
	}
}

type largeNumberSessionCounter struct {
	payload []byte
}

func (counter *largeNumberSessionCounter) Count(_ context.Context, request runtimecontext.TokenCountRequest) (int64, error) {
	counter.payload = request.Payload
	return 17, nil
}

func TestAgentSessionCountsLosslessHistoryOutsideFloat64(t *testing.T) {
	const arguments = `{"limit":1e1000,"id":9007199254740993}`
	const replay = `{"value":-1e999}`
	store := harness.NewMemoryStore()
	modelClient := &agentSessionModel{}
	counter := &largeNumberSessionCounter{}
	options := agentSessionOptions(t, memory.NewStore(), store, modelClient)
	options.Context.Counter = counter
	// The typed request exceeds this byte-based fallback, so proceeding requires the exact count.
	options.Session.Config.ContextPolicy.MaxInputTokens = 25
	options.Context.ResolveWindow = func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
		return runtimecontext.ModelWindow{ContextTokens: 16384, ReservedOutputTokens: 128,
			TokenCountContext: runtimecontext.TokenCountContext{Protocol: "fixture", Model: "fixture"}}, nil
	}
	options.Session.Transcript = harness.TranscriptSourceFunc(func(_ context.Context, _, turn harness.HostRef, prompt string) (*harness.ContextSeed, error) {
		prior := contextEntry("old-user", "old", model.RoleUser, "prior prompt", false)
		assistant := contextEntry("old-answer", "old", model.RoleAssistant, "prior answer", false)
		assistant.Message.ToolCalls = []tools.Call{{ID: "historic", ToolKey: "lookup", Arguments: json.RawMessage(arguments)}}
		assistant.Message.ProviderReplay = &model.ProviderReplay{Provider: "fixture", Format: "opaque", Data: json.RawMessage(replay)}
		result := contextEntry("old-result", "old", model.RoleTool, "tool result", false)
		result.Message.ToolCallID = "historic"
		current := contextEntry("current", turn.ID, model.RoleUser, prompt, true)
		return &harness.ContextSeed{SourcePath: []string{"old-user", "old-answer", "old-result", "current"},
			Entries: []runtimecontext.Entry{prior, assistant, result, current}}, nil
	})
	session, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	done, err := session.Prompt(t.Context(), "first", "question")
	if err != nil || done.Turn.Status != harness.TurnCompleted || len(modelClient.requests) != 1 {
		t.Fatalf("sampling with large JSON numbers: %s, %v", done.Turn.Status, err)
	}
	counted := string(counter.payload)
	if !strings.Contains(counted, "\"arguments\":"+arguments) || !strings.Contains(counted, "\"data\":"+replay) {
		t.Fatalf("exact counter lost a JSON number: %s", counted)
	}
	checkpoint, err := store.GetContextCheckpoint(t.Context(), done.Turn.ContextCheckpointID)
	if err != nil || !runtimecontext.ValidCheckpoint(checkpoint) ||
		checkpoint.ContentHashEncoding != runtimecontext.CheckpointContentHashEncodingLosslessJSON {
		t.Fatalf("sampling did not retain a valid lossless Checkpoint: %v", err)
	}
}
