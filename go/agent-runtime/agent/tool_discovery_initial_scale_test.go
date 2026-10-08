package agent_test

import (
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestRuntimeToolDiscoverySupports128MCPCandidatesPlusHostControls(t *testing.T) {
	t.Parallel()
	// Gaoge may authorize 128 MCP tools and inject several first-party
	// Harness controls. 128 is the user selectable limit, not a total cap.
	const total = 133
	runtime, _ := newTestRuntimeAndApprovals(t)
	executions := 0
	registry, keys := discoveryTestRegistry(t, total, &executions)
	initial := append([]string(nil), keys[128:]...)
	capture := &initialToolsModel{t: t, keys: initial}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: capture, Catalog: registry, Executor: registry,
		ToolDiscovery: agent.LexicalToolDiscovery{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest("discovery-large-valid", "discovery-large-valid-id", "Summarize available information", keys...)
	request.InitialToolKeys = initial
	snapshot, err := runner.StartRun(t.Context(), request)
	if err != nil || snapshot.Run.Status != kernel.RunStatusCompleted || capture.calls != 1 || executions != 0 {
		t.Fatalf("valid high-tool-count run failed: status=%s err=%v calls=%d executions=%d",
			snapshot.Run.Status, err, capture.calls, executions)
	}
}

func TestRuntimeToolDiscoveryRejectsExcessiveCandidateCount(t *testing.T) {
	t.Parallel()
	const excessive = 257
	runtime, _ := newTestRuntimeAndApprovals(t)
	executions := 0
	registry, keys := discoveryTestRegistry(t, excessive, &executions)
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: simpleTextModel{t: t, wantTools: excessive},
		Catalog: registry, Executor: registry, ToolDiscovery: agent.LexicalToolDiscovery{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest("discovery-too-many", "discovery-too-many-id", "Answer", keys...)
	snapshot, err := runner.StartRun(t.Context(), request)
	if err == nil || snapshot.Run.ID != "" || executions != 0 {
		t.Fatalf("oversized candidate set did not fail closed: run=%+v err=%v", snapshot.Run, err)
	}
}

func TestRuntimeToolDiscoveryRejectsHostedToolAsInitiallyLoaded(t *testing.T) {
	t.Parallel()
	runtime, _ := newTestRuntimeAndApprovals(t)
	registry := mustRegistry(t, nil)
	const hostedKey = "minimax.web_search"
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: simpleTextModel{t: t, wantTools: 0},
		Catalog: registry, Executor: registry, ToolDiscovery: agent.LexicalToolDiscovery{},
		HostedTools: hostedCatalog{tool: model.HostedTool{
			Key: hostedKey, DefinitionVersion: "hosted-v1",
			Target: json.RawMessage(`{"variants":[{"protocol":"minimax_anthropic","payload":{"type":"web_search"}}]}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest("discovery-hosted-incorrect", "discovery-hosted-incorrect-id", "Search", hostedKey)
	request.InitialToolKeys = []string{hostedKey}
	snapshot, err := runner.StartRun(t.Context(), request)
	if err == nil || snapshot.Run.ID != "" {
		t.Fatalf("initial local tools accepted a hosted activation: run=%+v err=%v", snapshot.Run, err)
	}
}

func TestReservedSDKDiscoveryKeyCannotBeHostedInSmallRuns(t *testing.T) {
	t.Parallel()
	for _, composed := range []bool{false, true} {
		name := "without_sdk_search"
		if composed {
			name = "with_sdk_search"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runtime, _ := newTestRuntimeAndApprovals(t)
			registry := mustRegistry(t, nil)
			deps := agent.Dependencies{
				Runtime: runtime, Model: simpleTextModel{t: t, wantTools: 0},
				Catalog: registry, Executor: registry,
				HostedTools: hostedCatalog{tool: model.HostedTool{
					Key:               agent.ToolDiscoveryKey,
					DefinitionVersion: "bad-provider-hosted-control-v1",
					Target:            json.RawMessage(`{"type":"web_search"}`),
				}},
			}
			if composed {
				deps.ToolDiscovery = agent.LexicalToolDiscovery{}
			}
			runner, err := agent.NewRunner(deps)
			if err != nil {
				t.Fatal(err)
			}
			start := startRequest("reserved_sdk_hosted", "reserved_sdk_hosted_request", "Answer", agent.ToolDiscoveryKey)
			snapshot, err := runner.StartRun(t.Context(), start)
			if err == nil || snapshot.Run.ID != "" {
				t.Fatalf("Hosted Tool shadowed SDK search control without 16 local candidates: run=%+v err=%v",
					snapshot.Run, err)
			}
		})
	}
}
