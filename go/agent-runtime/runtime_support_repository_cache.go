package agentruntime

import (
	"context"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

// GenerationStreamMessage 是生成流中的一条可恢复事件。
type GenerationStreamMessage struct {
	ID          string
	Seq         int64
	PayloadJSON string
}

// GenerationStreamCacheRepository stores short-lived Agent Runtime stream recovery state.
type GenerationStreamCacheRepository interface {
	RegisterGenerationStream(ctx context.Context, runID string, actor domain.ActorRef, ttl time.Duration) error
	GetGenerationStreamOwner(ctx context.Context, runID string) (domain.ActorRef, bool, error)
	TouchGenerationStreamActive(ctx context.Context, runID string, ttl time.Duration) error
	ClearGenerationStreamActive(ctx context.Context, runID string) error
	IsGenerationStreamActive(ctx context.Context, runID string) (bool, error)
	RequestGenerationStreamCancel(ctx context.Context, runID string, ttl time.Duration) error
	IsGenerationStreamCanceled(ctx context.Context, runID string) (bool, error)
	AppendGenerationStreamEvent(ctx context.Context, runID string, payloadJSON string, maxEvents int64, ttl time.Duration) (GenerationStreamMessage, error)
	ListGenerationStreamEvents(ctx context.Context, runID string, limit int64) ([]GenerationStreamMessage, error)
	ReadGenerationStreamEvents(ctx context.Context, runID string, afterID string, block time.Duration, limit int64) ([]GenerationStreamMessage, error)
	ExpireGenerationStream(ctx context.Context, runID string, ttl time.Duration) error
}
