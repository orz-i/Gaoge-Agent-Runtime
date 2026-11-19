package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
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
	if final.err != nil || viewErr != nil || final.snapshot.Run.Status != kernel.RunStatusCompleted || calls != 2 || view.InputCursor != 1 ||
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
