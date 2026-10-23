package redis

import (
	"encoding/json"
	"errors"
	"testing"

	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
)

func TestRedisQueueLargeIntegerPayloadConflict(t *testing.T) {
	t.Parallel()
	queue, _ := newRedisQueueTest(t)
	request := queuecore.EnqueueRequest{
		Queue: redisQueueName, ClientJobID: "large-integer", Kind: "test",
		Payload: json.RawMessage(`{"id":9007199254740993}`),
	}
	first, err := queue.Enqueue(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := queue.Get(t.Context(), first.Job.ID)
	if err != nil || string(stored.Payload) != string(request.Payload) {
		t.Fatalf("persisted payload changed: %s, err=%v", stored.Payload, err)
	}
	request.Payload = json.RawMessage(`{ "id": 9007199254740993 }`)
	replay, err := queue.Enqueue(t.Context(), request)
	if err != nil || !replay.Reused || replay.Job.Fingerprint != first.Job.Fingerprint {
		t.Fatalf("identical large integer was not reused: %#v, err=%v", replay, err)
	}
	request.Payload = json.RawMessage(`{"id":9007199254740992}`)
	if _, err = queue.Enqueue(t.Context(), request); !errors.Is(err, queuecore.ErrConflict) {
		t.Fatalf("different large integer must conflict, got %v", err)
	}
	delivery := claimRedisOne(t, queue, "number-worker")
	if string(delivery.Job.Payload) != string(first.Job.Payload) {
		t.Fatalf("delivery payload changed: %s", delivery.Job.Payload)
	}
}
