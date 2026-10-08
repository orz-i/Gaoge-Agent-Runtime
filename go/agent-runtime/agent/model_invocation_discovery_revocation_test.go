package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
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
	if len(provider.requests) == 1 {
		return model.Response{}, model.NewRetryableError(errRetryableProvider)
	}
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
			runtime, _ := newTestRuntimeAndApprovals(t)
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
			if !errors.Is(err, errRetryableProvider) || pending.Run.Status != kernel.RunStatusRunning ||
				len(provider.requests) != 1 {
				t.Fatalf("expected first failed physical delivery: %+v err=%v calls=%d",
					pending.Run, err, len(provider.requests))
			}
			tc.revoke(catalog)
			restarted, err := agent.NewRunner(deps)
			if err != nil {
				t.Fatal(err)
			}
			after, err := restarted.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
			if err == nil || after.Run.Status != kernel.RunStatusFailed ||
				after.Run.ErrorCode != "agent.discovery_invalid" || len(provider.requests) != 1 {
				t.Fatalf("pending Hosted request bypassed revocation: status=%s code=%s err=%v providerDeliveries=%d",
					after.Run.Status, after.Run.ErrorCode, err, len(provider.requests))
			}
		})
	}
}

func TestPendingLocalToolDefinitionDriftBlocksProviderRetry(t *testing.T) {
	t.Parallel()
	runtime, _ := newTestRuntimeAndApprovals(t)
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
	if !errors.Is(err, errRetryableProvider) || pending.Run.Status != kernel.RunStatusRunning ||
		len(provider.requests) != 1 {
		t.Fatalf("expected pending model intent: run=%+v err=%v physicalCalls=%d",
			pending.Run, err, len(provider.requests))
	}
	catalog.changed = true
	restarted, err := agent.NewRunner(deps)
	if err != nil {
		t.Fatal(err)
	}
	after, err := restarted.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
	if err == nil || after.Run.Status != kernel.RunStatusFailed ||
		after.Run.ErrorCode != "agent.discovery_invalid" || len(provider.requests) != 1 || executions != 0 {
		t.Fatalf("pending Tool Search request escaped frozen catalog: run=%+v err=%v providerCalls=%d toolCalls=%d",
			after.Run, err, len(provider.requests), executions)
	}
}

func TestPendingUnchangedHostedGrantRetainsInvocationIdentity(t *testing.T) {
	t.Parallel()
	runtime, _ := newTestRuntimeAndApprovals(t)
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
	if !errors.Is(err, errRetryableProvider) || pending.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("expected recoverable model failure: status=%s err=%v", pending.Run.Status, err)
	}
	restarted, err := agent.NewRunner(deps)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := restarted.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted ||
		len(provider.requests) != 2 ||
		provider.requests[0].InvocationID == "" ||
		provider.requests[0].InvocationID != provider.requests[1].InvocationID {
		t.Fatalf("valid Hosted retry no longer preserves durable identity: status=%s err=%v calls=%d",
			completed.Run.Status, err, len(provider.requests))
	}
}
