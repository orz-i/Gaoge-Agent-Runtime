package consumer

import (
	"context"
	"net/http"
	"testing"

	edge "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-mcp"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

var (
	_ func(edge.TransportDependencies) (*edge.Transport, error)      = edge.NewTransport
	_ edge.EndpointValidator                                         = edge.EndpointValidatorFunc(func(string) error { return nil })
	_ edge.HeaderProvider                                            = edge.HeaderProviderFunc(func(context.Context) (http.Header, error) { return nil, nil })
	_ func(edge.ToolCaller, edge.Discovery) (*tools.Registry, error) = edge.NewRegistry
)

func TestHostComposesTransport(t *testing.T) {
	transport, err := edge.NewTransport(edge.TransportDependencies{
		HTTPClient:        &http.Client{},
		EndpointValidator: edge.EndpointValidatorFunc(func(string) error { return nil }),
		Headers: edge.HeaderProviderFunc(func(context.Context) (http.Header, error) {
			return http.Header{"X-Host": []string{"consumer"}}, nil
		}),
	})
	if err != nil || transport == nil {
		t.Fatalf("compose host transport: %v", err)
	}
}
