// Package conversation owns conversation use cases and policy.
package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	// userMemCacheTTL：用户记忆在会话期间极少变化，缓存 3 分钟。
	userMemCacheTTL = 3 * time.Minute
	// userSettingCacheTTL：用户设置在会话期间几乎不变，缓存 10 分钟。
	userSettingCacheTTL = 10 * time.Minute
	// inMemoryCacheSweepInterval：主动清理过期内存缓存，避免冷 key 长期驻留。
	inMemoryCacheSweepInterval = time.Minute
)

type cachedUserMemories struct {
	memories  []MemoryItem
	expiresAt time.Time
}

type cachedUserSetting struct {
	value     string
	valid     bool
	expiresAt time.Time
}

// getUserSettingCached 从内存缓存读取用户设置，未命中时回退到 DB 查询。
func (s *Engine) getUserSettingCached(ctx context.Context, actor domain.ActorRef, key string) (string, error) {
	cacheKey := fmt.Sprintf("%s:%s:%s", actor.TenantID, actor.ActorID, key)
	if v, ok := s.userSettingCache.Load(cacheKey); ok {
		entry, ok := v.(*cachedUserSetting)
		if !ok {
			s.userSettingCache.Delete(cacheKey)
			return s.settingsRepo.GetActorSettingValue(ctx, actor, key)
		}
		if time.Now().Before(entry.expiresAt) {
			if !entry.valid {
				return "", errCategory5CEE377763
			}
			return entry.value, nil
		}
		s.userSettingCache.Delete(cacheKey)
	}
	val, err := s.settingsRepo.GetActorSettingValue(ctx, actor, key)
	if err != nil {
		s.userSettingCache.Store(cacheKey, &cachedUserSetting{valid: false, expiresAt: time.Now().Add(userSettingCacheTTL)})
		return "", err
	}
	s.userSettingCache.Store(cacheKey, &cachedUserSetting{value: val, valid: true, expiresAt: time.Now().Add(userSettingCacheTTL)})
	return val, nil
}

// getCachedUserMemories 从内存缓存读取用户长期记忆，未命中时回退到 DB 查询。
func (s *Engine) getCachedUserMemories(ctx context.Context, actor domain.ActorRef) ([]MemoryItem, error) {
	cacheKey := actor.TenantID + ":" + actor.ActorID
	if v, ok := s.userMemCache.Load(cacheKey); ok {
		entry, ok := v.(*cachedUserMemories)
		if !ok {
			s.userMemCache.Delete(cacheKey)
			return s.memoryRecorder.ListUserMemories(ctx, actor)
		}
		if time.Now().Before(entry.expiresAt) {
			return entry.memories, nil
		}
		s.userMemCache.Delete(cacheKey)
	}
	mems, err := s.memoryRecorder.ListUserMemories(ctx, actor)
	if err != nil {
		return nil, err
	}
	s.userMemCache.Store(cacheKey, &cachedUserMemories{
		memories:  mems,
		expiresAt: time.Now().Add(userMemCacheTTL),
	})
	return mems, nil
}

func (s *Engine) startInMemoryCacheCleanupWorker(ctx context.Context) {
	if s == nil {
		return
	}
	ticker := time.NewTicker(inMemoryCacheSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.cleanupExpiredInMemoryCaches(now)
		}
	}
}

func (s *Engine) cleanupExpiredInMemoryCaches(now time.Time) {
	s.userMemCache.Range(func(key, value interface{}) bool {
		entry, ok := value.(*cachedUserMemories)
		if !ok || !now.Before(entry.expiresAt) {
			s.userMemCache.Delete(key)
		}
		return true
	})
	s.userSettingCache.Range(func(key, value interface{}) bool {
		entry, ok := value.(*cachedUserSetting)
		if !ok || !now.Before(entry.expiresAt) {
			s.userSettingCache.Delete(key)
		}
		return true
	})
}

var (
	errCategory5CEE377763 = errors.New("not found")
)
