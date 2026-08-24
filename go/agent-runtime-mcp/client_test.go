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
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type lookupInput struct {
	ID string `json:"id"`
}

func TestClientProjectsNegotiatedExtensions(t *testing.T) {
	t.Parallel()
	capabilities := &mcpsdk.ServerCapabilities{Extensions: map[string]any{
		"io.modelcontextprotocol/ui": map[string]any{"version": "2026-01-26", "enabled": true},
		"com.example/audit":          map[string]any{"mode": "strict"},
	}}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: testServerName, Version: "1.0.0"}, &mcpsdk.ServerOptions{
		Capabilities: capabilities,
	})
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: testLookupTool},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, input lookupInput) (*mcpsdk.CallToolResult, lookupOutput, error) {
			return nil, lookupOutput{Value: input.ID}, nil
		})
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, &mcpsdk.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true,
	})
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	client := newTestClient(t, httpServer.Client())
	discovery, err := client.DiscoverTools(t.Context(), httpServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.Extensions) != 2 || discovery.Extensions[0].ID != "com.example/audit" ||
		discovery.Extensions[1].ID != "io.modelcontextprotocol/ui" {
		t.Fatalf("extensions = %#v", discovery.Extensions)
	}
	if !strings.Contains(string(discovery.Extensions[1].Settings), `"version":"2026-01-26"`) {
		t.Fatalf("extension settings = %s", discovery.Extensions[1].Settings)
	}
	clone := cloneDiscovery(discovery)
	discovery.Extensions[0].Settings[0] = '['
	if string(clone.Extensions[0].Settings) != `{"mode":"strict"}` {
		t.Fatalf("clone extension settings mutated: %s", clone.Extensions[0].Settings)
	}
}

func TestDiscoveryRejectsInvalidOrOversizedExtensions(t *testing.T) {
	t.Parallel()
	base := &mcpsdk.InitializeResult{Capabilities: &mcpsdk.ServerCapabilities{Tools: &mcpsdk.ToolCapabilities{}}}
	base.Capabilities.Extensions = map[string]any{"invalid": map[string]any{}}
	if _, err := newDiscovery(base, "https://mcp.example/rpc"); !errors.Is(err, ErrDiscoveryLimit) {
		t.Fatalf("invalid extension id error = %v", err)
	}
	base.Capabilities.Extensions = map[string]any{
		"com.example/oversized": map[string]any{"payload": strings.Repeat("x", maxExtensionSettings)},
	}
	if _, err := newDiscovery(base, "https://mcp.example/rpc"); !errors.Is(err, ErrDiscoveryLimit) {
		t.Fatalf("oversized extension error = %v", err)
	}
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

const (
	testLookupTool = "lookup"
	testServerName = "fixture"
)

type testProtocolObserver struct {
	name   string
	events []plugin.Event
}

func (observer *testProtocolObserver) Name() string { return observer.name }

func (observer *testProtocolObserver) Observe(_ context.Context, event plugin.Event) {
	observer.events = append(observer.events, event)
}

func TestClientUses20260728StatelessDiscoveryAndToolCall(t *testing.T) {
	t.Parallel()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: testServerName, Version: "1.0.0"}, nil)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: testLookupTool, Title: "Lookup", Description: "Find a value"},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, input lookupInput) (*mcpsdk.CallToolResult, lookupOutput, error) {
			return nil, lookupOutput{Value: "value:" + input.ID}, nil
		})
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, &mcpsdk.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, PropagateRequestCancellation: true,
	})
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	observer := &testProtocolObserver{name: "mcp-observer"}
	client := newTestClient(t, httpServer.Client(), observer)
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
	assertProtocolObserverSafe(t, observer, httpServer.URL, "mcp-observer-secret", []string{
		eventDiscovery + ":started", eventDiscovery + ":completed",
		eventToolCall + ":started", eventToolCall + ":completed",
	})
}

func TestClientBlocksOfficialSDKLegacyInitializeFallbackBeforeNetwork(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		http.Error(writer, "modern discovery unavailable", http.StatusNotFound)
	}))
	defer httpServer.Close()

	observer := &testProtocolObserver{name: "mcp-failure-observer"}
	client := newTestClient(t, httpServer.Client(), observer)
	_, err := client.DiscoverTools(t.Context(), httpServer.URL)
	if !errors.Is(err, ErrLegacyProtocol) {
		t.Fatalf("legacy fallback error = %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("network requests = %d, want only server/discover", requests.Load())
	}
	assertProtocolObserverSafe(t, observer, httpServer.URL, "mcp-observer-secret", []string{
		eventDiscovery + ":started", eventDiscovery + ":failed",
	})
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

func TestProjectToolRejectsOversizedRemoteMetadata(t *testing.T) {
	t.Parallel()
	_, err := projectTool(&mcpsdk.Tool{
		Name: testLookupTool, Description: strings.Repeat("x", maxRemoteTextBytes+1),
		InputSchema: map[string]any{"type": "object"},
	})
	if !errors.Is(err, ErrDiscoveryLimit) {
		t.Fatalf("discovery limit error = %v", err)
	}
}

func newTestClient(t *testing.T, httpClient *http.Client, observers ...plugin.Observer) *Client {
	t.Helper()
	transport, err := NewTransport(TransportDependencies{
		HTTPClient: httpClient,
		EndpointValidator: EndpointValidatorFunc(func(string) error {
			return nil
		}),
		Headers: HeaderProviderFunc(func(context.Context) (http.Header, error) {
			return http.Header{"Authorization": []string{"Bearer mcp-observer-secret"}}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientDependencies{
		Transport: transport, ImplementationName: "gaoge-test", ImplementationVersion: "0.1.0",
		Observers: observers,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func assertProtocolObserverSafe(
	t *testing.T,
	observer *testProtocolObserver,
	endpoint string,
	secret string,
	want []string,
) {
	t.Helper()
	got := make([]string, 0, len(observer.events))
	for _, event := range observer.events {
		got = append(got, event.Type+":"+event.Status)
	}
	if len(got) != len(want) {
		t.Fatalf("observer events = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("observer events = %#v, want %#v", got, want)
		}
	}
	raw, err := json.Marshal(observer.events)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), endpoint) || strings.Contains(string(raw), secret) {
		t.Fatalf("protocol observer leaked endpoint or secret: %s", raw)
	}
}
