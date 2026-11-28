package consumer

import (
	"context"
	"net/http"
	"testing"

	edge "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-a2a"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var (
	_ func(edge.TransportDependencies) (*edge.Transport, error)                                  = edge.NewTransport
	_ edge.EndpointValidator                                                                     = edge.EndpointValidatorFunc(func(string) error { return nil })
	_ edge.HeaderProvider                                                                        = edge.HeaderProviderFunc(func(context.Context) (http.Header, error) { return nil, nil })
	_ handoff.ChildRunnerResolver                                                                = (*edge.Plugin)(nil)
	_ handoff.RemoteShadowObserver                                                               = (*edge.ShadowRunner)(nil)
	_ func(*edge.ShadowRunner, context.Context, string, uint64) (kernel.Snapshot, error)         = (*edge.ShadowRunner).CancelRemote
	_ func(*edge.ShadowRunner, context.Context, string, uint64, string) (kernel.Snapshot, error) = (*edge.ShadowRunner).ResumeRun
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
