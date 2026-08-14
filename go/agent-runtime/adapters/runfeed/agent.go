package runfeedadapter

import (
	"context"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runfeed"
)

// Observer adapts optional Agent live observations to a replayable Run Feed publisher.
type Observer struct {
	publisher runfeed.Publisher
}

// New creates a best-effort Agent observation adapter.
func New(publisher runfeed.Publisher) *Observer {
	return &Observer{publisher: publisher}
}

func (observer *Observer) ObserveAgent(ctx context.Context, runID string, observation agent.Observation) {
	if observer == nil || observer.publisher == nil {
		return
	}
	_, _ = observer.publisher.Publish(ctx, runID, runfeed.Draft{
		Type: observation.Type, Delta: observation.Delta, Message: observation.Message,
		Data: observation.Data, Revision: observation.Revision, Status: observation.Status,
		Terminal: observation.Terminal,
	})
}
