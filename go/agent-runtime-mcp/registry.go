package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

var ErrInvalidRegistry = errors.New("invalid MCP registry")

// ToolCaller is the MCP wire capability consumed by the Runtime Tool registry.
type ToolCaller interface {
	CallTool(context.Context, string, CallRequest) (json.RawMessage, error)
}

// NewRegistry binds one discovery to the existing Tool catalog/executor. Schema
// validation, selection, cloning and result validation belong to tools.Registry.
func NewRegistry(client ToolCaller, discovery Discovery) (*tools.Registry, error) {
	endpoint := strings.TrimSpace(discovery.Endpoint)
	if client == nil || endpoint == "" || strings.TrimSpace(discovery.ProtocolVersion) == "" || len(discovery.Tools) == 0 {
		return nil, ErrInvalidRegistry
	}
	registrations := make([]tools.Registration, 0, len(discovery.Tools))
	for _, item := range discovery.Tools {
		definition := tools.CloneDefinition(item.Definition)
		name := strings.TrimSpace(definition.Name)
		registrations = append(registrations, tools.Registration{
			Definition: definition,
			Handler: tools.HandlerFunc(func(ctx context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
				content, err := client.CallTool(ctx, endpoint, CallRequest{
					Name: name, Arguments: append(json.RawMessage(nil), request.Call.Arguments...),
				})
				if err != nil {
					return tools.ExecutionResult{}, err
				}
				return tools.ExecutionResult{Content: content, Receipt: tools.Receipt{
					ExecutionID: request.Call.ID, Disposition: "committed",
				}}, nil
			}),
		})
	}
	registry, err := tools.NewRegistry(registrations)
	if err != nil {
		return nil, errors.Join(ErrInvalidRegistry, err)
	}
	return registry, nil
}
