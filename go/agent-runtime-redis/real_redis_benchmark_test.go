package redis

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	goredis "github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runfeed"
)

func BenchmarkRealRedisQueueRoundTrip(b *testing.B) {
	client, prefix := benchmarkRealRedisClient(b)
	queue := NewQueue(client, QueueOptions{KeyPrefix: prefix})
	b.ReportAllocs()
	b.ResetTimer()
	for index := range b.N {
		enqueued, err := queue.Enqueue(context.Background(), queuecore.EnqueueRequest{
			Queue: "benchmark", ClientJobID: "job-" + strconv.Itoa(index), Kind: "benchmark.job",
			Payload: json.RawMessage(`{"ok":true}`),
			Policy:  queuecore.Policy{MaxAttempts: 2, VisibilityTimeout: 30 * time.Second},
		})
		if err != nil {
			b.Fatal(err)
		}
		deliveries, err := queue.Claim(context.Background(), queuecore.ClaimRequest{
			Queue: "benchmark", WorkerID: "benchmark-worker", Limit: 1,
		})
		if err != nil || len(deliveries) != 1 || deliveries[0].Job.ID != enqueued.Job.ID {
			b.Fatalf("deliveries=%#v err=%v", deliveries, err)
		}
		if _, err = queue.Ack(context.Background(), queuecore.LeaseRequest{
			JobID: deliveries[0].Job.ID, LeaseID: deliveries[0].Lease.ID, WorkerID: deliveries[0].Lease.WorkerID,
		}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRealRedisRunFeedAppend(b *testing.B) {
	client, prefix := benchmarkRealRedisClient(b)
	store := NewRunFeedStore(client, RunFeedOptions{KeyPrefix: prefix})
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	b.ReportAllocs()
	b.ResetTimer()
	for index := range b.N {
		event, err := store.Append(context.Background(), "benchmark-run", runfeed.Draft{
			Type: "benchmark.event", Delta: strconv.Itoa(index), Revision: uint64(index + 1),
		}, now, time.Minute)
		if err != nil || event.Seq != int64(index+1) {
			b.Fatalf("event=%#v err=%v", event, err)
		}
	}
}

func benchmarkRealRedisClient(b *testing.B) (*goredis.Client, string) {
	b.Helper()
	address := strings.TrimSpace(os.Getenv("TEST_REDIS_ADDR"))
	if address == "" {
		b.Skip("TEST_REDIS_ADDR is not configured")
	}
	client := goredis.NewClient(&goredis.Options{Addr: address})
	if err := client.Ping(context.Background()).Err(); err != nil {
		b.Fatal(err)
	}
	prefix := "benchmark:" + strings.ReplaceAll(uuid.NewString(), "-", "") + ":"
	b.Cleanup(func() {
		ctx := context.Background()
		keys := make([]string, 0)
		iterator := client.Scan(ctx, 0, prefix+"*", 0).Iterator()
		for iterator.Next(ctx) {
			keys = append(keys, iterator.Val())
		}
		if len(keys) > 0 {
			_ = client.Del(ctx, keys...).Err()
		}
		_ = client.Close()
	})
	return client, prefix
}
