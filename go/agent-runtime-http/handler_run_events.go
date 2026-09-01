package http

import (
	stdhttp "net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

const (
	defaultRunEventPageLimit = 100
	maxRunEventPageLimit     = 1000
)

func (handler *Handler) GetRunEvents(context *gin.Context) {
	if handler == nil || handler.runtime == nil {
		writeError(context, stdhttp.StatusServiceUnavailable, "events.unavailable", "run event journal is unavailable")
		return
	}
	runID, err := runIDParam(context)
	if err != nil {
		writeError(context, stdhttp.StatusBadRequest, "events.invalid_request", err.Error())
		return
	}
	afterSeq, limit, err := runEventPage(context)
	if err != nil {
		writeError(context, stdhttp.StatusBadRequest, "events.invalid_request", "invalid event journal cursor")
		return
	}
	snapshot, err := handler.authorizedRun(context, runID, RunOperationEventsRead)
	if err != nil {
		WriteRunAccessError(context, "events", err)
		return
	}
	if afterSeq >= snapshot.EventHead {
		writeSuccess(context, RunEventPageResponse{Events: []kernel.Event{}, EventHead: snapshot.EventHead})
		return
	}
	events, err := handler.runtime.ListEvents(context.Request.Context(), runID, afterSeq, limit)
	if err != nil {
		WriteKernelError(context, "events", err)
		return
	}
	events = eventsThroughHead(events, snapshot.EventHead)
	if events == nil {
		events = []kernel.Event{}
	}
	writeSuccess(context, RunEventPageResponse{Events: events, EventHead: snapshot.EventHead})
}

func runEventPage(context *gin.Context) (int64, int, error) {
	afterSeq := int64(0)
	if raw := strings.TrimSpace(context.Query("afterSeq")); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			return 0, 0, kernel.ErrInvalidInput
		}
		afterSeq = value
	}
	limit := defaultRunEventPageLimit
	if raw := strings.TrimSpace(context.Query("limit")); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || value <= 0 || value > maxRunEventPageLimit {
			return 0, 0, kernel.ErrInvalidInput
		}
		limit = int(value)
	}
	return afterSeq, limit, nil
}

func eventsThroughHead(events []kernel.Event, eventHead int64) []kernel.Event {
	end := len(events)
	for index, event := range events {
		if event.Seq > eventHead {
			end = index
			break
		}
	}
	return events[:end]
}
