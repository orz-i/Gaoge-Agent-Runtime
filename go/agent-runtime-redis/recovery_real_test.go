package redis

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	goredis "github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
)

func TestRealRedisExpiredLeaseRecoveryFencesStaleWorker(t *testing.T) {
	address := strings.TrimSpace(os.Getenv("TEST_REDIS_ADDR"))
	if address == "" {
		t.Skip("TEST_REDIS_ADDR is not configured")
	}
	prefix := "recovery:" + uuid.NewString() + ":"
	clock := &redisQueueClock{now: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	connect := func() (*DeliveryQueue, *goredis.Client) {
		client := goredis.NewClient(&goredis.Options{Addr: address})
		t.Cleanup(func() { _ = client.Close() })
		if err := client.Ping(t.Context()).Err(); err != nil {
			t.Fatal(err)
		}
		return NewQueue(client, QueueOptions{KeyPrefix: prefix, Clock: clock}), client
	}
	first, firstClient := connect()
	request := queuecore.EnqueueRequest{
		Queue: redisQueueName, ClientJobID: "recover-after-claim", Kind: "workflow.resume",
		Payload: json.RawMessage(`{"runID":"run-1","revision":2}`),
		Policy:  queuecore.Policy{MaxAttempts: 3, VisibilityTimeout: 5 * time.Second},
	}
	enqueued, err := first.Enqueue(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	old := claimRedisOne(t, first, "lost-worker")
	if err = firstClient.Close(); err != nil {
		t.Fatal(err)
	}

	// Reconstruct the queue from a different Redis connection without Ack.
	replacement, replacementClient := connect()
	replayed, err := replacement.Enqueue(t.Context(), request)
	if err != nil || !replayed.Reused || replayed.Job.ID != enqueued.Job.ID {
		t.Fatalf("enqueue replay changed the leased job: %#v, %v", replayed, err)
	}
	assertRedisNoClaim(t, replacement, "early-worker")
	clock.Advance(5 * time.Second)
	if _, err = replacement.Ack(t.Context(), redisLeaseRequest(old)); !errors.Is(err, queuecore.ErrLeaseExpired) {
		t.Fatalf("expired worker acknowledgement = %v", err)
	}
	current := claimRedisOne(t, replacement, "replacement-worker")
	if current.Job.ID != enqueued.Job.ID || current.Lease.Generation != 2 || current.Lease.Attempt != 2 ||
		current.Lease.ID == old.Lease.ID || string(current.Job.Payload) != string(enqueued.Job.Payload) {
		t.Fatalf("redelivery changed identity or payload: old=%#v current=%#v", old, current)
	}
	stale := redisLeaseRequest(old)
	for name, operation := range map[string]func() error{
		"ack": func() error {
			_, err := replacement.Ack(t.Context(), stale)
			return err
		},
		"renew": func() error {
			_, err := replacement.Renew(t.Context(), stale)
			return err
		},
		"nack": func() error {
			_, err := replacement.Nack(t.Context(), queuecore.NackRequest{LeaseRequest: stale})
			return err
		},
	} {
		if err = operation(); !errors.Is(err, queuecore.ErrLeaseLost) {
			t.Fatalf("stale worker %s = %v", name, err)
		}
	}
	if err = replacementClient.Close(); err != nil {
		t.Fatal(err)
	}

	// The new lease remains authoritative after both clients are gone.
	restarted, _ := connect()
	completed, err := restarted.Ack(t.Context(), redisLeaseRequest(current))
	if err != nil || completed.Status != queuecore.StatusCompleted || completed.Attempt != 2 {
		t.Fatalf("complete recovered delivery = %#v, %v", completed, err)
	}
	replayed, err = restarted.Enqueue(t.Context(), request)
	if err != nil || !replayed.Reused || replayed.Job.ID != completed.ID || replayed.Job.Status != queuecore.StatusCompleted {
		t.Fatalf("completed enqueue replay = %#v, %v", replayed, err)
	}
	assertRedisNoClaim(t, restarted, "after-completion")
}
