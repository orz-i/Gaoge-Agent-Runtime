package harness_test

import (
	"context"
	"sync/atomic"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

type customPreflightCancellationRecorder struct {
	runtime *kernel.Runtime
	calls   atomic.Int32
}

func (c *customPreflightCancellationRecorder) Cancel(ctx context.Context, runID string, revision uint64, reason string) (kernel.Snapshot, error) {
	c.calls.Add(1)
	return c.runtime.Cancel(ctx, runID, revision, reason)
}
