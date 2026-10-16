package conformance

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// ExecutionStoreFactory provisions an empty, isolated durable/volatile Store.
// The fixture MUST be a real adapter, not a mocked execution map.
type ExecutionStoreFactory func(*testing.T) kernel.Store

// RunKernelExecutionSuite exercises the opt-in Stage 2 execution-authority
// contract. Time is Store-owned; no user-supplied Now participates in fencing.
func RunKernelExecutionSuite(t *testing.T, factory ExecutionStoreFactory) {
	t.Helper()
	t.Run("epoch-and-multi-cas-segment", func(t *testing.T) {
		ctx := t.Context()
		store := factory(t)
		runtime, err := kernel.New(kernel.Dependencies{Store: store})
		if err != nil {
			t.Fatal(err)
		}
		created, err := runtime.Create(ctx, kernel.CreateRequest{
			ID: "fenced-run", Kind: "agent", Actor: kernel.ActorRef{TenantID: "profile", ActorID: "actor"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "fenced execution", State: json.RawMessage(`{}`),
			Events: []kernel.EventDraft{{Type: "run.wakeup", Wakeup: true}},
		})
		if err != nil {
			t.Fatal(err)
		}
		acquire := func(owner string, ttl time.Duration) kernel.ExecutionLease {
			t.Helper()
			lease, e := runtime.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: created.Run.ID, OwnerID: owner, LeaseDuration: ttl})
			if e != nil {
				t.Fatalf("acquire %q: %v", owner, e)
			}
			return lease
		}
		first := acquire("owner-A", 300*time.Millisecond)
		if first.Epoch != 1 || first.LeaseID == "" {
			t.Fatalf("initial lease invalid: %+v", first)
		}
		mutation := func(current kernel.Snapshot, tag string) kernel.Mutation {
			return kernel.Mutation{Status: kernel.RunStatusRunning, State: json.RawMessage(`{"tag":` + `"` + tag + `"}`), Events: []kernel.EventDraft{{Type: "agent.step"}}}
		}
		if _, err = runtime.Apply(ctx, created.Run.ID, created.Run.Revision, mutation(created, "unfenced")); !errors.Is(err, kernel.ErrExecutionRequired) {
			t.Fatalf("unfenced execution was authorized: %v", err)
		}
		if _, err = runtime.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: created.Run.ID, OwnerID: "owner-B", LeaseDuration: time.Second}); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("active lease was stolen: %v", err)
		}
		bound, err := runtime.WithExecution(first)
		if err != nil {
			t.Fatal(err)
		}
		current := created
		for step := 1; step <= 3; step++ {
			current, err = bound.Apply(ctx, created.Run.ID, current.Run.Revision, mutation(current, "step"))
			if err != nil {
				t.Fatalf("multi-CAS segment step %d: %v", step, err)
			}
		}
		if current.Run.Revision != created.Run.Revision+3 {
			t.Fatalf("multi-CAS run=%+v", current.Run)
		}
		// Renew rotates the expiry in the exact token while retaining Epoch.
		renewed, err := runtime.RenewExecution(ctx, kernel.ExecutionRenewRequest{Lease: first, LeaseDuration: 50 * time.Millisecond})
		if err != nil || renewed.Epoch != first.Epoch || renewed.ExpiresAt.Equal(first.ExpiresAt) {
			t.Fatalf("renewed segment epoch/expiry error: %+v %v", renewed, err)
		}
		if _, err = bound.Apply(ctx, created.Run.ID, current.Run.Revision, mutation(current, "stale-renew-token")); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("old expiry token committed after renewal: %v", err)
		}
		rebound, err := runtime.WithExecution(renewed)
		if err != nil {
			t.Fatal(err)
		}
		current, err = rebound.Apply(ctx, created.Run.ID, current.Run.Revision, mutation(current, "renewed"))
		if err != nil {
			t.Fatal(err)
		}
		// Expired A cannot write even before B reclaims the lease.
		time.Sleep(90 * time.Millisecond)
		if _, err = rebound.Apply(ctx, created.Run.ID, current.Run.Revision, mutation(current, "expired")); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("expired owner without B committed: %v", err)
		}
		revisionAtTakeover := current.Run.Revision
		second := acquire("owner-B", time.Second)
		if second.Epoch != first.Epoch+1 || second.OwnerID != "owner-B" {
			t.Fatalf("takeover did not bump epoch: %+v", second)
		}
		stored, err := runtime.Load(ctx, created.Run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Run.Revision != revisionAtTakeover {
			t.Fatalf("takeover unexpectedly advanced Run revision: %d vs %d", stored.Run.Revision, revisionAtTakeover)
		}
		if _, err = rebound.Apply(ctx, created.Run.ID, revisionAtTakeover, mutation(current, "old-epoch")); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("A committed unchanged Run revision after B takeover: %v", err)
		}
		if err = runtime.ReleaseExecution(ctx, renewed); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("stale owner released new lease: %v", err)
		}
		b, err := runtime.WithExecution(second)
		if err != nil {
			t.Fatal(err)
		}
		current, err = b.Apply(ctx, created.Run.ID, revisionAtTakeover, mutation(current, "owner-B"))
		if err != nil {
			t.Fatal(err)
		}
		if err = runtime.ReleaseExecution(ctx, second); err != nil {
			t.Fatal(err)
		}
		if _, err = runtime.Apply(ctx, created.Run.ID, current.Run.Revision, mutation(current, "released-unfenced")); !errors.Is(err, kernel.ErrExecutionRequired) {
			t.Fatalf("release disabled the sticky execution fence: %v", err)
		}
		third := acquire("owner-C", time.Second)
		if third.Epoch != second.Epoch+1 {
			t.Fatalf("epoch not monotonic after release: %+v", third)
		}
		c, err := runtime.WithExecution(third)
		if err != nil {
			t.Fatal(err)
		}
		completed, err := c.Apply(ctx, created.Run.ID, current.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusCompleted, State: json.RawMessage(`{"result":"done"}`),
			Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage(`{"ok":true}`)},
		})
		if err != nil || completed.Run.Status != kernel.RunStatusCompleted {
			t.Fatalf("terminal fenced apply: %+v %v", completed.Run, err)
		}
		if _, err = runtime.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: created.Run.ID, OwnerID: "revival", LeaseDuration: time.Second}); !errors.Is(err, kernel.ErrTerminal) {
			t.Fatalf("terminal Run obtained execution rights: %v", err)
		}
		events, err := runtime.ListEvents(ctx, created.Run.ID, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 7 {
			t.Fatalf("expected all 7 event facts across one segment, got %d", len(events))
		}
		// Only initial wakeup and final terminal fact should be enqueued.
		claims, err := store.ClaimTransitions(ctx, kernel.TransitionClaimRequest{
			WorkerID: "projection", Limit: 10, LeaseDuration: time.Second, Now: time.Now().Add(time.Second),
		})
		if err != nil || len(claims) != 2 {
			t.Fatalf("unexpected intermediate wakeup jobs %#v %v", claims, err)
		}
	})
	t.Run("pending-gates-cannot-acquire", func(t *testing.T) {
		ctx := t.Context()
		store := factory(t)
		runtime, err := kernel.New(kernel.Dependencies{Store: store})
		if err != nil {
			t.Fatal(err)
		}
		for _, status := range []kernel.RunStatus{kernel.RunStatusWaitingInput, kernel.RunStatusPausedBudget} {
			id := "gate-" + string(status)
			current, err := runtime.Create(ctx, kernel.CreateRequest{
				ID: id, Kind: "agent", Actor: kernel.ActorRef{TenantID: "p", ActorID: "a"},
				Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "gate", State: json.RawMessage(`{}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			mutation := kernel.Mutation{Status: status, State: json.RawMessage(`{}`)}
			if status == kernel.RunStatusWaitingInput {
				mutation.Checkpoint = &kernel.Checkpoint{
					ID: "approval", Kind: "tool_approval", Status: kernel.CheckpointPending,
					Payload: json.RawMessage(`{"callID":"c"}`), CreatedAt: time.Now().UTC(),
				}
			}
			gated, err := runtime.Apply(ctx, id, current.Run.Revision, mutation)
			if err != nil || gated.Run.Status != status {
				t.Fatalf("gate: %+v %v", gated.Run, err)
			}
			_, err = runtime.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: id, OwnerID: "drive-without-approval", LeaseDuration: time.Second})
			if !errors.Is(err, kernel.ErrExecutionConflict) {
				t.Fatalf("%s bypassed by acquire: %v", status, err)
			}
		}
	})

	t.Run("simultaneous-claims-are-exclusive", func(t *testing.T) {
		ctx := t.Context()
		store := factory(t)
		runtime, err := kernel.New(kernel.Dependencies{Store: store})
		if err != nil {
			t.Fatal(err)
		}
		_, err = runtime.Create(ctx, kernel.CreateRequest{
			ID: "exclusive", Kind: "agent", Actor: kernel.ActorRef{TenantID: "p", ActorID: "a"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "competing owners", State: json.RawMessage(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		results := make(chan error, 2)
		var workers sync.WaitGroup
		for _, owner := range []string{"owner-A", "owner-B"} {
			workers.Add(1)
			go func() {
				defer workers.Done()
				<-start
				var claimErr error
				for attempt := 0; attempt < 6; attempt++ {
					_, claimErr = runtime.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{
						RunID: "exclusive", OwnerID: owner, LeaseDuration: 2 * time.Second,
					})
					if !errors.Is(claimErr, kernel.ErrExecutionBusy) {
						break
					}
					// Bounded contention retries are safe: no lease was
					// acquired by this attempt, and no side effect was run.
					time.Sleep(10 * time.Millisecond)
				}
				results <- claimErr
			}()
		}
		close(start)
		workers.Wait()
		close(results)
		successes, conflicts := 0, 0
		for result := range results {
			if result == nil {
				successes++
			} else if errors.Is(result, kernel.ErrExecutionConflict) {
				conflicts++
			} else {
				t.Fatalf("unexpected claim failure: %v", result)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("concurrent claim successes=%d conflicts=%d", successes, conflicts)
		}
	})

	t.Run("fenced-run-cannot-use-legacy-control-bypass", func(t *testing.T) {
		ctx := t.Context()
		store := factory(t)
		runtime, err := kernel.New(kernel.Dependencies{Store: store})
		if err != nil {
			t.Fatal(err)
		}
		created, err := runtime.Create(ctx, kernel.CreateRequest{
			ID: "fenced-gate", Kind: "agent", Actor: kernel.ActorRef{TenantID: "p", ActorID: "a"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "fenced pause", State: json.RawMessage(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		claim, err := runtime.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{
			RunID: created.Run.ID, OwnerID: "worker", LeaseDuration: time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		bound, err := runtime.WithExecution(claim)
		if err != nil {
			t.Fatal(err)
		}
		gated, err := bound.Apply(ctx, created.Run.ID, created.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusWaitingInput, State: json.RawMessage(`{}`),
			Checkpoint: &kernel.Checkpoint{
				ID: "approval", Kind: "tool_approval", Status: kernel.CheckpointPending,
				Payload: json.RawMessage(`{"callID":"c"}`), CreatedAt: time.Now().UTC(),
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		// Stage 2 next phase must add a separately authorized control port.
		// Until then, fail CLOSED rather than letting plain Apply silently
		// bypass the Store epoch guard to "approve" or cancel a Run.
		_, err = runtime.Apply(ctx, created.Run.ID, gated.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: json.RawMessage(`{}`),
		})
		if !errors.Is(err, kernel.ErrExecutionRequired) {
			t.Fatalf("unfenced control bypassed epoch: %v", err)
		}
		_, err = runtime.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{
			RunID: created.Run.ID, OwnerID: "new-worker", LeaseDuration: time.Second,
		})
		if !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("waiting approval was reclaimed as executable: %v", err)
		}
	})

}
