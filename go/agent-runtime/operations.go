package agentruntime

import (
	"context"
	"errors"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

var ErrRuntimeEventQueryUnavailable = errors.New("runtime event query unavailable")

// RuntimeEventListFilter is the operator-facing, refs-only event filter.
type RuntimeEventListFilter struct {
	Query, EventType, Status, Sort string
	Actor                          *domain.ActorRef
	Thread                         *domain.ThreadRef
	CreatedFrom, CreatedTo         *time.Time
}

type RuntimeEventView struct {
	domain.Event
	Actor  domain.ActorRef
	Thread domain.ThreadRef
}

type runtimeEventQuery interface {
	ListRuntimeEvents(context.Context, RuntimeEventListFilter, int, int) ([]RuntimeEventView, int64, error)
}

func (s *Engine) ListRuntimeEvents(ctx context.Context, page, pageSize int, filter RuntimeEventListFilter) ([]RuntimeEventView, int64, error) {
	reader, ok := s.repo.(runtimeEventQuery)
	if !ok {
		return nil, 0, ErrRuntimeEventQueryUnavailable
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	return reader.ListRuntimeEvents(ctx, filter, (page-1)*pageSize, pageSize)
}
