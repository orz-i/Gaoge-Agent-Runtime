package http

import (
	"context"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var ErrInvalidCancellationRoute = errors.New("invalid runtime cancellation route")

// RunCanceller performs feature-owned cleanup before a Run becomes terminal.
type RunCanceller interface {
	Cancel(context.Context, string, uint64, string) (kernel.Snapshot, error)
}

// CancellationRoute is one explicit composition-root binding by closed Run kind.
type CancellationRoute struct {
	Kind      kernel.RunKind
	Canceller RunCanceller
}

// CancellationRouter is immutable after construction and has no service discovery.
type CancellationRouter struct {
	routes map[kernel.RunKind]RunCanceller
}

// NewCancellationRouter validates a static, duplicate-free route table.
func NewCancellationRouter(routes ...CancellationRoute) (*CancellationRouter, error) {
	result := &CancellationRouter{routes: make(map[kernel.RunKind]RunCanceller, len(routes))}
	for _, route := range routes {
		route.Kind = kernel.RunKind(strings.TrimSpace(string(route.Kind)))
		if route.Kind == "" || route.Canceller == nil {
			return nil, ErrInvalidCancellationRoute
		}
		if _, duplicate := result.routes[route.Kind]; duplicate {
			return nil, ErrInvalidCancellationRoute
		}
		result.routes[route.Kind] = route.Canceller
	}
	return result, nil
}

func (router *CancellationRouter) cancel(
	ctx context.Context,
	snapshot kernel.Snapshot,
	expectedRevision uint64,
	reason string,
) (kernel.Snapshot, bool, error) {
	if router == nil {
		return kernel.Snapshot{}, false, nil
	}
	canceller, exists := router.routes[snapshot.Run.Kind]
	if !exists {
		return kernel.Snapshot{}, false, nil
	}
	cancelled, err := canceller.Cancel(
		ctx, snapshot.Run.ID, expectedRevision, strings.TrimSpace(reason),
	)
	return cancelled, true, err
}
