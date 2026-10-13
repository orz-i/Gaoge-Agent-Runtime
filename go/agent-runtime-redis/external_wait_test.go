package redis

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	goredis "github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
)

func TestContinuationWaitGenerationSurvivesQueueReconstruction(t *testing.T) {
	first, clock := newRedisQueueTest(t)
	verifyWaitGenerationRecovery(t, first, clock, func() *DeliveryQueue {
		return NewQueue(first.client, QueueOptions{KeyPrefix: "test:", Clock: clock})
	})
}

func TestRealRedisContinuationWaitGenerationSurvivesQueueReconstruction(t *testing.T) {
	address := strings.TrimSpace(os.Getenv("TEST_REDIS_ADDR"))
	if address == "" {
		t.Skip("TEST_REDIS_ADDR is not configured")
	}
	clock := &redisQueueClock{now: time.Now().UTC()}
	prefix := "integration:wait:" + uuid.NewString() + ":"
	newQueue := func() *DeliveryQueue {
		client := goredis.NewClient(&goredis.Options{Addr: address})
		t.Cleanup(func() { _ = client.Close() })
		if err := client.Ping(t.Context()).Err(); err != nil {
			t.Fatalf("connect to real Redis: %v", err)
		}
		return NewQueue(client, QueueOptions{KeyPrefix: prefix, Clock: clock})
	}
	verifyWaitGenerationRecovery(t, newQueue(), clock, newQueue)
}

func verifyWaitGenerationRecovery(t *testing.T, first *DeliveryQueue, clock *redisQueueClock, restart func() *DeliveryQueue) {
	t.Helper()
	payload := continuation.Payload{
		SchemaVersion: continuation.SchemaVersion, RunID: "external-wait", ExpectedRevision: 8,
		Trigger: continuation.TriggerToolWaitDeadline, SourceRunID: "external-wait", SourceRevision: 8, WaitGeneration: 5,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	_, err = first.Enqueue(t.Context(), queuecore.EnqueueRequest{
		Queue: continuation.QueueName, ClientJobID: "deadline-external-wait", Kind: continuation.JobKind,
		Payload: encoded, AvailableAt: clock.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted := restart()
	before, err := restarted.Claim(t.Context(), queuecore.ClaimRequest{Queue: continuation.QueueName, WorkerID: "wait-worker", Limit: 1})
	if err != nil || len(before) != 0 {
		t.Fatalf("deadline dispatched early: %#v, %v", before, err)
	}
	clock.Advance(time.Minute)
	deliveries, err := restarted.Claim(t.Context(), queuecore.ClaimRequest{Queue: continuation.QueueName, WorkerID: "wait-worker", Limit: 1})
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("deadline delivery=%#v, %v", deliveries, err)
	}
	var recovered continuation.Payload
	if err = json.Unmarshal(deliveries[0].Job.Payload, &recovered); err != nil || recovered != payload {
		t.Fatalf("lost wait generation: %#v, %v", recovered, err)
	}
}
