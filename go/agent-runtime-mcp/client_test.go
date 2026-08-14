package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type lookupInput struct {
	ID string `json:"id"`
}

func assertTestDiscovery(t *testing.T, discovery Discovery) {
	t.Helper()
	if discovery.ProtocolVersion != ProtocolVersion || discovery.ServerName != "fixture" || len(discovery.Tools) != 1 {
		t.Fatalf("unexpected discovery: %#v", discovery)
	}
	tool := discovery.Tools[0]
	if tool.Name != testLookupTool || tool.Definition.Key != testLookupTool || !strings.Contains(string(tool.InputSchema), "id") {
		t.Fatalf("unexpected Tool projection: %#v", tool)
	}
}

type lookupOutput struct {
	Value string `json:"value"`
}

const testLookupTool = "lookup"

func TestClientUses20260728StatelessDiscoveryAndToolCall(t *testing.T) {
	t.Parallel()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "fixture", Version: "1.0.0"}, nil)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: testLookupTool, Title: "Lookup", Description: "Find a value"},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, input lookupInput) (*mcpsdk.CallToolResult, lookupOutput, error) {
			return nil, lookupOutput{Value: "value:" + input.ID}, nil
		})
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, &mcpsdk.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, PropagateRequestCancellation: true,
	})
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	client := newTestClient(t, httpServer.Client())
	discovery, err := client.DiscoverTools(t.Context(), httpServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	assertTestDiscovery(t, discovery)

	result, err := client.CallTool(t.Context(), httpServer.URL, CallRequest{
		Name: testLookupTool, Arguments: json.RawMessage(`{"id":"42"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result), `"value":"value:42"`) || strings.Contains(string(result), "user_id") {
		t.Fatalf("unexpected Tool result: %s", result)
	}
}

func TestClientBlocksOfficialSDKLegacyInitializeFallbackBeforeNetwork(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		http.Error(writer, "modern discovery unavailable", http.StatusNotFound)
	}))
	defer httpServer.Close()

	client := newTestClient(t, httpServer.Client())
	_, err := client.DiscoverTools(t.Context(), httpServer.URL)
	if !errors.Is(err, ErrLegacyProtocol) {
		t.Fatalf("legacy fallback error = %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("network requests = %d, want only server/discover", requests.Load())
	}
}

func TestClientRejectsNonObjectArgumentsBeforeNetwork(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, &http.Client{})
	_, err := client.CallTool(t.Context(), "https://mcp.example/rpc", CallRequest{
		Name: testLookupTool, Arguments: json.RawMessage(`[]`),
	})
	if !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("argument error = %v", err)
	}
}

func newTestClient(t *testing.T, httpClient *http.Client) *Client {
	t.Helper()
	transport, err := NewTransport(TransportDependencies{
		HTTPClient: httpClient,
		EndpointValidator: EndpointValidatorFunc(func(string) error {
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientDependencies{
		Transport: transport, ImplementationName: "gaoge-test", ImplementationVersion: "0.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}
