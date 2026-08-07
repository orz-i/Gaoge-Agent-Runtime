package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runfeed"
)

// StreamRunFeed replays retained events and follows the Run without owning or cancelling its execution.
func (handler *Handler) StreamRunFeed(context *gin.Context) {
	subscription, snapshot, afterSeq, ok := handler.prepareRunFeed(context)
	if !ok {
		return
	}
	defer subscription.Close()
	beginRunFeedStream(context)
	lastSeq, terminal := writeRunFeedReplay(context, subscription.Replay, afterSeq)
	if terminal {
		return
	}
	if terminalRunStatus(snapshot.Run.Status) {
		_ = writeRunFeedEvent(context, terminalSnapshotFeedEvent(snapshot, lastSeq+1))
		return
	}
	followRunFeed(context, subscription)
}

func (handler *Handler) prepareRunFeed(
	context *gin.Context,
) (*runfeed.Subscription, kernel.Snapshot, int64, bool) {
	if handler == nil || handler.runtime == nil || handler.feed == nil {
		writeError(context, http.StatusServiceUnavailable, "runfeed.unavailable", "run feed is unavailable")
		return nil, kernel.Snapshot{}, 0, false
	}
	runID, err := runIDParam(context)
	if err != nil {
		writeError(context, http.StatusBadRequest, "runfeed.invalid_request", err.Error())
		return nil, kernel.Snapshot{}, 0, false
	}
	afterSeq, err := runFeedAfterSeq(context)
	if err != nil {
		writeError(context, http.StatusBadRequest, "runfeed.invalid_request", err.Error())
		return nil, kernel.Snapshot{}, 0, false
	}
	snapshot, err := handler.authorizedRun(context, runID)
	if err != nil {
		writeTargetRuntimeError(context, "runfeed", err)
		return nil, kernel.Snapshot{}, 0, false
	}
	subscription, err := handler.feed.Subscribe(context.Request.Context(), runID, afterSeq)
	if err != nil {
		writeTargetRuntimeError(context, "runfeed", err)
		return nil, kernel.Snapshot{}, 0, false
	}
	return subscription, snapshot, afterSeq, true
}

func followRunFeed(context *gin.Context, subscription *runfeed.Subscription) {
	events := subscription.Events
	errorsChannel := subscription.Errors
	for {
		select {
		case event, open := <-events:
			if !open {
				return
			}
			if writeRunFeedEvent(context, event) != nil || event.Terminal {
				return
			}
		case _, open := <-errorsChannel:
			if open {
				return
			}
			errorsChannel = nil
		case <-context.Request.Context().Done():
			return
		}
	}
}

func (handler *Handler) authorizedRun(context *gin.Context, runID string) (kernel.Snapshot, error) {
	snapshot, err := handler.runtime.Load(context.Request.Context(), runID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	actor, err := handler.actorRef(context)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if snapshot.Run.Actor != actor {
		return kernel.Snapshot{}, kernel.ErrNotFound
	}
	return snapshot, nil
}

func runFeedAfterSeq(context *gin.Context) (int64, error) {
	raw := strings.TrimSpace(context.Query("afterSeq"))
	if raw == "" {
		raw = strings.TrimSpace(context.GetHeader("Last-Event-ID"))
	}
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, runfeed.ErrInvalidInput
	}
	return value, nil
}

func beginRunFeedStream(context *gin.Context) {
	context.Header("Content-Type", "text/event-stream; charset=utf-8")
	context.Header("Cache-Control", "no-cache, no-transform")
	context.Header("Connection", "keep-alive")
	context.Header("X-Accel-Buffering", "no")
	context.Status(http.StatusOK)
	context.Writer.Flush()
}

func writeRunFeedReplay(context *gin.Context, replay []runfeed.Event, afterSeq int64) (int64, bool) {
	lastSeq := afterSeq
	for _, event := range replay {
		if err := writeRunFeedEvent(context, event); err != nil {
			return lastSeq, true
		}
		lastSeq = event.Seq
		if event.Terminal {
			return lastSeq, true
		}
	}
	return lastSeq, false
}

func writeRunFeedEvent(context *gin.Context, event runfeed.Event) error {
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(context.Writer, "id: %d\ndata: %s\n\n", event.Seq, encoded); err != nil {
		return err
	}
	context.Writer.Flush()
	return nil
}

func terminalSnapshotFeedEvent(snapshot kernel.Snapshot, sequence int64) runfeed.Event {
	eventType := runfeed.EventRunFailed
	switch snapshot.Run.Status {
	case kernel.RunStatusCompleted:
		eventType = runfeed.EventRunCompleted
	case kernel.RunStatusCancelled:
		eventType = runfeed.EventRunCancelled
	}
	return runfeed.Event{
		Seq: sequence, RunID: snapshot.Run.ID, Type: eventType, Message: snapshot.Run.ErrorDetail,
		Revision: snapshot.Run.Revision, Status: string(snapshot.Run.Status), Terminal: true,
		CreatedAt: snapshot.Run.UpdatedAt,
	}
}

func terminalRunStatus(status kernel.RunStatus) bool {
	return status == kernel.RunStatusCompleted || status == kernel.RunStatusFailed || status == kernel.RunStatusCancelled
}
