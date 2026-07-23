package agentruntime

import (
	"context"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func (s *Engine) ResolveThread(ctx context.Context, actor domain.ActorRef, thread domain.ThreadRef) (*ThreadSnapshot, error) {
	if s.threadContext == nil {
		return nil, ErrThreadNotFound
	}
	item, err := s.threadContext.ResolveThread(ctx, ResolveThreadRequest{Actor: actor, Thread: thread})
	if err != nil {
		return nil, ErrThreadNotFound
	}
	return &item, nil
}

func (s *Engine) ListRunRecords(ctx context.Context, actor domain.ActorRef, thread domain.ThreadRef, page, pageSize int) ([]domain.Run, int64, error) {
	if _, err := s.ResolveThread(ctx, actor, thread); err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return s.repo.ListRuns(ctx, actor, &thread, (page-1)*pageSize, pageSize)
}
