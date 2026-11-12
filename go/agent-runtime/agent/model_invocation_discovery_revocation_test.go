package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

const (
	auditHostedToolKey     = "minimax.web_search"
	auditHostedToolVersion = "p3-hosted-v1"
)

type revocableHostedCatalog struct {
	current model.HostedTool
	enabled bool
}

func (catalog *revocableHostedCatalog) Resolve(
	_ context.Context, key, _ string,
) (model.HostedTool, bool, error) {
	if key != catalog.current.Key || !catalog.enabled {
		return model.HostedTool{}, false, nil
	}
	return model.CloneHostedTools([]model.HostedTool{catalog.current})[0], true, nil
}

type retryableCapturedModel struct {
	requests []model.Request
}

func (provider *retryableCapturedModel) Generate(
	_ context.Context, request model.Request,
) (model.Response, error) {
	provider.requests = append(provider.requests, model.CloneRequest(request))
	return model.Response{Content: "done after resume"}, nil
}

func TestPendingHostedGrantRevocationBlocksProviderRetry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		revoke func(*revocableHostedCatalog)
		scoped bool
	}{
		{"disabled role grant", func(c *revocableHostedCatalog) { c.enabled = false }, true},
		{"version drift role grant", func(c *revocableHostedCatalog) { c.current.DefinitionVersion = "p3-hosted-v2" }, true},
		{"provider payload drift role grant", func(c *revocableHostedCatalog) {
			c.current.Target = json.RawMessage(`{"variants":[{"protocol":"minimax_anthropic","payload":{"type":"web_search_new"}}]}`)
		}, true},
		{"disabled ordinary hosted activation", func(c *revocableHostedCatalog) { c.enabled = false }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runtime := newModelPreparedCutRuntime(t, 1)
			registry := mustRegistry(t, nil)
			catalog := &revocableHostedCatalog{
				enabled: true,
				current: model.HostedTool{
					Key: auditHostedToolKey, DefinitionVersion: auditHostedToolVersion,
					Target: json.RawMessage(`{"variants":[{"protocol":"minimax_anthropic","payload":{"type":"web_search"}}]}`),
				},
			}
			provider := &retryableCapturedModel{}
			deps := agent.Dependencies{
				Runtime: runtime, Model: provider,
				Catalog: registry, Executor: registry, HostedTools: catalog,
			}
			runner, err := agent.NewRunner(deps)
			if err != nil {
				t.Fatal(err)
			}
			start := startRequest("audit_hosted_retry", "audit_request", "Search", auditHostedToolKey)
			start.Model = "specialist-model"
			if tc.scoped {
				start.HostedToolGrants = []agent.HostedToolGrant{{
					Key: auditHostedToolKey, DefinitionVersion: auditHostedToolVersion,
				}}
			}
			pending, err := runner.StartRun(t.Context(), start)
			pending = mustLoadModelInvocationRun(t, runtime, start.ID)
			if !errors.Is(err, errInjectedModelInvocationCrash) || pending.Run.Status != kernel.RunStatusRunning ||
				len(provider.requests) != 0 {
				t.Fatalf("expected durable pre-dispatch interruption: %+v err=%v calls=%d",
					pending.Run, err, len(provider.requests))
			}
			assertPreparedUnsentInvocation(t, pending)
			tc.revoke(catalog)
			restarted, restartErr := agent.NewRunner(deps)
			if restartErr != nil {
				t.Fatal(restartErr)
			}
			after, err := restarted.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
			if err == nil || after.Run.Status != kernel.RunStatusFailed ||
				after.Run.ErrorCode != "agent.discovery_invalid" || len(provider.requests) != 0 {
				t.Fatalf("pending Hosted request bypassed revocation: status=%s code=%s err=%v providerDeliveries=%d",
					after.Run.Status, after.Run.ErrorCode, err, len(provider.requests))
			}
		})
	}
}

func TestPendingLocalToolDefinitionDriftBlocksProviderRetry(t *testing.T) {
	t.Parallel()
	runtime := newModelPreparedCutRuntime(t, 1)
	executions := 0
	registry, keys := discoveryTestRegistry(t, 32, &executions)
	catalog := &changingDiscoveryCatalog{Registry: registry}
	provider := &retryableCapturedModel{}
	deps := agent.Dependencies{
		Runtime: runtime, Model: provider, Catalog: catalog, Executor: registry,
		ToolDiscovery: agent.LexicalToolDiscovery{},
	}
	runner, err := agent.NewRunner(deps)
	if err != nil {
		t.Fatal(err)
	}
	start := startRequest("audit_discovery_pending_retry", "audit_discovery_request", "Find documents", keys...)
	start.InitialToolKeys = []string{discoveryChosenToolKey}
	pending, err := runner.StartRun(t.Context(), start)
	pending = mustLoadModelInvocationRun(t, runtime, start.ID)
	if !errors.Is(err, errInjectedModelInvocationCrash) || pending.Run.Status != kernel.RunStatusRunning ||
		len(provider.requests) != 0 {
		t.Fatalf("expected pending model intent: run=%+v err=%v physicalCalls=%d",
			pending.Run, err, len(provider.requests))
	}
	assertPreparedUnsentInvocation(t, pending)
	catalog.changed = true
	restarted, restartErr := agent.NewRunner(deps)
	if restartErr != nil {
		t.Fatal(restartErr)
	}
	after, err := restarted.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
	if err == nil || after.Run.Status != kernel.RunStatusFailed ||
		after.Run.ErrorCode != "agent.discovery_invalid" || len(provider.requests) != 0 || executions != 0 {
		t.Fatalf("pending Tool Search request escaped frozen catalog: run=%+v err=%v providerCalls=%d toolCalls=%d",
			after.Run, err, len(provider.requests), executions)
	}
}

func TestPendingUnchangedHostedGrantRetainsInvocationIdentity(t *testing.T) {
	t.Parallel()
	runtime := newModelPreparedCutRuntime(t, 1)
	registry := mustRegistry(t, nil)
	catalog := &revocableHostedCatalog{
		enabled: true,
		current: model.HostedTool{
			Key: auditHostedToolKey, DefinitionVersion: auditHostedToolVersion,
			Target: json.RawMessage(`{"variants":[{"protocol":"minimax_anthropic","payload":{"type":"web_search"}}]}`),
		},
	}
	provider := &retryableCapturedModel{}
	deps := agent.Dependencies{
		Runtime: runtime, Model: provider,
		Catalog: registry, Executor: registry, HostedTools: catalog,
	}
	runner, err := agent.NewRunner(deps)
	if err != nil {
		t.Fatal(err)
	}
	start := startRequest("audit_hosted_unchanged", "audit_request_unchanged", "Search", auditHostedToolKey)
	start.Model = "specialist-model"
	start.HostedToolGrants = []agent.HostedToolGrant{{
		Key: auditHostedToolKey, DefinitionVersion: auditHostedToolVersion,
	}}
	pending, err := runner.StartRun(t.Context(), start)
	pending = mustLoadModelInvocationRun(t, runtime, start.ID)
	if !errors.Is(err, errInjectedModelInvocationCrash) || pending.Run.Status != kernel.RunStatusRunning || len(provider.requests) != 0 {
		t.Fatalf("expected recoverable model failure: status=%s err=%v", pending.Run.Status, err)
	}
	original := assertPreparedUnsentInvocation(t, pending)
	restarted, restartErr := agent.NewRunner(deps)
	if restartErr != nil {
		t.Fatal(restartErr)
	}
	completed, err := restarted.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted ||
		len(provider.requests) != 1 || provider.requests[0].InvocationID != original.ID || !reflect.DeepEqual(provider.requests[0], original.Request) {
		t.Fatalf("valid Hosted retry no longer preserves durable identity: status=%s err=%v calls=%d",
			completed.Run.Status, err, len(provider.requests))
	}
	view := mustModelInvocationView(t, completed)
	if view.Budget.Usage.LLMCalls != 1 || view.Budget.Usage.ToolCalls != 0 {
		t.Fatalf("recovery billed more than one physical response: %+v", view.Budget.Usage)
	}
}

// Fail after the original durable intent, before claim or physical Generate.
// A provider error after an external send is never used as a no-effect proof.
type modelPreparedCutStore struct {
	*memory.Store
	mu           sync.Mutex
	target, seen int
}

func (s *modelPreparedCutStore) ApplyExecution(ctx context.Context, l kernel.ExecutionLease, r uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	out, e := s.Store.ApplyExecution(ctx, l, r, m)
	if e != nil {
		return out, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if mutationHasEvent(m.Events, "agent.model_invocation.pending") {
		s.seen++
		if s.seen == s.target {
			return kernel.Snapshot{}, errInjectedModelInvocationCrash
		}
	}
	return out, nil
}
func newModelPreparedCutRuntime(t *testing.T, target int) *kernel.Runtime {
	t.Helper()
	return newModelInvocationRuntime(t, &modelPreparedCutStore{Store: memory.NewStore(), target: target})
}
func assertPreparedUnsentInvocation(t *testing.T, s kernel.Snapshot) agent.ModelInvocation {
	t.Helper()
	v := mustModelInvocationView(t, s)
	if len(v.ModelInvocations) == 0 {
		t.Fatal("missing original committed intent")
	}
	i := v.ModelInvocations[len(v.ModelInvocations)-1]
	if i.ID == "" || i.RunID != s.Run.ID || i.RequestHash == "" || i.Status != agent.ModelInvocationPending || i.DispatchAttempt != 0 || i.ExecutionAttempt != 0 || i.CompletedAt != nil || i.ConsumedAt != nil {
		t.Fatalf("fault was not before physical dispatch: %+v", i)
	}
	return i
}
