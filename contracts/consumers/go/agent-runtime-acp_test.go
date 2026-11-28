package consumer

import (
	"context"
	"testing"

	wire "github.com/caelis-labs/acp-go-sdk"
	edge "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-acp"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var (
	_ func(edge.PluginDependencies) (*edge.Plugin, error)                                = edge.NewPlugin
	_ func(edge.HostDependencies) (*edge.Host, error)                                    = edge.NewHost
	_ func(edge.StdioConfig) (*edge.StdioClient, error)                                  = edge.NewStdioClient
	_ handoff.ChildRunnerResolver                                                        = (*edge.Plugin)(nil)
	_ handoff.RemoteShadowObserver                                                       = (*edge.ShadowRunner)(nil)
	_ func(*edge.ShadowRunner, context.Context, string, uint64) (kernel.Snapshot, error) = (*edge.ShadowRunner).CancelRemote
	_                                                                                    = edge.HostDependencies{Implementation: wire.Implementation{Name: "host-product", Version: "1.0"}}
	_ interface {
		Cancel(context.Context, string, uint64, string) (kernel.Snapshot, error)
	} = (*edge.Plugin)(nil)
)

func TestProtocolVersionAndFrozenHostBoundary(t *testing.T) {
	if edge.ProtocolVersion != "1" {
		t.Fatal("not ACP stable v1")
	}
	if _, err := edge.NewPlugin(edge.PluginDependencies{}); err == nil {
		t.Fatal("missing Host authority accepted")
	}
	if _, err := edge.NewHost(edge.HostDependencies{}); err == nil {
		t.Fatal("missing Host backend accepted")
	}
}
