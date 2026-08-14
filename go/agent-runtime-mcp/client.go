package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

const (
	ProtocolVersion          = "2026-07-28"
	maxToolListPages         = 100
	maxDiscoveredTools       = 2048
	maxRemoteTextBytes       = 16 * 1024
	maxRemoteToolSchemaBytes = 1024 * 1024
	eventDiscovery           = "protocol.mcp.discovery"
	eventToolCall            = "protocol.mcp.tool_call"
)

var (
	ErrInvalidClient       = errors.New("invalid MCP client")
	ErrLegacyProtocol      = errors.New("legacy MCP protocol is forbidden")
	ErrUnsupportedProtocol = errors.New("unsupported MCP protocol version")
	ErrToolsUnavailable    = errors.New("MCP tools capability is unavailable")
	ErrInvalidTool         = errors.New("invalid MCP tool")
	ErrInvalidArguments    = errors.New("MCP tool arguments must be a JSON object")
	ErrToolResult          = errors.New("MCP tool returned an error")
	ErrInputRequired       = errors.New("MCP tool requires additional input")
	ErrToolPageLimit       = errors.New("MCP tool page limit exceeded")
	ErrDiscoveryLimit      = errors.New("MCP discovery limit exceeded")
)

// ClientDependencies keep protocol construction explicit and host-neutral.
type ClientDependencies struct {
	Transport             *Transport
	ImplementationName    string
	ImplementationVersion string
	Observers             []plugin.Observer
}

func (client *Client) observe(ctx context.Context, eventType, status string, terminal bool) {
	if client == nil || client.observers == nil {
		return
	}
	client.observers.Observe(ctx, plugin.Event{Type: eventType, Status: status, Terminal: terminal})
}

func (client *Client) observeOutcome(ctx context.Context, eventType string, err error) {
	status := "completed"
	if err != nil {
		status = "failed"
	}
	client.observe(ctx, eventType, status, true)
}

// Client owns only the MCP wire adapter. It does not own host security,
// credentials, Runtime state, or Tool execution policy.
type Client struct {
	transport *Transport
	impl      mcpsdk.Implementation
	observers *plugin.ObserverSet
}

// DiscoveredTool preserves MCP discovery metadata while exposing a stable
// Runtime Tool Definition for model/tool composition.
type DiscoveredTool struct {
	Name         string
	Title        string
	Description  string
	InputSchema  json.RawMessage
	OutputSchema json.RawMessage
	Definition   tools.Definition
}

// Discovery is one immutable MCP tools snapshot.
type Discovery struct {
	ProtocolVersion  string
	ServerName       string
	ServerVersion    string
	CapabilitiesJSON json.RawMessage
	ToolsListChanged bool
	Tools            []DiscoveredTool
	Catalog          CatalogSnapshot
}

// CallRequest executes one named MCP Tool with object arguments.
type CallRequest struct {
	Name      string
	Arguments json.RawMessage
}

// NewClient creates one MCP 2026-07-28-only adapter.
func NewClient(dependencies ClientDependencies) (*Client, error) {
	name := strings.TrimSpace(dependencies.ImplementationName)
	version := strings.TrimSpace(dependencies.ImplementationVersion)
	if dependencies.Transport == nil || name == "" || version == "" {
		return nil, ErrInvalidClient
	}
	observers, err := plugin.NewObserverSet(dependencies.Observers...)
	if err != nil {
		return nil, errors.Join(ErrInvalidClient, err)
	}
	return &Client{
		transport: dependencies.Transport,
		impl:      mcpsdk.Implementation{Name: name, Version: version},
		observers: observers,
	}, nil
}

func validateToolMetadata(name, title, description string) error {
	if name == "" {
		return ErrInvalidTool
	}
	if len(name) > maxRemoteTextBytes || len(title) > maxRemoteTextBytes || len(description) > maxRemoteTextBytes {
		return ErrDiscoveryLimit
	}
	return nil
}

func projectToolSchemas(item *mcpsdk.Tool) (json.RawMessage, json.RawMessage, error) {
	input, err := projectToolSchema(item.InputSchema)
	if err != nil {
		return nil, nil, err
	}
	if item.OutputSchema == nil {
		return input, nil, nil
	}
	output, err := projectToolSchema(item.OutputSchema)
	if err != nil {
		return nil, nil, err
	}
	return input, output, nil
}

func projectToolSchema(schema any) (json.RawMessage, error) {
	payload, err := json.Marshal(schema)
	if err != nil || !json.Valid(payload) {
		return nil, ErrInvalidTool
	}
	if len(payload) > maxRemoteToolSchemaBytes {
		return nil, ErrDiscoveryLimit
	}
	return payload, nil
}

// DiscoverTools returns the complete deterministic Tool catalog exposed by one
// modern stateless MCP endpoint.
func (client *Client) DiscoverTools(ctx context.Context, rawEndpoint string) (Discovery, error) {
	client.observe(ctx, eventDiscovery, "started", false)
	discovery, err := client.discoverTools(ctx, rawEndpoint)
	client.observeOutcome(ctx, eventDiscovery, err)
	return discovery, err
}

func (client *Client) discoverTools(ctx context.Context, rawEndpoint string) (Discovery, error) {
	session, endpoint, err := client.connect(ctx, rawEndpoint)
	if err != nil {
		return Discovery{}, err
	}
	defer func() { _ = session.Close() }()
	discovery, err := newDiscovery(session.InitializeResult(), endpoint)
	if err != nil {
		return Discovery{}, err
	}
	seenNames := make(map[string]struct{})
	seenCursors := make(map[string]struct{})
	cursor := ""
	for page := 0; page < maxToolListPages; page++ {
		result, listErr := session.ListTools(ctx, &mcpsdk.ListToolsParams{Cursor: cursor})
		if listErr != nil {
			return Discovery{}, listErr
		}
		if err = appendToolPage(&discovery, result, seenNames); err != nil {
			return Discovery{}, err
		}
		cursor, err = nextToolCursor(result.NextCursor, seenCursors)
		if err != nil {
			return Discovery{}, err
		}
		if cursor == "" {
			return cloneDiscovery(discovery), nil
		}
	}
	return Discovery{}, ErrToolPageLimit
}

func newDiscovery(initialize *mcpsdk.InitializeResult, endpoint string) (Discovery, error) {
	if initialize == nil || initialize.Capabilities == nil || initialize.Capabilities.Tools == nil {
		return Discovery{}, ErrToolsUnavailable
	}
	capabilitiesJSON, err := json.Marshal(initialize.Capabilities)
	if err != nil {
		return Discovery{}, err
	}
	discovery := Discovery{
		ProtocolVersion: ProtocolVersion, CapabilitiesJSON: append(json.RawMessage(nil), capabilitiesJSON...),
		ToolsListChanged: initialize.Capabilities.Tools.ListChanged, Catalog: CatalogSnapshot{Endpoint: endpoint},
	}
	if initialize.ServerInfo != nil {
		discovery.ServerName = strings.TrimSpace(initialize.ServerInfo.Name)
		discovery.ServerVersion = strings.TrimSpace(initialize.ServerInfo.Version)
	}
	return discovery, nil
}

func appendToolPage(discovery *Discovery, result *mcpsdk.ListToolsResult, seenNames map[string]struct{}) error {
	if discovery == nil || result == nil {
		return ErrInvalidTool
	}
	for _, item := range result.Tools {
		if len(discovery.Tools) >= maxDiscoveredTools {
			return ErrDiscoveryLimit
		}
		projected, err := projectTool(item)
		if err != nil {
			return err
		}
		if _, duplicate := seenNames[projected.Name]; duplicate {
			return fmt.Errorf("%w: duplicate tool %q", ErrInvalidTool, projected.Name)
		}
		seenNames[projected.Name] = struct{}{}
		discovery.Tools = append(discovery.Tools, projected)
		discovery.Catalog.Definitions = append(discovery.Catalog.Definitions, tools.CloneDefinition(projected.Definition))
	}
	return nil
}

func nextToolCursor(raw string, seen map[string]struct{}) (string, error) {
	next := strings.TrimSpace(raw)
	if next == "" {
		return "", nil
	}
	if _, repeated := seen[next]; repeated {
		return "", fmt.Errorf("%w: repeated cursor", ErrInvalidTool)
	}
	seen[next] = struct{}{}
	return next, nil
}

// CallTool executes one Tool through the official MCP SDK and returns its full
// protocol result JSON. Business Tool errors remain errors at this edge.
func (client *Client) CallTool(ctx context.Context, rawEndpoint string, request CallRequest) (json.RawMessage, error) {
	client.observe(ctx, eventToolCall, "started", false)
	result, err := client.callTool(ctx, rawEndpoint, request)
	client.observeOutcome(ctx, eventToolCall, err)
	return result, err
}

func (client *Client) callTool(ctx context.Context, rawEndpoint string, request CallRequest) (json.RawMessage, error) {
	name := strings.TrimSpace(request.Name)
	if name == "" {
		return nil, ErrInvalidTool
	}
	arguments, err := decodeArguments(request.Arguments)
	if err != nil {
		return nil, err
	}
	session, _, err := client.connect(ctx, rawEndpoint)
	if err != nil {
		return nil, err
	}
	defer func() { _ = session.Close() }()

	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, ErrToolResult
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if result.NeedsInput() {
		return nil, fmt.Errorf("%w: %s", ErrInputRequired, toolResultMessage(raw))
	}
	if result.IsError {
		return nil, fmt.Errorf("%w: %s", ErrToolResult, toolResultMessage(raw))
	}
	return append(json.RawMessage(nil), raw...), nil
}

func (client *Client) connect(ctx context.Context, rawEndpoint string) (*mcpsdk.ClientSession, string, error) {
	if client == nil || client.transport == nil {
		return nil, "", ErrInvalidClient
	}
	endpoint, headers, err := client.transport.prepare(ctx, rawEndpoint)
	if err != nil {
		return nil, "", err
	}
	headers.Del("Mcp-Session-Id")
	headers.Del("MCP-Protocol-Version")

	httpClient := *client.transport.client
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient.Transport = &modernOnlyRoundTripper{next: base, headers: headers}
	transport := &mcpsdk.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           &httpClient,
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}
	sdkClient := mcpsdk.NewClient(&client.impl, &mcpsdk.ClientOptions{
		Capabilities:   &mcpsdk.ClientCapabilities{},
		MultiRoundTrip: &mcpsdk.MultiRoundTripOptions{Disabled: true},
	})
	session, err := sdkClient.Connect(ctx, transport, nil)
	if err != nil {
		return nil, "", err
	}
	initialize := session.InitializeResult()
	if initialize == nil || strings.TrimSpace(initialize.ProtocolVersion) != ProtocolVersion {
		_ = session.Close()
		return nil, "", ErrUnsupportedProtocol
	}
	return session, endpoint, nil
}

type modernOnlyRoundTripper struct {
	next    http.RoundTripper
	headers http.Header
}

func (roundTripper *modernOnlyRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := validateModernRequest(request, roundTripper); err != nil {
		return nil, err
	}
	clone, err := cloneRequest(request, roundTripper.headers)
	if err != nil {
		return nil, err
	}
	if legacyPayload(clone) {
		return nil, ErrLegacyProtocol
	}
	return roundTripper.next.RoundTrip(clone)
}

func validateModernRequest(request *http.Request, roundTripper *modernOnlyRoundTripper) error {
	if request == nil || roundTripper == nil || roundTripper.next == nil {
		return ErrInvalidTransport
	}
	if strings.TrimSpace(request.Header.Get("Mcp-Session-Id")) != "" {
		return ErrLegacyProtocol
	}
	return nil
}

func cloneRequest(request *http.Request, headers http.Header) (*http.Request, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	for key, values := range headers {
		clone.Header.Del(key)
		for _, value := range values {
			clone.Header.Add(key, value)
		}
	}
	if clone.Body == nil {
		return clone, nil
	}
	payload, err := io.ReadAll(clone.Body)
	if err != nil {
		return nil, err
	}
	_ = clone.Body.Close()
	clone.Body = io.NopCloser(bytes.NewReader(payload))
	return clone, nil
}

func legacyPayload(request *http.Request) bool {
	if request == nil || request.Body == nil {
		return false
	}
	payload, err := io.ReadAll(request.Body)
	if err != nil {
		return false
	}
	_ = request.Body.Close()
	request.Body = io.NopCloser(bytes.NewReader(payload))
	var envelope struct {
		Method string `json:"method"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return false
	}
	method := strings.TrimSpace(envelope.Method)
	return method == "initialize" || method == "notifications/initialized"
}

func projectTool(item *mcpsdk.Tool) (DiscoveredTool, error) {
	if item == nil {
		return DiscoveredTool{}, ErrInvalidTool
	}
	name := strings.TrimSpace(item.Name)
	title := strings.TrimSpace(item.Title)
	description := strings.TrimSpace(item.Description)
	if err := validateToolMetadata(name, title, description); err != nil {
		return DiscoveredTool{}, err
	}
	input, output, err := projectToolSchemas(item)
	if err != nil {
		return DiscoveredTool{}, err
	}
	definition := tools.Definition{
		Key: name, Name: name, Description: description, InputSchema: append(json.RawMessage(nil), input...),
	}
	return DiscoveredTool{
		Name: name, Title: title, Description: definition.Description,
		InputSchema: append(json.RawMessage(nil), input...), OutputSchema: append(json.RawMessage(nil), output...),
		Definition: tools.CloneDefinition(definition),
	}, nil
}

func decodeArguments(raw json.RawMessage) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	var arguments map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&arguments); err != nil || arguments == nil {
		return nil, ErrInvalidArguments
	}
	return arguments, nil
}

func toolResultMessage(raw json.RawMessage) string {
	var envelope struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &envelope) == nil {
		parts := make([]string, 0, len(envelope.Content))
		for _, item := range envelope.Content {
			if text := strings.TrimSpace(item.Text); text != "" {
				parts = append(parts, text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	return strings.TrimSpace(string(raw))
}

func cloneDiscovery(discovery Discovery) Discovery {
	discovery.CapabilitiesJSON = append(json.RawMessage(nil), discovery.CapabilitiesJSON...)
	discovery.Tools = append([]DiscoveredTool(nil), discovery.Tools...)
	for index := range discovery.Tools {
		discovery.Tools[index].InputSchema = append(json.RawMessage(nil), discovery.Tools[index].InputSchema...)
		discovery.Tools[index].OutputSchema = append(json.RawMessage(nil), discovery.Tools[index].OutputSchema...)
		discovery.Tools[index].Definition = tools.CloneDefinition(discovery.Tools[index].Definition)
	}
	discovery.Catalog = CloneCatalogSnapshot(discovery.Catalog)
	return discovery
}
