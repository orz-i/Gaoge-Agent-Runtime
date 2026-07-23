package memory

import (
	"sync"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime"
)

// Cache implements Conversation's generation stream cache for single-process deployments.
type Cache struct {
	mu      sync.Mutex
	ops     uint64
	streams map[string]*generationStream
}

var _ agentruntime.GenerationStreamCacheRepository = (*Cache)(nil)

// New creates an in-memory Conversation cache.
func New() *Cache {
	return &Cache{streams: map[string]*generationStream{}}
}

// NewGenerationStreamCache exposes the cache through Conversation's consumer port.
func NewGenerationStreamCache(cache *Cache) agentruntime.GenerationStreamCacheRepository {
	return cache
}

func ttlFromNow(ttl time.Duration) time.Time {
	if ttl <= 0 {
		return time.Now().Add(time.Minute)
	}
	return time.Now().Add(ttl)
}

func (c *Cache) maybeSweepLocked(now time.Time) {
	c.ops++
	if c.ops%128 != 0 {
		return
	}
	for runID, stream := range c.streams {
		if stream.ownerExpired(now) && stream.activeExpired(now) && stream.cancelExpired(now) && stream.eventsExpired(now) {
			delete(c.streams, runID)
		}
	}
}
