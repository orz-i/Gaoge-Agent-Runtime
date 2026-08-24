package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	goredis "github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runfeed"
)

func TestRealRedisQueueFeedConcurrencyAndRestart(t *testing.T) {
	address := strings.TrimSpace(os.Getenv("TEST_REDIS_ADDR"))
	if address == "" {
		t.Skip("TEST_REDIS_ADDR is not configured")
	}
	prefix := "integration:" + strings.ReplaceAll(uuid.NewString(), "-", "") + ":"
	clients := make([]*goredis.Client, 0, 4)
	newClient := func() *goredis.Client {
		client := goredis.NewClient(&goredis.Options{Addr: address})
		if err := client.Ping(t.Context()).Err(); err != nil {
			t.Fatalf("connect to real Redis: %v", err)
		}
		clients = append(clients, client)
		return client
	}
	t.Cleanup(func() {
		for _, client := range clients {
			_ = client.Close()
		}
	})

	first := NewQueue(newClient(), QueueOptions{KeyPrefix: prefix})
	second := NewQueue(newClient(), QueueOptions{KeyPrefix: prefix})
	request := queuecore.EnqueueRequest{
		Queue: "continuations", ClientJobID: "restart-job", Kind: "workflow.resume",
		Payload: json.RawMessage(`{"runID":"run-1"}`),
		Policy:  queuecore.Policy{MaxAttempts: 2, VisibilityTimeout: 30 * time.Second},
	}
	enqueued, err := first.Enqueue(t.Context(), request)
	if err != nil || enqueued.Reused {
		t.Fatalf("enqueue real Redis job = %#v, err=%v", enqueued, err)
	}
	replayed, err := second.Enqueue(t.Context(), request)
	if err != nil || !replayed.Reused || replayed.Job.ID != enqueued.Job.ID {
		t.Fatalf("reconstruct idempotent enqueue = %#v, err=%v", replayed, err)
	}

	const workers = 20
	results := make(chan []queuecore.Delivery, workers)
	errorsChannel := make(chan error, workers)
	var group sync.WaitGroup
	for index := range workers {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			queue := first
			if worker%2 == 1 {
				queue = second
			}
			deliveries, claimErr := queue.Claim(context.Background(), queuecore.ClaimRequest{
				Queue: "continuations", WorkerID: fmt.Sprintf("worker-%d", worker), Limit: 1,
			})
			results <- deliveries
			errorsChannel <- claimErr
		}(index)
	}
	group.Wait()
	close(results)
	close(errorsChannel)
	for claimErr := range errorsChannel {
		if claimErr != nil {
			t.Fatalf("real Redis concurrent claim: %v", claimErr)
		}
	}
	var delivery queuecore.Delivery
	claimed := 0
	for deliveries := range results {
		claimed += len(deliveries)
		if len(deliveries) == 1 {
			delivery = deliveries[0]
		}
	}
	if claimed != 1 || delivery.Job.ID != enqueued.Job.ID {
		t.Fatalf("real Redis claimed=%d delivery=%#v", claimed, delivery)
	}

	restarted := NewQueue(newClient(), QueueOptions{KeyPrefix: prefix})
	completed, err := restarted.Ack(t.Context(), queuecore.LeaseRequest{
		JobID: delivery.Job.ID, LeaseID: delivery.Lease.ID, WorkerID: delivery.Lease.WorkerID,
	})
	if err != nil || completed.Status != queuecore.StatusCompleted {
		t.Fatalf("ack after queue reconstruction = %#v, err=%v", completed, err)
	}
	loaded, err := NewQueue(newClient(), QueueOptions{KeyPrefix: prefix}).Get(t.Context(), completed.ID)
	if err != nil || loaded.Status != queuecore.StatusCompleted || loaded.CompletedAt == nil {
		t.Fatalf("load completed job after restart = %#v, err=%v", loaded, err)
	}

	feedOne := NewRunFeedStore(clients[0], RunFeedOptions{KeyPrefix: prefix})
	feedTwo := NewRunFeedStore(clients[1], RunFeedOptions{KeyPrefix: prefix})
	firstEvent, err := feedOne.Append(t.Context(), "run-restart", runfeed.Draft{
		Type: "model.delta", Delta: "hello", Revision: 1,
	}, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	secondEvent, err := feedTwo.Append(t.Context(), "run-restart", runfeed.Draft{
		Type: "run.completed", Status: "completed", Terminal: true, Revision: 2,
	}, time.Now(), time.Minute)
	if err != nil || secondEvent.Seq != firstEvent.Seq+1 {
		t.Fatalf("append feed after reconstruction = %#v, err=%v", secondEvent, err)
	}
	events, err := NewRunFeedStore(clients[2], RunFeedOptions{KeyPrefix: prefix}).List(t.Context(), "run-restart", 0, 10)
	if err != nil || len(events) != 2 || events[0].Delta != "hello" || !events[1].Terminal {
		t.Fatalf("replay feed after reconstruction = %#v, err=%v", events, err)
	}
}
