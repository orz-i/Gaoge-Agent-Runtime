package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	provider "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider"
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

func taskToolPoliciesForSuite(suite string) []harness.ToolPolicySnapshot {
	reader := harness.ContextArtifactToolPolicySnapshot()
	if suite == qualitySuite {
		reader = harness.ContextArtifactCandidateToolPolicySnapshot()
	}
	return []harness.ToolPolicySnapshot{
		{Key: lookupKey, DefinitionVersion: "task-v1", ApprovalMode: "never", SideEffectLevel: "read"},
		reader,
		{Key: draftKey, DefinitionVersion: "task-v1", ApprovalMode: "never", SideEffectLevel: "write", IdempotencyMode: "none"},
	}
}

func taskArtifactRegistrationForSuite(suite string, handler *harness.ContextArtifactToolHandler) tools.Registration {
	if suite == qualitySuite {
		return harness.ContextArtifactCandidateToolRegistration(handler)
	}
	return harness.ContextArtifactToolRegistration(handler)
}

func taskContextPolicy() runtimecontext.Policy {
	return runtimecontext.NormalizePolicy(runtimecontext.Policy{
		MaxInputTokens: serviceInputTokens, PreserveRecentTurns: 1, MinimumRecentTurns: 1,
		MaxCompactionTokens: 80, MaxToolResultBytes: 512,
	})
}

// The evidence-quality workload needs room for physically completed opaque
// reasoning replay. The Context manager's no-tokenizer fallback correctly
// charges >=1 token per serialized byte, so the legacy 8192 service cap can
// reject a valid third request even on models with much larger windows.
// A separate quality policy is tied to its dataset hash; the old two-case
// benchmark retains its exact budget and hash. Never change SDK accounting.
func taskContextPolicyForSuite(suite string) runtimecontext.Policy {
	policy := taskContextPolicy()
	if suite == qualitySuite {
		policy.MaxInputTokens = 32768
	}
	return policy
}

// Observes only the optional native count result. This does not replace the
// Context counter or change its existing Fail-Closed budget fallback.
// Metrics intentionally do not include input content, IDs or error text.
type recordedInputTokenCounter struct {
	next  runtimecontext.TokenCounter
	state *taskState
}

func (counter recordedInputTokenCounter) Count(ctx context.Context, request runtimecontext.TokenCountRequest) (int64, error) {
	started := time.Now()
	count, err := counter.next.Count(ctx, request)
	durationMs := time.Since(started).Milliseconds()
	var countedRequest model.Request
	decodeErr := json.Unmarshal(request.Payload, &countedRequest)
	fingerprint, fingerprintErr := inputCountObservationFingerprint(countedRequest)
	validFingerprint := decodeErr == nil && fingerprintErr == nil
	counter.state.mu.Lock()
	defer counter.state.mu.Unlock()
	observed := &counter.state.inputCounts
	observed.Attempts++
	observed.TotalDurationMs += durationMs
	if durationMs > observed.MaxDurationMs {
		observed.MaxDurationMs = durationMs
	}
	if observed.UnavailableReasons == nil {
		observed.UnavailableReasons = &inputCountUnavailableReasons{}
	}
	if err == nil && count > 0 {
		observed.Accepted++
		if counter.state.acceptedInputCounts == nil {
			counter.state.acceptedInputCounts = make(map[[32]byte]int64)
		}
		if validFingerprint {
			counter.state.acceptedInputCounts[fingerprint] = count
		}
	} else {
		if validFingerprint {
			delete(counter.state.acceptedInputCounts, fingerprint)
		}
		observed.Unavailable++
		switch provider.ClassifyInputCountFailure(err) {
		case provider.InputCountLocalRejected:
			observed.UnavailableReasons.LocalRejected++
		case provider.InputCountHTTP4xx:
			observed.UnavailableReasons.HTTP4xx++
		case provider.InputCountHTTP5xx:
			observed.UnavailableReasons.HTTP5xx++
		case provider.InputCountOtherHTTP:
			observed.UnavailableReasons.OtherHTTP++
		case provider.InputCountTransportOrProtocol:
			observed.UnavailableReasons.TransportOrProtocol++
		case provider.InputCountOther:
			observed.UnavailableReasons.Other++
		default:
			// Unknown future enums are never evidence of a native HTTP
			// response. Preserve the conservative unclassified bucket.
			observed.UnavailableReasons.Other++
		}
	}
	return count, err
}

// compose is deliberately local to this example: the host chooses stores,
// authority, real model limits, preparation order and execution ownership.
func compose(store kernel.Store, state *taskState, client model.Client, config settings, owner string, transcript harness.TranscriptSource) (*harness.SessionClient, error) {
	return composeContextMode(store, state, client, config, owner, transcript, false)
}

// The ordinary mode exists only for the example's opaque-failure regression.
// It is not a fallback or an alternate recovery path in the public example.
func composeContextMode(store kernel.Store, state *taskState, client model.Client, config settings, owner string, transcript harness.TranscriptSource, opaque bool) (*harness.SessionClient, error) {
	rt, err := kernel.New(kernel.Dependencies{Store: store, Clock: state.clock})
	if err != nil {
		return nil, err
	}
	// The optional native count is a read-only, pre-intent capability of the
	// original Host-selected model. It never authorizes a model dispatch.
	// Scope the changed accounting policy to the versioned quality workload;
	// the historical two-case baseline and all scripted paths stay unchanged.
	contextDependencies := runtimecontext.Dependencies{}
	if config.suite == qualitySuite && config.provider == "openai" {
		contextDependencies.Counter = recordedInputTokenCounter{next: provider.ContextTokenCounter{Client: client}, state: state}
	}
	manager := runtimecontext.NewManager(contextDependencies)
	reader := taskArtifactRegistrationForSuite(config.suite, harness.NewContextArtifactToolHandler(state.store, nil))
	handler := reader.Handler
	reader.Handler = tools.HandlerFunc(func(ctx context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
		result, readErr := handler.Execute(ctx, request)
		if readErr == nil {
			var page artifactPage
			if decodeErr := json.Unmarshal(result.Content, &page); decodeErr != nil {
				return tools.ExecutionResult{}, decodeErr
			}
			var input struct {
				Query string `json:"query"`
			}
			if decodeErr := json.Unmarshal(request.Call.Arguments, &input); decodeErr != nil {
				return tools.ExecutionResult{}, decodeErr
			}
			page.queried = input.Query != ""
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
	resolve := func(_ context.Context, request model.Request) (runtimecontext.ModelWindow, error) {
		if request.Model != config.routedModel() {
			return runtimecontext.ModelWindow{}, fmt.Errorf("unknown model window: %s", request.Model)
		}
		// Live capacity comes from the host's explicit model configuration.
		// MaxInputTokens below is a separate, intentionally small service cap.
		return runtimecontext.ModelWindow{
			ContextTokens: config.contextTokens, ReservedOutputTokens: config.outputTokens,
			TokenCountContext: runtimecontext.TokenCountContext{Protocol: config.provider, Model: config.modelID},
		}, nil
	}

	bounded, err := harness.NewBoundedContextAdapter(state.store, manager, resolve, nil)
	if err != nil {
		return nil, err
	}
	var prepare agent.ModelRequestPreparer = bounded.Prepare
	var rehydrate plugin.RunMiddleware = bounded
	if opaque {
		prepare, err = harness.NewManagedContextPreparer(state.store, manager, resolve)
		if err != nil {
			return nil, err
		}
		rehydrate, err = harness.NewContextRunMiddleware(state.store, nil)
		if err != nil {
			return nil, err
		}
	}
	agentRunner, err := agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: client, Catalog: registry, Executor: registry, Clock: state.clock,
		// Add any host schema projections BEFORE Context preparation, which must be last.
		PrepareModelRequest: agent.ChainModelRequestPreparers(prepare),
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
		Runtime: rt, Agent: agentRunner, Store: state.store, Clock: state.clock, Context: manager, Catalog: registry,
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
			ModelOptions:   config.modelOptions(),
			Instructions:   taskInstructionsForSuite(config.suite),
			OutputContract: &model.OutputContract{Name: "task_answer", Schema: answerSchema},
			ToolKeys:       []string{lookupKey, harness.ContextArtifactToolKey, draftKey},
			ToolPolicies:   taskToolPoliciesForSuite(config.suite),
			ContextPolicy:  taskContextPolicyForSuite(config.suite),
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
		receipt := numericUsage(response.Usage)
		ceiling.state.observations = append(ceiling.state.observations, modelObservation{
			DurationMs: time.Since(started).Milliseconds(), Usage: receipt,
		})
		ceiling.state.mu.Unlock()
		ceiling.state.recordInputCountUsageComparison(request, receipt, err)
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
