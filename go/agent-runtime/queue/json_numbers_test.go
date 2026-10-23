package queue_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
)

func TestPrepareEnqueuePreservesJSONNumbers(t *testing.T) {
	t.Parallel()
	for _, payload := range []string{
		`9007199254740993`,
		`18446744073709551615`,
		`-9007199254740993`,
		`0.123456789012345678901`,
		`1e400`,
		`{"values":[9007199254740993,18446744073709551615,-9007199254740993,0.123456789012345678901]}`,
	} {
		t.Run(payload, func(t *testing.T) {
			job, err := queue.PrepareEnqueue(queue.EnqueueRequest{
				Queue: queueNameContinuations, ClientJobID: "numbers", Kind: "test",
				Payload: json.RawMessage(payload),
			}, newQueueClock().Now())
			if err != nil || string(job.Payload) != payload {
				t.Fatalf("payload changed: got %s, want %s, err=%v", job.Payload, payload, err)
			}
		})
	}
}

func TestMemoryQueueLargeIntegerPayloadConflict(t *testing.T) {
	t.Parallel()
	memory := queue.NewMemory(queue.Dependencies{Clock: newQueueClock()})
	request := queue.EnqueueRequest{
		Queue: queueNameContinuations, ClientJobID: "large-integer", Kind: "test",
		Payload: json.RawMessage(`{"id":9007199254740993}`),
	}
	first, err := memory.Enqueue(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := memory.Get(t.Context(), first.Job.ID)
	if err != nil || string(stored.Payload) != string(request.Payload) {
		t.Fatalf("stored payload changed: %s, err=%v", stored.Payload, err)
	}
	request.Payload = json.RawMessage(`{ "id": 9007199254740993 }`)
	replay, err := memory.Enqueue(t.Context(), request)
	if err != nil || !replay.Reused || replay.Job.Fingerprint != first.Job.Fingerprint {
		t.Fatalf("identical large integer was not reused: %#v, err=%v", replay, err)
	}
	request.Payload = json.RawMessage(`{"id":9007199254740992}`)
	if _, err = memory.Enqueue(t.Context(), request); !errors.Is(err, queue.ErrConflict) {
		t.Fatalf("different large integer must conflict, got %v", err)
	}
}

func TestPrepareEnqueueRejectsMalformedJSON(t *testing.T) {
	t.Parallel()
	for _, payload := range []string{`{`, `{"id":1} {"id":2}`, `{"id":1}garbage`, `"value" true`, " \t\n"} {
		t.Run(payload, func(t *testing.T) {
			_, err := queue.PrepareEnqueue(queue.EnqueueRequest{
				Queue: queueNameContinuations, ClientJobID: "invalid-json", Kind: "test",
				Payload: json.RawMessage(payload),
			}, newQueueClock().Now())
			if !errors.Is(err, queue.ErrInvalidInput) {
				t.Fatalf("invalid payload %q accepted: %v", payload, err)
			}
		})
	}
}
