package agent

import (
	"context"
	"sync"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// ModelAdmissionBarrier is a channel-only admission synchronizer. It cannot run
// caller code or attest Provider no-send; that remains the final Agent guard's
// separate request-bound authority. Ordinary middleware is never equivalent.
type ModelAdmissionBarrier struct {
	entered chan<- struct{}
	release <-chan struct{}
	once    sync.Once
}

func NewModelAdmissionBarrier(entered chan<- struct{}, release <-chan struct{}) *ModelAdmissionBarrier {
	return &ModelAdmissionBarrier{entered: entered, release: release}
}
func (*ModelAdmissionBarrier) Name() string { return "runtime.model_admission_barrier.v1" }
func (b *ModelAdmissionBarrier) Model(ctx context.Context, r model.Request, emit model.StreamSink, next plugin.ModelNext) (model.Response, error) {
	b.once.Do(func() {
		if b.entered != nil {
			select {
			case b.entered <- struct{}{}:
			case <-ctx.Done():
			}
		}
	})
	select {
	case <-b.release:
	case <-ctx.Done():
		return model.Response{}, ctx.Err()
	}
	out, err := next(ctx, r, emit)
	callbackscope.Record(ctx, b, "model.admission_barrier.returned", r)
	return out, err
}
