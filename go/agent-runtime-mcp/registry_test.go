package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const testRegistryEndpoint = "https://mcp.example/rpc"

type registryCaller struct {
	endpoint string
	request  CallRequest
}

func (caller *registryCaller) CallTool(_ context.Context, endpoint string, request CallRequest) (json.RawMessage, error) {
	caller.endpoint = endpoint
	caller.request = request
	return json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`), nil
}

func TestRegistryImplementsRuntimeCatalogAndExecutor(t *testing.T) {
	t.Parallel()
	caller := &registryCaller{}
	discovery := Discovery{
		ProtocolVersion: ProtocolVersion,
		Catalog:         CatalogSnapshot{Endpoint: testRegistryEndpoint},
		Tools: []DiscoveredTool{{
			Name: testLookupTool,
			Definition: tools.Definition{
				Key: testLookupTool, Name: testLookupTool, InputSchema: json.RawMessage(`{"type":"object"}`),
			},
		}},
	}
	registry, err := NewRegistry(caller, discovery)
	if err != nil {
		t.Fatal(err)
	}
	var _ tools.Catalog = registry
	var _ tools.Executor = registry
	definitions, err := registry.List([]string{testLookupTool, testLookupTool})
	if err != nil || len(definitions) != 1 || definitions[0].Key != testLookupTool {
		t.Fatalf("definitions=%#v err=%v", definitions, err)
	}
	result, err := registry.Execute(t.Context(), tools.ExecutionRequest{
		RunID: "run-1",
		Call:  tools.Call{ID: "call-1", ToolKey: testLookupTool, Arguments: json.RawMessage(`{"id":"42"}`)},
	})
	if err != nil || caller.endpoint != discovery.Catalog.Endpoint || caller.request.Name != testLookupTool ||
		result.Receipt.ExecutionID != "call-1" || result.Receipt.Disposition != "committed" {
		t.Fatalf("caller=%#v result=%#v err=%v", caller, result, err)
	}
}

func TestRegistryClonesDiscoveredDefinitions(t *testing.T) {
	t.Parallel()
	discovery := Discovery{
		ProtocolVersion: ProtocolVersion,
		Catalog:         CatalogSnapshot{Endpoint: testRegistryEndpoint},
		Tools: []DiscoveredTool{{
			Name: testLookupTool,
			Definition: tools.Definition{
				Key: testLookupTool, Name: testLookupTool, InputSchema: json.RawMessage(`{"type":"object"}`),
			},
		}},
	}
	registry, err := NewRegistry(&registryCaller{}, discovery)
	if err != nil {
		t.Fatal(err)
	}
	discovery.Tools[0].Definition.InputSchema[0] = '['
	definition, ok := registry.Resolve(testLookupTool)
	if !ok || string(definition.InputSchema) != `{"type":"object"}` {
		t.Fatalf("definition=%#v ok=%v", definition, ok)
	}
}
