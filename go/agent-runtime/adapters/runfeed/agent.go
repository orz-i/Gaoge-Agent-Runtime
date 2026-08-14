package runfeedadapter

import (
	"context"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runfeed"
)

const observerName = "runfeed"

// Observer adapts optional Agent live observations to a replayable Run Feed publisher.
type Observer struct {
	publisher runfeed.Publisher
}

// New creates a best-effort Runtime event adapter.
func New(publisher runfeed.Publisher) *Observer {
	return &Observer{publisher: publisher}
}

// Name returns the stable plugin registration name.
func (observer *Observer) Name() string { return observerName }

// Observe maps one neutral Runtime event to the replayable Run Feed.
func (observer *Observer) Observe(ctx context.Context, event plugin.Event) {
	if observer == nil || observer.publisher == nil {
		return
	}
	_, _ = observer.publisher.Publish(ctx, event.RunID, runfeed.Draft{
		Type: event.Type, Delta: event.Delta, Message: event.Message,
		Data: event.Data, Revision: event.Revision, Status: event.Status,
		Terminal: event.Terminal,
	})
}
