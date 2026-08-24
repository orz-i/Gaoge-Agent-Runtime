package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

var (
	ErrInvalidRegistry = errors.New("invalid MCP registry")
	ErrToolNotFound    = errors.New("MCP tool not found")
)

// ToolCaller is the minimum MCP execution port consumed by the immutable registry.
type ToolCaller interface {
	CallTool(context.Context, string, CallRequest) (json.RawMessage, error)
}

// Registry is one immutable remote MCP Tool catalog plus executor. It is bound
// to exactly one explicit discovery snapshot and endpoint.
type Registry struct {
	client      ToolCaller
	endpoint    string
	definitions map[string]tools.Definition
	remoteNames map[string]string
}

// NewRegistry freezes one discovered MCP Tool catalog for explicit host composition.
func NewRegistry(client ToolCaller, discovery Discovery) (*Registry, error) {
	endpoint := strings.TrimSpace(discovery.Catalog.Endpoint)
	if client == nil || endpoint == "" || discovery.ProtocolVersion != ProtocolVersion || len(discovery.Tools) == 0 {
		return nil, ErrInvalidRegistry
	}
	registry := &Registry{
		client: client, endpoint: endpoint,
		definitions: make(map[string]tools.Definition, len(discovery.Tools)),
		remoteNames: make(map[string]string, len(discovery.Tools)),
	}
	for _, item := range discovery.Tools {
		definition := tools.CloneDefinition(item.Definition)
		key := strings.TrimSpace(definition.Key)
		remoteName := strings.TrimSpace(item.Name)
		if key == "" || remoteName == "" {
			return nil, ErrInvalidRegistry
		}
		if _, duplicate := registry.definitions[key]; duplicate {
			return nil, ErrInvalidRegistry
		}
		registry.definitions[key] = definition
		registry.remoteNames[key] = remoteName
	}
	return registry, nil
}

// Resolve returns an isolated Tool definition by stable runtime key.
func (registry *Registry) Resolve(key string) (tools.Definition, bool) {
	if registry == nil {
		return tools.Definition{}, false
	}
	definition, ok := registry.definitions[strings.TrimSpace(key)]
	return tools.CloneDefinition(definition), ok
}

// List resolves exactly the caller-selected Tool keys in caller order.
func (registry *Registry) List(keys []string) ([]tools.Definition, error) {
	if registry == nil {
		return nil, ErrInvalidRegistry
	}
	result := make([]tools.Definition, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, rawKey := range keys {
		key := strings.TrimSpace(rawKey)
		if key == "" {
			return nil, ErrToolNotFound
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		definition, ok := registry.Resolve(key)
		if !ok {
			return nil, ErrToolNotFound
		}
		seen[key] = struct{}{}
		result = append(result, definition)
	}
	return result, nil
}

// Execute delegates one stable Tool call to the bound MCP endpoint.
func (registry *Registry) Execute(ctx context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
	if registry == nil || registry.client == nil || strings.TrimSpace(request.RunID) == "" ||
		strings.TrimSpace(request.Call.ID) == "" || strings.TrimSpace(request.Call.ToolKey) == "" ||
		!json.Valid(request.Call.Arguments) {
		return tools.ExecutionResult{}, tools.ErrInvalidCall
	}
	remoteName, ok := registry.remoteNames[request.Call.ToolKey]
	if !ok {
		return tools.ExecutionResult{}, ErrToolNotFound
	}
	content, err := registry.client.CallTool(ctx, registry.endpoint, CallRequest{
		Name: remoteName, Arguments: append(json.RawMessage(nil), request.Call.Arguments...),
	})
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	if !json.Valid(content) {
		return tools.ExecutionResult{}, tools.ErrInvalidCall
	}
	return tools.ExecutionResult{
		Content: append(json.RawMessage(nil), content...),
		Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"},
	}, nil
}
