package redis

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const defaultKeyPrefix = "agentruntime:"

type Options struct {
	KeyPrefix string
}

// Cache implements Conversation's generation stream cache port.
type Cache struct {
	client *redis.Client
	prefix string
}

var _ agentruntime.GenerationStreamCacheRepository = (*Cache)(nil)

// New creates the Agent Runtime generation-stream cache.
func New(client *redis.Client, options Options) agentruntime.GenerationStreamCacheRepository {
	prefix := strings.TrimSpace(options.KeyPrefix)
	if prefix == "" {
		prefix = defaultKeyPrefix
	}
	if !strings.HasSuffix(prefix, ":") {
		prefix += ":"
	}
	return &Cache{client: client, prefix: prefix + "generation:"}
}

// ---------------------------------------------------------------------------
// 生成流恢复
// ---------------------------------------------------------------------------

// RegisterGenerationStream 记录 run 归属用户，并清除上一轮取消标记。
func (c *Cache) RegisterGenerationStream(ctx context.Context, runID string, actor domain.ActorRef, ttl time.Duration) error {
	if c.client == nil {
		return nil
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil
	}
	pipe := c.client.Pipeline()
	pipe.Set(ctx, c.ownerKey(runID), actor.TenantID+"\x1f"+actor.ActorID, ttl)
	pipe.Del(ctx, c.cancelKey(runID))
	_, err := pipe.Exec(ctx)
	return err
}

// GetGenerationStreamOwner 返回 run 归属用户。
func (c *Cache) GetGenerationStreamOwner(ctx context.Context, runID string) (domain.ActorRef, bool, error) {
	if c.client == nil {
		return domain.ActorRef{}, false, nil
	}
	raw, err := c.client.Get(ctx, c.ownerKey(runID)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return domain.ActorRef{}, false, nil
		}
		return domain.ActorRef{}, false, err
	}
	value, ok := parseGenerationStreamOwner(raw)
	if !ok {
		return domain.ActorRef{}, false, nil
	}
	return value, true, nil
}

func parseGenerationStreamOwner(raw string) (domain.ActorRef, bool) {
	tenantID, actorID, ok := strings.Cut(strings.TrimSpace(raw), "\x1f")
	if !ok || tenantID == "" || actorID == "" {
		return domain.ActorRef{}, false
	}
	return domain.ActorRef{TenantID: tenantID, ActorID: actorID}, true
}

// TouchGenerationStreamActive 刷新 run 的活跃租约。
func (c *Cache) TouchGenerationStreamActive(ctx context.Context, runID string, ttl time.Duration) error {
	if c.client == nil || ttl <= 0 {
		return nil
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil
	}
	return c.client.Set(ctx, c.activeKey(runID), "1", ttl).Err()
}

// ClearGenerationStreamActive 清理 run 的活跃租约。
func (c *Cache) ClearGenerationStreamActive(ctx context.Context, runID string) error {
	if c.client == nil {
		return nil
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil
	}
	return c.client.Del(ctx, c.activeKey(runID)).Err()
}

// IsGenerationStreamActive 查询 run 是否仍有活跃生成租约。
func (c *Cache) IsGenerationStreamActive(ctx context.Context, runID string) (bool, error) {
	if c.client == nil {
		return false, nil
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return false, nil
	}
	count, err := c.client.Exists(ctx, c.activeKey(runID)).Result()
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// RequestGenerationStreamCancel 标记 run 已被用户显式取消。
func (c *Cache) RequestGenerationStreamCancel(ctx context.Context, runID string, ttl time.Duration) error {
	if c.client == nil {
		return nil
	}
	return c.client.Set(ctx, c.cancelKey(runID), "1", ttl).Err()
}

// IsGenerationStreamCanceled 查询 run 是否已被显式取消。
func (c *Cache) IsGenerationStreamCanceled(ctx context.Context, runID string) (bool, error) {
	if c.client == nil {
		return false, nil
	}
	count, err := c.client.Exists(ctx, c.cancelKey(runID)).Result()
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// AppendGenerationStreamEvent 追加生成流事件，使用独立 seq 保持前端游标稳定。
func (c *Cache) AppendGenerationStreamEvent(ctx context.Context, runID string, payloadJSON string, maxEvents int64, ttl time.Duration) (agentruntime.GenerationStreamMessage, error) {
	if c.client == nil {
		return agentruntime.GenerationStreamMessage{}, nil
	}
	if maxEvents <= 0 {
		maxEvents = 1024
	}
	seq, err := c.client.Incr(ctx, c.seqKey(runID)).Result()
	if err != nil {
		return agentruntime.GenerationStreamMessage{}, err
	}
	id, err := c.client.XAdd(ctx, &redis.XAddArgs{
		Stream:       c.eventsKey(runID),
		MaxLenApprox: maxEvents,
		Values: map[string]interface{}{
			"seq":     seq,
			"payload": payloadJSON,
		},
	}).Result()
	if err != nil {
		return agentruntime.GenerationStreamMessage{}, err
	}
	_ = c.ExpireGenerationStream(ctx, runID, ttl)
	return agentruntime.GenerationStreamMessage{ID: id, Seq: seq, PayloadJSON: payloadJSON}, nil
}

// ListGenerationStreamEvents 返回当前保留窗口内的生成流事件。
func (c *Cache) ListGenerationStreamEvents(ctx context.Context, runID string, limit int64) ([]agentruntime.GenerationStreamMessage, error) {
	if c.client == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 1024
	}
	items, err := c.client.XRevRangeN(ctx, c.eventsKey(runID), "+", "-", limit).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, err
	}
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
	return parseGenerationStreamMessages(items), nil
}

// ReadGenerationStreamEvents 阻塞读取 afterID 之后的生成流事件。
func (c *Cache) ReadGenerationStreamEvents(ctx context.Context, runID string, afterID string, block time.Duration, limit int64) ([]agentruntime.GenerationStreamMessage, error) {
	if c.client == nil {
		return nil, nil
	}
	if strings.TrimSpace(afterID) == "" {
		afterID = "0-0"
	}
	if block <= 0 {
		block = 5 * time.Second
	}
	if limit <= 0 {
		limit = 128
	}
	streams, err := c.client.XRead(ctx, &redis.XReadArgs{
		Streams: []string{c.eventsKey(runID), afterID},
		Count:   limit,
		Block:   block,
	}).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, err
	}
	results := make([]agentruntime.GenerationStreamMessage, 0)
	for _, stream := range streams {
		results = append(results, parseGenerationStreamMessages(stream.Messages)...)
	}
	return results, nil
}

// ExpireGenerationStream 设置生成流相关键的过期时间。
func (c *Cache) ExpireGenerationStream(ctx context.Context, runID string, ttl time.Duration) error {
	if c.client == nil || ttl <= 0 {
		return nil
	}
	pipe := c.client.Pipeline()
	pipe.Expire(ctx, c.eventsKey(runID), ttl)
	pipe.Expire(ctx, c.seqKey(runID), ttl)
	pipe.Expire(ctx, c.ownerKey(runID), ttl)
	pipe.Expire(ctx, c.cancelKey(runID), ttl)
	_, err := pipe.Exec(ctx)
	return err
}

func parseGenerationStreamMessages(items []redis.XMessage) []agentruntime.GenerationStreamMessage {
	results := make([]agentruntime.GenerationStreamMessage, 0, len(items))
	for _, item := range items {
		payload := strings.TrimSpace(getStringVal(item.Values["payload"]))
		if payload == "" {
			continue
		}
		results = append(results, agentruntime.GenerationStreamMessage{
			ID:          item.ID,
			Seq:         getInt64Val(item.Values["seq"]),
			PayloadJSON: payload,
		})
	}
	return results
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

func (c *Cache) eventsKey(runID string) string {
	return c.prefix + strings.TrimSpace(runID) + ":events"
}

func (c *Cache) seqKey(runID string) string {
	return c.prefix + strings.TrimSpace(runID) + ":seq"
}

func (c *Cache) ownerKey(runID string) string {
	return c.prefix + strings.TrimSpace(runID) + ":owner"
}

func (c *Cache) activeKey(runID string) string {
	return c.prefix + strings.TrimSpace(runID) + ":active"
}

func (c *Cache) cancelKey(runID string) string {
	return c.prefix + strings.TrimSpace(runID) + ":cancel"
}

func getStringVal(raw interface{}) string {
	switch v := raw.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return fmt.Sprintf("%v", raw)
	}
}

func getInt64Val(raw interface{}) int64 {
	switch v := raw.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	case []byte:
		n, _ := strconv.ParseInt(strings.TrimSpace(string(v)), 10, 64)
		return n
	default:
		return 0
	}
}
