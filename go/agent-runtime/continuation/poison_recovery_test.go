package continuation_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
)

type healthyPoisonNeighborResumer struct{ runtime *kernel.Runtime }

func (resumer healthyPoisonNeighborResumer) Resume(ctx context.Context, id string, revision uint64) (kernel.Snapshot, error) {
	current, err := resumer.runtime.Load(ctx, id)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return resumer.runtime.Apply(ctx, id, revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: current.State,
		Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage(`true`)},
	})
}

func enqueuePoisonRecovery(t *testing.T, queue *queuecore.Memory, snapshot kernel.Snapshot, priority int) string {
	t.Helper()
	payload, err := json.Marshal(continuation.Payload{
		RunID: snapshot.Run.ID, ExpectedRevision: snapshot.Run.Revision,
		Trigger:     continuation.TriggerRunReady,
		SourceRunID: snapshot.Run.ID, SourceRevision: snapshot.Run.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := queue.Enqueue(t.Context(), queuecore.EnqueueRequest{
		Queue: continuation.QueueName, ClientJobID: snapshot.Run.ID,
		Kind: continuation.JobKind, Payload: payload, Priority: priority,
		Policy: queuecore.Policy{
			MaxAttempts: 3, InitialBackoff: time.Minute,
			VisibilityTimeout: 250 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.Job.ID
}

// An unreadable Run Status must never be mistaken for a stale wakeup. Valid
// JSON with invalid Feature-owned Agent state must also remain retryable,
// while an unrelated healthy Run continues on the same real Queue Worker.
func TestRunSemanticPoisonNACKDoesNotStarveHealthyRun(t *testing.T) {
	for _, test := range []struct {
		name          string
		corruptStatus bool
	}{
		{name: "unknown persisted status", corruptStatus: true},
		{name: "invalid agent state"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := memory.NewStore()
			runtime := newRuntimeWithStore(t, store)
			poisonKind := agent.RunKind
			if test.corruptStatus {
				poisonKind = "test.invalid_status"
			}
			poison := createRun(t, runtime, "poison-run", poisonKind)
			if test.corruptStatus {
				// Simulate a semantically poisoned authoritative Store record
				// without bypassing the public Runtime's validation rules.
				damaged := poison.Run
				damaged.Status = kernel.RunStatus("unexpected_status")
				damaged.Revision++
				var storeErr error
				poison, storeErr = store.Apply(t.Context(), damaged.ID, damaged.Revision-1, kernel.StoreMutation{
					Record: kernel.Record{Run: damaged, State: poison.State},
				})
				if storeErr != nil {
					t.Fatal(storeErr)
				}
			}
			healthy := createRun(t, runtime, "healthy-run", "test.healthy")
			healthyResumer := healthyPoisonNeighborResumer{runtime: runtime}
			var registrations []continuation.ResumerRegistration
			if test.corruptStatus {
				registrations = append(registrations, continuation.RegisterResumer(poisonKind,
					&recordingResumer{snapshot: poison}))
			} else {
				agentRunner, _ := newIntegrationAgent(t, runtime, newIntegrationToolRegistry(t, &atomic.Int32{}))
				registrations = append(registrations, continuation.RegisterResumer(poisonKind, agentRunner))
			}
			registrations = append(registrations, continuation.RegisterResumer(healthy.Run.Kind, healthyResumer))
			dispatcher, err := continuation.NewDispatcher(runtime, registrations...)
			if err != nil {
				t.Fatal(err)
			}
			queue := queuecore.NewMemory(queuecore.Dependencies{})
			poisonJobID := enqueuePoisonRecovery(t, queue, poison, 10)
			healthyJobID := enqueuePoisonRecovery(t, queue, healthy, 0)
			worker, err := continuation.NewWorker(queue, dispatcher, continuation.WorkerOptions{
				WorkerID: "poison-isolation-worker", ClaimLimit: 2, PollInterval: 5 * time.Millisecond,
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if err = worker.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = worker.Close(context.Background()) }()
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				bad, badErr := queue.Get(t.Context(), poisonJobID)
				good, goodErr := queue.Get(t.Context(), healthyJobID)
				if badErr != nil || goodErr != nil {
					t.Fatalf("jobs unavailable: %v, %v", badErr, goodErr)
				}
				if bad.Attempt >= 1 && good.Status == queuecore.StatusCompleted {
					if bad.Status != queuecore.StatusQueued || bad.LastErrorCode != "continuation.dispatch_failed" {
						t.Fatalf("poison delivery was incorrectly ACKed: %+v", bad)
					}
					savedBad, badErr := runtime.Load(t.Context(), poison.Run.ID)
					savedGood, goodErr := runtime.Load(t.Context(), healthy.Run.ID)
					if badErr != nil || goodErr != nil ||
						savedBad.Run.Revision != poison.Run.Revision ||
						savedGood.Run.Status != kernel.RunStatusCompleted {
						t.Fatalf("poison mutated or healthy blocked: bad=%+v good=%+v err=%v/%v",
							savedBad.Run, savedGood.Run, badErr, goodErr)
					}
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
			bad, _ := queue.Get(t.Context(), poisonJobID)
			good, _ := queue.Get(t.Context(), healthyJobID)
			t.Fatalf("poison run stalled healthy delivery: bad=%+v good=%+v", bad, good)
		})
	}
}
