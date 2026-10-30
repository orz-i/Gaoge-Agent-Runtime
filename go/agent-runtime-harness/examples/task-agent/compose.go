package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const (
	leaseDuration = 2 * time.Minute
	modelTimeout  = time.Minute
	toolTimeout   = 10 * time.Second
	sampleTimeout = 5 * time.Minute
	maxModelCalls = 12
	maxToolCalls  = 10
)

func taskLimits() agent.Limits {
	return agent.Limits{MaxLLMCalls: maxModelCalls, MaxToolCalls: maxToolCalls}
}

func taskToolPolicies() []harness.ToolPolicySnapshot {
	return []harness.ToolPolicySnapshot{
		{Key: lookupKey, DefinitionVersion: "task-v1", ApprovalMode: "never", SideEffectLevel: "read"},
		harness.ContextArtifactToolPolicySnapshot(),
		{Key: draftKey, DefinitionVersion: "task-v1", ApprovalMode: "never", SideEffectLevel: "write", IdempotencyMode: "none"},
	}
}

func taskContextPolicy() runtimecontext.Policy {
	return runtimecontext.NormalizePolicy(runtimecontext.Policy{
		MaxInputTokens: serviceInputTokens, PreserveRecentTurns: 1, MinimumRecentTurns: 1,
		MaxCompactionTokens: 80, MaxToolResultBytes: 512,
	})
}

// compose is deliberately local to this example: the host chooses stores,
// authority, real model limits, preparation order and execution ownership.
func compose(store kernel.Store, state *taskState, client model.Client, config settings, owner string, transcript harness.TranscriptSource) (*harness.SessionClient, error) {
	rt, err := kernel.New(kernel.Dependencies{Store: store, Clock: state.clock})
	if err != nil {
		return nil, err
	}
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	reader := harness.ContextArtifactToolRegistration(harness.NewContextArtifactToolHandler(state.store, nil))
	handler := reader.Handler
	reader.Handler = tools.HandlerFunc(func(ctx context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
		result, readErr := handler.Execute(ctx, request)
		if readErr == nil {
			var page artifactPage
			if decodeErr := json.Unmarshal(result.Content, &page); decodeErr != nil {
				return tools.ExecutionResult{}, decodeErr
			}
			state.mu.Lock()
			state.reads = append(state.reads, page)
			state.mu.Unlock()
		}
		return result, readErr
	})
	registrations := append(state.registrations(), reader)
	for index := range registrations {
		registrations[index].Handler = recordingHandler{state: state, handler: registrations[index].Handler}
	}
	registry, err := tools.NewRegistry(registrations)
	if err != nil {
		return nil, err
	}
	managed, err := harness.NewManagedContextPreparer(state.store, manager, func(_ context.Context, request model.Request) (runtimecontext.ModelWindow, error) {
		if request.Model != config.routedModel() {
			return runtimecontext.ModelWindow{}, fmt.Errorf("unknown model window: %s", request.Model)
		}
		// Live capacity comes from the host's explicit model configuration.
		// MaxInputTokens below is a separate, intentionally small service cap.
		return runtimecontext.ModelWindow{
			ContextTokens: config.contextTokens, ReservedOutputTokens: config.outputTokens,
			TokenCountContext: runtimecontext.TokenCountContext{Protocol: config.provider, Model: config.modelID},
		}, nil
	})
	if err != nil {
		return nil, err
	}
	rehydrate, err := harness.NewContextRunMiddleware(state.store, nil)
	if err != nil {
		return nil, err
	}
	direct, err := agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: client, Catalog: registry, Executor: registry, Clock: state.clock,
		// Add any host schema projections BEFORE managed, which must be last.
		PrepareModelRequest: agent.ChainModelRequestPreparers(managed),
		RunMiddleware:       []plugin.RunMiddleware{rehydrate},
		ModelMiddleware:     []plugin.ModelMiddleware{outputCeiling{tokens: config.outputTokens, state: state}},
		Limits:              taskLimits(),
		RunExecution:        &agent.RunExecution{OwnerID: owner, LeaseDuration: leaseDuration},
		Execution: agent.ExecutionPolicy{
			Model: agent.CallPolicy{Timeout: modelTimeout, MaxAttempts: 1, MaxConcurrency: 1},
			Tool:  agent.CallPolicy{Timeout: toolTimeout, MaxAttempts: 1, MaxConcurrency: 1},
		},
	})
	if err != nil {
		return nil, err
	}
	host, err := harness.NewRunner(harness.Dependencies{
		Runtime: rt, Agent: direct, Store: state.store, Clock: state.clock, Context: manager, Catalog: registry,
	})
	if err != nil {
		return nil, err
	}
	return host.OpenSession(harness.SessionOptions{
		HostThread: harness.HostRef{Kind: "conversation", ID: "task-agent/local/" + state.sampleID},
		Actor:      kernel.ActorRef{TenantID: "local", ActorID: "operator"},
		Thread:     kernel.ThreadRef{Kind: "conversation", ID: "task-agent/local/" + state.sampleID},
		Transcript: transcript,
		Config: harness.ConfigSnapshot{
			Model:          config.routedModel(),
			Instructions:   taskInstructions,
			OutputContract: &model.OutputContract{Name: "task_answer", Schema: answerSchema},
			ToolKeys:       []string{lookupKey, harness.ContextArtifactToolKey, draftKey},
			ToolPolicies:   taskToolPolicies(),
			ContextPolicy:  taskContextPolicy(),
			Limits:         taskLimits(),
		},
	})
}

type outputCeiling struct {
	tokens int64
	state  *taskState
}

func (outputCeiling) Name() string { return "task-agent.output-ceiling" }

func (ceiling outputCeiling) Model(ctx context.Context, request model.Request, sink model.StreamSink, next plugin.ModelNext) (model.Response, error) {
	// Reservation alone is not a provider output limit. This permitted
	// dispatch-time restriction enforces the same ceiling at the adapter.
	if request.MaxOutputTokens == 0 || request.MaxOutputTokens > ceiling.tokens {
		request.MaxOutputTokens = ceiling.tokens
	}
	started := time.Now()
	response, err := next(ctx, request, sink)
	// An ownership rejection before physical dispatch is not a model call.
	if !agent.IsModelPreDispatchDenied(err) {
		ceiling.state.mu.Lock()
		ceiling.state.requests = append(ceiling.state.requests, model.CloneRequest(request))
		ceiling.state.observations = append(ceiling.state.observations, modelObservation{
			DurationMs: time.Since(started).Milliseconds(), Usage: numericUsage(response.Usage),
		})
		ceiling.state.mu.Unlock()
	}
	return response, err
}

func initialTranscript(_ context.Context, _ harness.HostRef, turn harness.HostRef, prompt string) (*harness.ContextSeed, error) {
	sourceID := turn.ID + "/user"
	return &harness.ContextSeed{
		SourcePath: []string{sourceID},
		Entries: []runtimecontext.Entry{{
			ID: sourceID, SourceID: sourceID, TurnID: turn.ID, Required: true,
			Message: model.Message{Role: model.RoleUser, Content: model.TextParts(prompt)},
		}},
	}, nil
}
