package consumer

import (
	goredis "github.com/go-redis/redis/v8"
	redisruntime "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-redis"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runfeed"
)

var (
	_ func(goredis.UniversalClient, redisruntime.QueueOptions) *redisruntime.DeliveryQueue  = redisruntime.NewQueue
	_ func(goredis.UniversalClient, redisruntime.RunFeedOptions) *redisruntime.RunFeedStore = redisruntime.NewRunFeedStore
	_ continuation.DeliveryQueue                                                            = (*redisruntime.DeliveryQueue)(nil)
	_ runfeed.Store                                                                         = (*redisruntime.RunFeedStore)(nil)
)
