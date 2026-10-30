package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type dispatchMutationMiddleware struct {
	name   string
	change func(*model.Request)
}

func (middleware dispatchMutationMiddleware) Name() string { return middleware.name }
func (middleware dispatchMutationMiddleware) Model(ctx context.Context, request model.Request, emit model.StreamSink, next plugin.ModelNext) (model.Response, error) {
	middleware.change(&request)
	return next(ctx, request, emit)
}

func TestModelDispatchRejectsPostIntentIdentityOrAuthorityMutation(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare bool
		change  func(*model.Request)
	}{
		{name: "model_without_preparer", change: func(r *model.Request) { r.Model = "other-model" }},
		{name: "model_with_preparer", prepare: true, change: func(r *model.Request) { r.Model = "other-model" }},
		{name: "run_id", change: func(r *model.Request) { r.RunID = "other-run" }},
		{name: "invocation_id", change: func(r *model.Request) { r.InvocationID = "other-invocation" }},
		{name: "model_options", change: func(r *model.Request) { r.ModelOptions = json.RawMessage(`{"temperature":0.99}`) }},
		{name: "output_contract_introduced", change: func(r *model.Request) {
			r.OutputContract = &model.OutputContract{Name: "answer", Schema: json.RawMessage(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`)}
		}},
		{name: "unauthorized_local_tool", change: func(r *model.Request) {
			r.Tools = append(r.Tools, tools.Definition{Key: "unregistered", Name: "unregistered", InputSchema: json.RawMessage(`{"type":"object"}`)})
		}},
		{name: "local_tool_rename", change: func(r *model.Request) { r.Tools[0].Name = "another_tool" }},
		{name: "local_tool_key_change", change: func(r *model.Request) { r.Tools[0].Key = "other" }},
		{name: "local_tool_duplicate", change: func(r *model.Request) { r.Tools = append(r.Tools, r.Tools[0]) }},
		{name: "local_tool_terminal", change: func(r *model.Request) { r.Tools[0].Terminal = !r.Tools[0].Terminal }},
		{name: "unauthorized_hosted_tool", change: func(r *model.Request) {
			r.HostedTools = append(r.HostedTools, model.HostedTool{Key: "hosted.new", Target: json.RawMessage(`{"scope":"all"}`)})
		}},
		{name: "require_tool_escalation", change: func(r *model.Request) { r.RequireToolCall = true }},
		{name: "negative_output_budget", change: func(r *model.Request) { r.MaxOutputTokens = -1 }},
		{name: "message_rewrite_without_preparer", change: func(r *model.Request) { r.Messages[0].Content = model.TextParts("new message") }},
		{name: "tool_presentation_post_intent", change: func(r *model.Request) { r.Tools[0].Description = "not frozen" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &invocationRecordingModel{}
			registry := mustRegistry(t, []tools.Registration{{Definition: tools.Definition{Key: manifestToolKey, Name: manifestToolName, InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
				t.Fatal("unexpected tool execution")
				return tools.ExecutionResult{}, nil
			})}})
			dependencies := agent.Dependencies{
				Runtime: newModelInvocationRuntime(t, memory.NewStore()), Model: provider,
				Catalog: registry, Executor: registry,
				ModelMiddleware: []plugin.ModelMiddleware{dispatchMutationMiddleware{name: "test.mutate", change: test.change}},
			}
			if test.prepare {
				dependencies.PrepareModelRequest = func(_ context.Context, r model.Request) (model.Request, error) { return r, nil }
			}
			runner, err := agent.NewRunner(dependencies)
			if err != nil {
				t.Fatal(err)
			}
			request := startRequest("dispatch-guard-"+test.name, "request-"+test.name, "complete", manifestToolKey)
			request.Model = "sealed-model"
			request.ModelOptions = json.RawMessage(`{"temperature":0.3}`)
			finished, err := runner.StartRun(t.Context(), request)
			if !errors.Is(err, agent.ErrInvalidRequest) || len(provider.callsCopy()) != 0 {
				t.Fatalf("post-intent change reached provider or did not fail closed: err=%v, calls=%+v", err, provider.callsCopy())
			}
			if finished.Run.Status != kernel.RunStatusFailed || finished.Run.ErrorCode != "agent.model_dispatch_invalid" {
				t.Fatalf("permanent invalid Model Middleware left a Run retrying indefinitely: status=%s code=%q", finished.Run.Status, finished.Run.ErrorCode)
			}
		})
	}
}

func TestModelDispatchAllowsOnlyBoundedBudgetAdjustments(t *testing.T) {
	provider := &invocationRecordingModel{}
	registry := mustRegistry(t, []tools.Registration{{Definition: tools.Definition{Key: manifestToolKey, Name: manifestToolName, InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
		t.Fatal("unexpected tool execution")
		return tools.ExecutionResult{}, nil
	})}})
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: newModelInvocationRuntime(t, memory.NewStore()), Model: provider,
		Catalog: registry, Executor: registry,
		PrepareModelRequest: func(_ context.Context, r model.Request) (model.Request, error) { return r, nil },
		ModelMiddleware: []plugin.ModelMiddleware{dispatchMutationMiddleware{name: "test.narrow", change: func(r *model.Request) {
			r.MaxOutputTokens = 32
			zero := 0
			r.HostedToolCallLimit = &zero
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest("dispatch-allow-budget", "request-allow-budget", "complete", manifestToolKey)
	request.Model = "sealed-model"
	completed, err := runner.StartRun(t.Context(), request)
	calls := provider.callsCopy()
	if err != nil || len(calls) != 1 || calls[0].Model != request.Model || calls[0].MaxOutputTokens != 32 || calls[0].HostedToolCallLimit == nil || *calls[0].HostedToolCallLimit != 0 || completed.Run.ID == "" {
		t.Fatalf("legal restriction failed: err=%v calls=%+v status=%s", err, calls, completed.Run.Status)
	}
}

func TestModelDispatchFenceSurvivesPendingIntentRestart(t *testing.T) {
	store := memory.NewStore()
	faults := &modelInvocationFaultStore{Store: store, eventType: "agent.model_invocation.pending", afterCommit: true}
	provider := &invocationRecordingModel{}
	first, err := agent.NewRunner(agent.Dependencies{
		Runtime: newModelInvocationRuntime(t, faults), Model: provider,
		PrepareModelRequest: func(_ context.Context, request model.Request) (model.Request, error) {
			request.Messages = append([]model.Message{{Role: model.RoleSystem, Content: model.TextParts("frozen context")}}, request.Messages...)
			return request, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest("dispatch-recovery", "dispatch-recovery-req", "answer")
	request.Model = "initial-model"
	_, err = first.StartRun(t.Context(), request)
	if !errors.Is(err, errInjectedModelInvocationCrash) || len(provider.callsCopy()) != 0 {
		t.Fatalf("pending commit fault: %v, provider=%+v", err, provider.callsCopy())
	}
	persisted := newModelInvocationRuntime(t, store)
	pending := mustLoadModelInvocationRun(t, persisted, request.ID)
	before := mustModelInvocationView(t, pending)
	if len(before.ModelInvocations) != 1 || before.ModelInvocations[0].Request.Model != request.Model ||
		len(before.ModelInvocations[0].Request.Messages) != 2 {
		t.Fatalf("pending intent not frozen: %+v", before.ModelInvocations)
	}
	// Different process configuration on restart still cannot change Provider/Model
	// identity. Only the originally committed request can be delivered.
	reopened, err := agent.NewRunner(agent.Dependencies{
		Runtime: persisted, Model: provider, Clock: modelInvocationClock{offset: 3 * time.Minute},
		ModelMiddleware: []plugin.ModelMiddleware{dispatchMutationMiddleware{name: "test.swap-on-resume", change: func(r *model.Request) {
			r.Model = "swapped-model"
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = reopened.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
	if !errors.Is(err, agent.ErrInvalidRequest) || len(provider.callsCopy()) != 0 {
		t.Fatalf("restart dispatched inconsistent identity: err=%v calls=%+v", err, provider.callsCopy())
	}
	latest := mustLoadModelInvocationRun(t, persisted, request.ID)
	view := mustModelInvocationView(t, latest)
	if len(view.ModelInvocations) != 1 || view.ModelInvocations[0].RequestHash != before.ModelInvocations[0].RequestHash ||
		view.ModelInvocations[0].Request.Model != request.Model || len(view.ModelInvocations[0].Request.Messages) != 2 {
		t.Fatalf("restart rewrote committed intent: before=%+v after=%+v", before.ModelInvocations, view.ModelInvocations)
	}
}

// The projection is committed once; recovery replays its exact model-visible
// schema, while the original Catalog identity continues to be revalidated.
type driftableModelCatalog struct {
	*tools.Registry
	drift bool
}

func (catalog *driftableModelCatalog) Resolve(key string) (tools.Definition, bool) {
	definition, ok := catalog.Registry.Resolve(key)
	if ok && catalog.drift {
		definition.Description = "new Catalog owner"
	}
	return definition, ok
}

func TestModelIntentFreezesProjectedSchemaAndCatalogEvidence(t *testing.T) {
	for _, drift := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume_exact_projection", true: "block_catalog_drift"}[drift], func(t *testing.T) {
			store := memory.NewStore()
			faults := &modelInvocationFaultStore{Store: store, eventType: "agent.model_invocation.pending", afterCommit: true}
			provider := &invocationRecordingModel{}
			registry := mustRegistry(t, []tools.Registration{{Definition: tools.Definition{Key: manifestToolKey, Name: manifestToolName, InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
				t.Fatal("unexpected execution")
				return tools.ExecutionResult{}, nil
			})}})
			catalog := &driftableModelCatalog{Registry: registry}
			prepared := 0
			first, err := agent.NewRunner(agent.Dependencies{Runtime: newModelInvocationRuntime(t, faults), Model: provider, Catalog: catalog, Executor: registry,
				PrepareModelRequest: func(_ context.Context, request model.Request) (model.Request, error) {
					prepared++
					request.Tools[0].Description = "frozen Host-owned projection"
					request.Tools[0].InputSchema = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
					return request, nil
				}})
			if err != nil {
				t.Fatal(err)
			}
			request := startRequest("schema-frozen-"+map[bool]string{false: "valid", true: "drift"}[drift], "schema-frozen-request", "answer", manifestToolKey)
			request.Model = "model-1"
			_, err = first.StartRun(t.Context(), request)
			if !errors.Is(err, errInjectedModelInvocationCrash) || prepared != 1 || len(provider.callsCopy()) != 0 {
				t.Fatalf("pending=%v prepared=%d calls=%d", err, prepared, len(provider.callsCopy()))
			}
			runtime := newModelInvocationRuntime(t, store)
			pending := mustLoadModelInvocationRun(t, runtime, request.ID)
			view := mustModelInvocationView(t, pending)
			if len(view.ModelInvocations) != 1 || len(view.ModelInvocations[0].CatalogFingerprints) != 1 ||
				view.ModelInvocations[0].Request.Tools[0].Description != "frozen Host-owned projection" {
				t.Fatalf("persisted projection/evidence missing: %+v", view.ModelInvocations)
			}
			catalog.drift = drift
			reopened, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: provider, Catalog: catalog, Executor: registry, Clock: modelInvocationClock{offset: 3 * time.Minute},
				PrepareModelRequest: func(context.Context, model.Request) (model.Request, error) {
					t.Fatal("already committed intent was recalculated")
					return model.Request{}, nil
				}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = reopened.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
			calls := provider.callsCopy()
			if drift {
				if !errors.Is(err, agent.ErrToolDiscoveryDenied) || len(calls) != 0 {
					t.Fatalf("Catalog drift was dispatched: err=%v calls=%+v", err, calls)
				}
			} else if err != nil || len(calls) != 1 || calls[0].Tools[0].Description != "frozen Host-owned projection" ||
				string(calls[0].Tools[0].InputSchema) != `{"type":"object","properties":{},"additionalProperties":false}` {
				t.Fatalf("replay not frozen: err=%v calls=%+v", err, calls)
			}
		})
	}
}

// Provider router configuration is Host-owned, but an existing durable
// invocation must not silently switch to a differently named Provider on restart.
type alternateNamedInvocationModel struct{ requests []model.Request }

func (*alternateNamedInvocationModel) ProviderName() string { return "other-provider" }
func (client *alternateNamedInvocationModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	client.requests = append(client.requests, model.CloneRequest(request))
	return model.Response{Content: "wrong provider"}, nil
}

// This replacement keeps the exact Provider name while changing only the
// immutable binding fingerprint (endpoint/credentials/Host policy).
type retargetedNamedInvocationModel struct{ invocationRecordingModel }

func (*retargetedNamedInvocationModel) BindingFingerprintForRequest(model.Request) string {
	return strings.Repeat("b", 64)
}

type missingNamedBindingInvocationModel struct{ invocationRecordingModel }

func (*missingNamedBindingInvocationModel) BindingFingerprintForRequest(model.Request) string {
	return ""
}

func TestPendingModelInvocationRejectsSameNamedChangedBindingFingerprint(t *testing.T) {
	store := memory.NewStore()
	faults := &modelInvocationFaultStore{Store: store, eventType: "agent.model_invocation.pending", afterCommit: true}
	first, err := agent.NewRunner(agent.Dependencies{
		Runtime: newModelInvocationRuntime(t, faults), Model: &invocationRecordingModel{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest("same-name-provider-binding", "same-name-provider-request", "complete")
	_, err = first.StartRun(t.Context(), request)
	if !errors.Is(err, errInjectedModelInvocationCrash) {
		t.Fatalf("pending request was not committed before simulated crash: %v", err)
	}
	runtime := newModelInvocationRuntime(t, store)
	pending := mustLoadModelInvocationRun(t, runtime, request.ID)
	before := mustModelInvocationView(t, pending)
	if len(before.ModelInvocations) != 1 || before.ModelInvocations[0].Provider != "fault-test-provider" ||
		before.ModelInvocations[0].BindingFingerprint != strings.Repeat("a", 64) {
		t.Fatalf("binding was not persisted: %+v", before.ModelInvocations)
	}
	for _, replacement := range []model.Client{&retargetedNamedInvocationModel{}, &missingNamedBindingInvocationModel{}} {
		restarted, newErr := agent.NewRunner(agent.Dependencies{
			Runtime: runtime, Model: replacement, Clock: modelInvocationClock{offset: 3 * time.Minute},
		})
		if newErr != nil {
			t.Fatal(newErr)
		}
		_, resumeErr := restarted.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
		if !errors.Is(resumeErr, agent.ErrProviderBindingMismatch) {
			t.Fatalf("unverified binding was allowed to dispatch: %v", resumeErr)
		}
	}
	unmodified := mustLoadModelInvocationRun(t, runtime, request.ID)
	if unmodified.Run.Revision != pending.Run.Revision {
		t.Fatalf("rejected binding mutated persisted pending work: before=%d after=%d", pending.Run.Revision, unmodified.Run.Revision)
	}
}

func TestNamedProviderWithoutBindingFingerprintRejectsNewModelIntent(t *testing.T) {
	provider := &missingNamedBindingInvocationModel{}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: newModelInvocationRuntime(t, memory.NewStore()), Model: provider,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.StartRun(t.Context(), startRequest("missing-binding", "missing-binding-request", "complete"))
	if !errors.Is(err, agent.ErrInvalidRequest) || len(provider.callsCopy()) != 0 {
		t.Fatalf("missing named binding authorized generation: err=%v calls=%+v", err, provider.callsCopy())
	}
}

func TestPendingModelInvocationRejectsChangedProviderBinding(t *testing.T) {
	store := memory.NewStore()
	faults := &modelInvocationFaultStore{Store: store, eventType: "agent.model_invocation.pending", afterCommit: true}
	first, err := agent.NewRunner(agent.Dependencies{
		Runtime: newModelInvocationRuntime(t, faults), Model: &invocationRecordingModel{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest("provider-binding-restart", "provider-binding-request", "complete")
	request.Model = "stable-model"
	_, err = first.StartRun(t.Context(), request)
	if !errors.Is(err, errInjectedModelInvocationCrash) {
		t.Fatalf("pending setup: %v", err)
	}
	runtime := newModelInvocationRuntime(t, store)
	pending := mustLoadModelInvocationRun(t, runtime, request.ID)
	before := mustModelInvocationView(t, pending)
	if len(before.ModelInvocations) != 1 || before.ModelInvocations[0].Provider != "fault-test-provider" {
		t.Fatalf("provider not bound into intent: %+v", before.ModelInvocations)
	}
	replacement := &alternateNamedInvocationModel{}
	restarted, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: replacement, Clock: modelInvocationClock{offset: 3 * time.Minute},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = restarted.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
	if !errors.Is(err, agent.ErrProviderBindingMismatch) || len(replacement.requests) != 0 {
		t.Fatalf("provider changed after intent commit: %v %+v", err, replacement.requests)
	}
}
