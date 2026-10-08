package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	runtimemodel "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type hostedGrantRecordingModel struct {
	t    *testing.T
	seen runtimemodel.Request
}

func (model *hostedGrantRecordingModel) Generate(_ context.Context, input runtimemodel.Request) (runtimemodel.Response, error) {
	model.seen = runtimemodel.CloneRequest(input)
	if len(input.Tools) != 0 || len(input.HostedTools) != 1 ||
		input.HostedTools[0].Key != "minimax.web_search" {
		model.t.Fatalf("role-scoped hosted tool incorrectly projected: %+v", input)
	}
	return runtimemodel.Response{
		Content: "found current evidence",
		Usage:   &runtimemodel.Usage{InputTokens: 12, OutputTokens: 6},
	}, nil
}

func TestHostedRoleGrantRequiresCanonicalDefinitionVersion(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		granted  string
		actual   string
		localKey bool
		allowed  bool
	}{
		{name: "exact frozen hosted version", granted: "version-a", actual: "version-a", allowed: true},
		{name: "stale provider version", granted: "version-a", actual: "version-b"},
		{name: "missing provider version", granted: "version-a", actual: ""},
		{name: "empty grant version", granted: "", actual: "version-a"},
		{name: "non-authorized key", granted: "version-a", actual: "version-a", localKey: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runtime, _ := newTestRuntimeAndApprovals(t)
			mock := &hostedGrantRecordingModel{t: t}
			registry := mustRegistry(t, nil)
			runner, err := agent.NewRunner(agent.Dependencies{
				Runtime: runtime, Model: mock,
				Catalog: registry, Executor: registry,
				HostedTools: hostedCatalog{tool: runtimemodel.HostedTool{
					Key: "minimax.web_search", DefinitionVersion: test.actual,
					Target: json.RawMessage(`{"variants":[{"protocol":"minimax_anthropic","payload":{"type":"web_search"}}]}`),
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			request := startRequest("run_scoped_hosted", "grant-request", "research", "minimax.web_search")
			if test.localKey {
				request.ToolKeys = []string{}
			}
			request.HostedToolGrants = []agent.HostedToolGrant{
				{Key: "minimax.web_search", DefinitionVersion: test.granted},
			}
			snapshot, err := runner.StartRun(t.Context(), request)
			if test.allowed {
				if err != nil || snapshot.Run.Status != kernel.RunStatusCompleted {
					t.Fatalf("expected child Hosted grant; run=%+v err=%v", snapshot.Run, err)
				}
				if len(mock.seen.HostedTools) != 1 || mock.seen.HostedTools[0].DefinitionVersion != test.granted {
					t.Fatalf("hosted declaration was not version-frozen: %+v", mock.seen.HostedTools)
				}
				return
			}
			if !errors.Is(err, agent.ErrInvalidRequest) || snapshot.Run.ID != "" {
				t.Fatalf("invalid grant accepted: run=%+v err=%v", snapshot.Run, err)
			}
		})
	}
}

func TestHostedRoleGrantRejectsDuplicateAndNoCatalog(t *testing.T) {
	t.Parallel()
	runtime, _ := newTestRuntimeAndApprovals(t)
	registry := mustRegistry(t, nil)
	model := &hostedGrantRecordingModel{t: t}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: model, Catalog: registry, Executor: registry,
		HostedTools: hostedCatalog{tool: runtimemodel.HostedTool{Key: "minimax.web_search", DefinitionVersion: "v1", Target: json.RawMessage("{}")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest("duplicate-grant", "req", "research", "minimax.web_search")
	request.HostedToolGrants = []agent.HostedToolGrant{
		{Key: "minimax.web_search", DefinitionVersion: "v1"},
		{Key: "minimax.web_search", DefinitionVersion: "v1"},
	}
	if _, err = runner.StartRun(t.Context(), request); !errors.Is(err, agent.ErrInvalidRequest) {
		t.Fatalf("duplicate grant accepted: %v", err)
	}
}
