package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/evaluation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestMCPDeterministicScenarioCorpus(t *testing.T) {
	draft := loadMCPScenarioSuite(t, "testdata/mcp-eval-smoke.json")
	dataset, err := evaluation.CompileScenarioDataset(draft)
	if err != nil {
		t.Fatalf("compile MCP scenario dataset: %v", err)
	}
	runner, err := evaluation.NewScenarioRunner(mcpScenarioExecutor{t: t})
	if err != nil {
		t.Fatalf("create MCP scenario runner: %v", err)
	}
	record, err := runner.Execute(t.Context(), evaluation.ExecuteRequest{
		RequestID: "mcp-eval-smoke-v1", TargetName: "mcp-local-http",
		Dataset: dataset, MaxConcurrency: 1,
	})
	if err != nil {
		t.Fatalf("execute MCP scenario corpus: %v", err)
	}
	baseline := loadMCPBaseline(t, "testdata/mcp-eval-smoke-baseline.json")
	comparison, err := evaluation.CompareBaseline(record, baseline)
	if err != nil {
		t.Fatalf("compare MCP baseline (dataset hash %s): %v", dataset.Hash, err)
	}
	if !comparison.Passed {
		t.Fatalf("MCP baseline regressed: %v", comparison.Regressions)
	}
	encoded, err := json.Marshal(record.Report)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("MCP eval report: %s", encoded)
}

type mcpScenarioExecutor struct {
	t *testing.T
}

type mcpScenarioInput struct {
	Operation string `json:"operation"`
}

func (executor mcpScenarioExecutor) Execute(
	ctx context.Context,
	request evaluation.ScenarioRequest,
) (evaluation.ScenarioObservation, error) {
	var input mcpScenarioInput
	if err := json.Unmarshal(request.Input, &input); err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	switch input.Operation {
	case "discover_call_tool":
		return executor.discoverCallTool(ctx, false)
	case "invalid_arguments_fenced":
		return executor.invalidArgumentsFenced(ctx)
	case "legacy_negotiation":
		return executor.discoverCallTool(ctx, true)
	default:
		return evaluation.ScenarioObservation{}, errors.New("unsupported MCP evaluation scenario")
	}
}

func (executor mcpScenarioExecutor) discoverCallTool(
	ctx context.Context, legacy bool,
) (evaluation.ScenarioObservation, error) {
	var httpServer *httptest.Server
	if legacy {
		httpServer = newLegacyTestServer(executor.t, true)
	} else {
		server := newTestLookupServer()
		handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, &mcpsdk.StreamableHTTPOptions{
			Stateless: true, JSONResponse: true, PropagateRequestCancellation: true,
		})
		httpServer = httptest.NewServer(handler)
		executor.t.Cleanup(httpServer.Close)
	}

	observer := &testProtocolObserver{name: "mcp-eval-observer"}
	client := newTestClient(executor.t, httpServer.Client(), observer)
	discovery, err := client.DiscoverTools(ctx, httpServer.URL)
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	if legacy && discovery.ProtocolVersion != "2025-11-25" {
		return evaluation.ScenarioObservation{}, errors.New("MCP legacy version was not negotiated")
	}
	result, err := client.CallTool(ctx, httpServer.URL, CallRequest{
		Name: testLookupTool, Arguments: json.RawMessage(`{"id":"42"}`),
	})
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	if !strings.Contains(string(result), `"value":"value:42"`) {
		return evaluation.ScenarioObservation{}, errors.New("MCP tool result did not match fixture")
	}
	events := make([]string, 0, len(observer.events))
	for _, event := range observer.events {
		events = append(events, event.Type+":"+event.Status)
	}
	return evaluation.ScenarioObservation{
		Status: kernel.RunStatusCompleted, Revision: 1, EventTypes: events,
		Effects: []evaluation.EffectCount{
			{Key: "discovered_tool", Count: len(discovery.Tools)},
			{Key: "tool_call", Count: 1},
		},
	}, nil
}

func (executor mcpScenarioExecutor) invalidArgumentsFenced(ctx context.Context) (evaluation.ScenarioObservation, error) {
	caller := &registryCaller{}
	registry, err := NewRegistry(caller, Discovery{
		ProtocolVersion: ProtocolVersion,
		Catalog:         CatalogSnapshot{Endpoint: testRegistryEndpoint},
		Tools: []DiscoveredTool{{
			Name: testLookupTool,
			Definition: tools.Definition{
				Key: testLookupTool, Name: testLookupTool,
				InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"string","minLength":1}}}`),
			},
		}},
	})
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	_, err = registry.Execute(ctx, tools.ExecutionRequest{
		RunID: "mcp-eval-invalid",
		Call:  tools.Call{ID: "mcp-eval-call", ToolKey: testLookupTool, Arguments: json.RawMessage(`{"bad":true}`)},
	})
	code, _, recoverable := tools.RecoverableCallErrorInfo(err)
	if !recoverable || code != "tool.arguments_schema" || caller.calls != 0 {
		return evaluation.ScenarioObservation{}, errors.New("MCP invalid arguments were not fenced before remote execution")
	}
	return evaluation.ScenarioObservation{
		Status: kernel.RunStatusCompleted, Revision: 1, ErrorCode: code,
		Effects: []evaluation.EffectCount{
			{Key: "recoverable_schema_error", Count: 1},
			{Key: "remote_call", Count: caller.calls},
		},
	}, nil
}

func loadMCPScenarioSuite(t *testing.T, path string) evaluation.ScenarioSuiteDraft {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var draft evaluation.ScenarioSuiteDraft
	if err = json.Unmarshal(encoded, &draft); err != nil {
		t.Fatal(err)
	}
	return draft
}

func loadMCPBaseline(t *testing.T, path string) evaluation.Baseline {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var baseline evaluation.Baseline
	if err = json.Unmarshal(encoded, &baseline); err != nil {
		t.Fatal(err)
	}
	return baseline
}
