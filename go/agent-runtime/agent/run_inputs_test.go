package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestRunInputsDuringModelApplyBeforeFinalCompletion(t *testing.T) {
	rt, _ := newTestRuntimeAndApprovals(t)
	queue, err := agent.NewMemoryRunInputs("test-inputs-v1")
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan string, 1), make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	calls := 0
	client := progressModelFunc(func(ctx context.Context, request model.Request) (model.Response, error) {
		calls++
		if calls == 1 {
			entered <- request.RunID
			select {
			case <-release:
			case <-ctx.Done():
				return model.Response{}, ctx.Err()
			}
			if len(request.Messages) != 1 {
				t.Error("already-dispatched request was mutated")
			}
			return model.Response{Content: "old direction"}, nil
		}
		last := request.Messages[len(request.Messages)-1]
		if last.Role != model.RoleUser || model.TextValue(last.Content) != "Use the new direction instead" {
			t.Errorf("queued user input missing: %+v", request.Messages)
		}
		return model.Response{Content: "new direction completed"}, nil
	})
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: client, Inputs: queue})
	if err != nil {
		t.Fatal(err)
	}
	if runner.NativeStartCancellationFencedByKernel(rt) {
		t.Fatal("input-enabled Runner borrowed the no-Host-callback ownership shortcut")
	}
	type outcome struct {
		snapshot kernel.Snapshot
		err      error
	}
	result := make(chan outcome, 1)
	go func() {
		snapshot, callErr := runner.StartRun(ctx, startRequest("input-run", "input-request", "old direction"))
		result <- outcome{snapshot, callErr}
	}()
	var id string
	select {
	case id = <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	active, err := rt.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := queue.Submit(ctx, active.Run, "input-1", "Use the new direction instead")
	if err != nil || queued.Sequence != 1 {
		t.Fatalf("submit failed: %+v %v", queued, err)
	}
	close(release)
	var final outcome
	select {
	case final = <-result:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	view, viewErr := agent.ViewState(final.snapshot)
	if final.err != nil || viewErr != nil || final.snapshot.Run.Status != kernel.RunStatusCompleted || calls != 2 || view.InputCursor != 1 || view.InputBinding != "test-inputs-v1" ||
		final.snapshot.Result == nil || !strings.Contains(string(final.snapshot.Result.Content), "new direction completed") {
		t.Fatalf("steering failed: status=%s err=%v viewErr=%v calls=%d cursor=%d", final.snapshot.Run.Status, final.err, viewErr, calls, view.InputCursor)
	}
}

func TestRunInputQueueIsScopedIdempotentAndDetectsLostHistory(t *testing.T) {
	queue, _ := agent.NewMemoryRunInputs("inputs-v1")
	run := kernel.Run{ID: "run", Kind: agent.RunKind, Status: kernel.RunStatusRunning,
		Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "chat", ID: "thread"}}
	first, err := queue.Submit(t.Context(), run, "id-1", "new text")
	if err != nil {
		t.Fatal(err)
	}
	again, err := queue.Submit(t.Context(), run, "id-1", "new text")
	if err != nil || first != again {
		t.Fatal("idempotent submission changed its identity")
	}
	if _, err = queue.Submit(t.Context(), run, "id-1", "changed text"); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("same ID changed original text: %v", err)
	}
	foreign := run
	foreign.Actor.ActorID = "other-owner"
	if items, err := queue.ReadRunInputs(t.Context(), foreign, 0, 16); err != nil || len(items) != 0 {
		t.Fatalf("input crossed Actor scope: %v %+v", err, items)
	}
	if items, err := queue.ReadRunInputs(t.Context(), run, 1, 16); err != nil || len(items) != 0 {
		t.Fatalf("consumed input redelivered: %v %+v", err, items)
	}
	empty, _ := agent.NewMemoryRunInputs("inputs-v1")
	if _, err = empty.ReadRunInputs(t.Context(), run, 1, 16); !errors.Is(err, agent.ErrInvalidRequest) {
		t.Fatalf("lost memory history was treated as empty success: %v", err)
	}
	run.Status = kernel.RunStatusCompleted
	if _, err = queue.Submit(t.Context(), run, "late", "new request"); !errors.Is(err, agent.ErrInvalidRequest) {
		t.Fatalf("terminal Run accepted known-late input: %v", err)
	}
}

func TestRunInputsDrainQueuedPagesBeforeFirstModelCall(t *testing.T) {
	for _, tc := range []struct {
		name  string
		count int
		text  string
	}{
		{"record limit", 16, "queued"},
		{"byte limit", 4, strings.Repeat("x", 16<<10)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := newTestRuntimeAndApprovals(t)
			queue, _ := agent.NewMemoryRunInputs("inputs-v1")
			calls := 0
			client := progressModelFunc(func(_ context.Context, request model.Request) (model.Response, error) {
				calls++
				last := request.Messages[len(request.Messages)-1]
				if last.Role != model.RoleUser || model.TextValue(last.Content) != "last update" {
					t.Error("model did not receive the last queued update")
				}
				return model.Response{Content: "done"}, nil
			})
			runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: client, Inputs: queue,
				Limits: agent.Limits{MaxLLMCalls: 1, MaxToolCalls: 1}})
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := runner.PrepareRun(t.Context(), startRequest("paged-run", "request", "start"))
			if err != nil {
				t.Fatal(err)
			}
			for i := range tc.count {
				if _, err := queue.Submit(t.Context(), prepared.Run, fmt.Sprint(i), tc.text); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := queue.Submit(t.Context(), prepared.Run, "last", "last update"); err != nil {
				t.Fatal(err)
			}
			completed, err := runner.Resume(t.Context(), prepared.Run.ID, prepared.Run.Revision)
			view, viewErr := agent.ViewState(completed)
			if err != nil || viewErr != nil || completed.Run.Status != kernel.RunStatusCompleted || calls != 1 || view.InputCursor != uint64(tc.count+1) {
				t.Fatalf("resume: %v view: %v status=%s calls=%d cursor=%d", err, viewErr, completed.Run.Status, calls, view.InputCursor)
			}
		})
	}
}

func TestRunInputsDoNotRaiseModelBudget(t *testing.T) {
	rt, _ := newTestRuntimeAndApprovals(t)
	queue, _ := agent.NewMemoryRunInputs("inputs-v1")
	calls := 0
	client := progressModelFunc(func(ctx context.Context, request model.Request) (model.Response, error) {
		calls++
		snapshot, err := rt.Load(ctx, request.RunID)
		if err != nil {
			return model.Response{}, err
		}
		if _, err = queue.Submit(ctx, snapshot.Run, "input-1", "more work"); err != nil {
			return model.Response{}, err
		}
		return model.Response{Content: "first response"}, nil
	})
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: client, Inputs: queue, Limits: agent.Limits{MaxLLMCalls: 1, MaxToolCalls: 1}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.StartRun(t.Context(), startRequest("input-limit", "request", "start"))
	if !errors.Is(err, agent.ErrCallLimit) || calls != 1 || result.Run.Status != kernel.RunStatusFailed {
		t.Fatalf("input widened Model budget: %v calls=%d status=%s", err, calls, result.Run.Status)
	}
}

func TestRunInputsNeverBypassUnknownToolOutcome(t *testing.T) {
	rt, _ := newTestRuntimeAndApprovals(t)
	queue, _ := agent.NewMemoryRunInputs("inputs-v1")
	registry := progressRegistry(t, tools.HandlerFunc(func(ctx context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
		snapshot, err := rt.Load(ctx, request.RunID)
		if err != nil {
			return tools.ExecutionResult{}, err
		}
		if _, err = queue.Submit(ctx, snapshot.Run, "input-1", "change direction"); err != nil {
			return tools.ExecutionResult{}, err
		}
		return tools.ExecutionResult{}, errors.New("unknown external outcome")
	}))
	client := progressModelFunc(func(context.Context, model.Request) (model.Response, error) {
		return model.Response{ToolCalls: []tools.Call{{ID: "read", ToolKey: "lookup", Arguments: json.RawMessage(`{}`)}}}, nil
	})
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: client, Catalog: registry, Executor: registry, Inputs: queue})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.StartRun(t.Context(), startRequest("input-unknown", "request", "read", "lookup"))
	if !errors.Is(err, agent.ErrToolOutcomeUnknown) {
		t.Fatal(err)
	}
	result, err = runner.Resume(t.Context(), result.Run.ID, result.Run.Revision)
	view, viewErr := agent.ViewState(result)
	if !errors.Is(err, agent.ErrToolOutcomeUnknown) || viewErr != nil || view.InputCursor != 0 {
		t.Fatalf("input bypassed unknown Tool: %v view=%v cursor=%d", err, viewErr, view.InputCursor)
	}
}

func TestRunInputBindingIsFrozenAcrossResume(t *testing.T) {
	rt, _ := newTestRuntimeAndApprovals(t)
	queue, _ := agent.NewMemoryRunInputs("original-inputs")
	other, _ := agent.NewMemoryRunInputs("other-inputs")
	calls := 0
	client := progressModelFunc(func(context.Context, model.Request) (model.Response, error) {
		calls++
		return model.Response{Content: "done"}, nil
	})
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: client, Inputs: queue})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := runner.PrepareRun(t.Context(), startRequest("input-binding", "request", "start"))
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: client, Inputs: other})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.Resume(t.Context(), prepared.Run.ID, prepared.Run.Revision); !errors.Is(err, agent.ErrInvalidRequest) || calls != 0 {
		t.Fatalf("changed input binding reached Model: %v calls=%d", err, calls)
	}
}

func TestRunInputCursorSurvivesRunnerRecreationWithoutRedelivery(t *testing.T) {
	rt, _ := newTestRuntimeAndApprovals(t)
	queue, _ := agent.NewMemoryRunInputs("inputs-v1")
	calls := 0
	client := progressModelFunc(func(_ context.Context, request model.Request) (model.Response, error) {
		calls++
		seen := 0
		for _, message := range request.Messages {
			if message.Role == model.RoleUser && model.TextValue(message.Content) == "queued change" {
				seen++
			}
		}
		if seen != 1 {
			t.Errorf("queued input delivered %d times", seen)
		}
		return model.Response{Content: "answer", Continue: calls == 1}, nil
	})
	dependencies := agent.Dependencies{Runtime: rt, Model: client, Inputs: queue}
	runner, err := agent.NewRunner(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := runner.PrepareRun(t.Context(), startRequest("cursor-run", "request", "start"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = queue.Submit(t.Context(), prepared.Run, "input-1", "queued change"); err != nil {
		t.Fatal(err)
	}
	paused, err := runner.Resume(t.Context(), prepared.Run.ID, prepared.Run.Revision)
	if err != nil || paused.Run.Status != kernel.RunStatusRunning || calls != 1 {
		t.Fatalf("first segment failed: %s %v calls=%d", paused.Run.Status, err, calls)
	}
	reopened, err := agent.NewRunner(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := reopened.Resume(t.Context(), paused.Run.ID, paused.Run.Revision)
	view, viewErr := agent.ViewState(completed)
	if err != nil || viewErr != nil || completed.Run.Status != kernel.RunStatusCompleted || calls != 2 || view.InputCursor != 1 {
		t.Fatalf("input recovery failed: %v viewErr=%v status=%s calls=%d cursor=%d", err, viewErr, completed.Run.Status, calls, view.InputCursor)
	}
}

func TestRunInputQueueCapacityIsNotAConsumedRunBudget(t *testing.T) {
	queue, _ := agent.NewMemoryRunInputs("inputs-v1")
	run := kernel.Run{ID: "run", Kind: agent.RunKind, Status: kernel.RunStatusRunning,
		Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "chat", ID: "thread"}}
	text := strings.Repeat("x", 16<<10)
	for index := range 64 {
		if _, err := queue.Submit(t.Context(), run, fmt.Sprintf("id-%d", index), text); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := queue.Submit(t.Context(), run, "overflow", "next"); !errors.Is(err, agent.ErrInputQueueFull) || errors.Is(err, agent.ErrBudgetExceeded) {
		t.Fatalf("queue capacity was confused with Run usage: %v", err)
	}
	items, err := queue.ReadRunInputs(t.Context(), run, 0, 16)
	if err != nil || len(items) != 4 {
		t.Fatalf("source exceeded its 64 KiB batch bound: count=%d err=%v", len(items), err)
	}
	run.Status = kernel.RunStatusFailed
	if err := queue.ReleaseRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	run.ID, run.Status = "next", kernel.RunStatusRunning
	for index := range 64 {
		if _, err := queue.Submit(t.Context(), run, fmt.Sprint(index), text); err != nil {
			t.Fatalf("retired text still consumes capacity at record %d: %v", index, err)
		}
	}
}

func TestRunInputQueueReleasesOnlyTheTerminalScope(t *testing.T) {
	queue, _ := agent.NewMemoryRunInputs("inputs-v1")
	run := kernel.Run{Kind: agent.RunKind, Status: kernel.RunStatusRunning,
		Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "chat", ID: "thread"}}
	for i := range 256 {
		run.ID = fmt.Sprint(i)
		if _, err := queue.Submit(t.Context(), run, "id", "queued"); err != nil {
			t.Fatal(err)
		}
	}
	next := run
	next.ID = "next"
	for _, status := range []kernel.RunStatus{kernel.RunStatusRunning, kernel.RunStatusWaitingInput, kernel.RunStatusPausedBudget} {
		run.Status = status
		if err := queue.ReleaseRun(t.Context(), run); !errors.Is(err, agent.ErrInvalidRequest) {
			t.Fatalf("release of %s run: %v", status, err)
		}
	}
	run.Status = kernel.RunStatusCancelled
	foreign := run
	foreign.Actor.ActorID = "other"
	if err := queue.ReleaseRun(t.Context(), foreign); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Submit(t.Context(), next, "id", "queued"); !errors.Is(err, agent.ErrInputQueueFull) {
		t.Fatalf("release of another scope freed a slot: %v", err)
	}
	for range 2 {
		if err := queue.ReleaseRun(t.Context(), run); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := queue.ReadRunInputs(t.Context(), run, 1, 16); !errors.Is(err, agent.ErrInvalidRequest) {
		t.Fatalf("retired history accepted a previous cursor: %v", err)
	}
	if receipt, err := queue.Submit(t.Context(), next, "id", "queued"); err != nil || receipt.Sequence != 1 {
		t.Fatalf("new run could not use released slot: %+v %v", receipt, err)
	}
	next.Status = kernel.RunStatusCompleted
	if err := queue.ReleaseRun(t.Context(), next); err != nil {
		t.Fatal(err)
	}
}

var errRunInputACKLost = errors.New("fixture input CAS ACK lost")

type runInputACKLossStore struct {
	*memory.Store
	lost atomic.Bool
}

func (store *runInputACKLossStore) ApplyExecution(ctx context.Context, lease kernel.ExecutionLease, revision uint64, mutation kernel.StoreMutation) (kernel.Snapshot, error) {
	result, err := store.Store.ApplyExecution(ctx, lease, revision, mutation)
	if err != nil {
		return result, err
	}
	for _, event := range mutation.Events {
		if event.Type == "agent.input_applied" && store.lost.CompareAndSwap(false, true) {
			return kernel.Snapshot{}, errRunInputACKLost
		}
	}
	return result, nil
}

func TestRunInputCommittedCursorACKLossDoesNotRedeliverOrDispatchEarly(t *testing.T) {
	store := &runInputACKLossStore{Store: memory.NewStore()}
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	queue, _ := agent.NewMemoryRunInputs("inputs-v1")
	calls := 0
	client := progressModelFunc(func(_ context.Context, request model.Request) (model.Response, error) {
		calls++
		count := 0
		for _, message := range request.Messages {
			if model.TextValue(message.Content) == "one input" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("committed input delivered %d times", count)
		}
		return model.Response{Content: "done"}, nil
	})
	dependencies := agent.Dependencies{Runtime: rt, Model: client, Inputs: queue}
	runner, err := agent.NewRunner(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := runner.PrepareRun(t.Context(), startRequest("input-ack", "request", "start"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = queue.Submit(t.Context(), prepared.Run, "input-1", "one input"); err != nil {
		t.Fatal(err)
	}
	if _, err = runner.Resume(t.Context(), prepared.Run.ID, prepared.Run.Revision); !store.lost.Load() || !errors.Is(err, errRunInputACKLost) || calls != 0 {
		t.Fatalf("lost input ACK reached Model or was hidden: %v calls=%d", err, calls)
	}
	current, err := rt.Load(t.Context(), prepared.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := agent.ViewState(current)
	if err != nil || view.InputCursor != 1 {
		t.Fatalf("input cursor did not commit atomically: %v cursor=%d", err, view.InputCursor)
	}
	reopened, err := agent.NewRunner(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := reopened.Resume(t.Context(), current.Run.ID, current.Run.Revision)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted || calls != 1 {
		t.Fatalf("could not recover original input once: %v status=%s calls=%d", err, completed.Run.Status, calls)
	}
}

var errRunInputHostTail = errors.New("fixture unknown Host middleware tail")

type inputHostTailMiddleware struct{}

func (inputHostTailMiddleware) Name() string { return "fixture.input-host-tail" }
func (inputHostTailMiddleware) Run(ctx context.Context, _ plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	result, err := next(ctx)
	if err != nil {
		return result, err
	}
	return result, errRunInputHostTail
}

func TestRunInputBoundaryCannotSettleUnrelatedHostCallbackFailure(t *testing.T) {
	rt, _ := newTestRuntimeAndApprovals(t)
	queue, _ := agent.NewMemoryRunInputs("inputs-v1")
	client := progressModelFunc(func(context.Context, model.Request) (model.Response, error) {
		return model.Response{Content: "done"}, nil
	})
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: client, Inputs: queue,
		RunMiddleware: []plugin.RunMiddleware{inputHostTailMiddleware{}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.StartRun(t.Context(), startRequest("input-host-tail", "request", "start"))
	if !errors.Is(err, errRunInputHostTail) || !errors.Is(err, agent.ErrRootOutcomeUnknown) {
		t.Fatalf("input frame upgraded unrelated Host failure: %v", err)
	}
	if _, err = rt.LookupChildExecution(t.Context(), result.Run.ID); err != nil {
		t.Fatalf("unsettled original callback grant was retired: %v", err)
	}
}
