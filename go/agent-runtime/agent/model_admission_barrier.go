package agent

import (
	"context"
	"sync"
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
func (b *ModelAdmissionBarrier) wait(ctx context.Context) error {
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
		return ctx.Err()
	}
	return nil
}
