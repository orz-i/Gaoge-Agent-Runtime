package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

var errInjectedWorkflowCrash = errors.New("injected workflow crash")

func TestWorkflowRecoversCrashAfterEffectIntentCommitBeforeDispatch(t *testing.T) {
	t.Parallel()
	base := memory.NewStore()
	faults := &workflowFaultStore{Store: base, eventType: "workflow.effect.intents_created"}
	runtime := newWorkflowRuntimeWithStore(t, faults)
	executor := &scriptedExecutor{runtime: runtime, results: []workflow.EffectResult{{
		Disposition: workflow.DispositionCompleted, ReceiptID: "receipt", Output: json.RawMessage(`{"ok":true}`),
	}}}
	runner := newWorkflowRunner(t, runtime, executor)
	definition := compileWorkflow(t, []workflow.Node{
		effectNode("send", "message.send"), returnNode(json.RawMessage(`{"status":"sent"}`)),
	}, workflow.Limits{})
	request := workflowRequest(definition)
	request.ID = "workflow-effect-intent-crash"
	_, err := runner.StartRun(t.Context(), request)
	if !errors.Is(err, errInjectedWorkflowCrash) {
		t.Fatalf("start error = %v", err)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("effect dispatched before recovery: %v", executor.calls)
	}

	restartedRuntime := newWorkflowRuntimeWithStore(t, base)
	executor.runtime = restartedRuntime
	restarted := newWorkflowRunner(t, restartedRuntime, executor)
	crashed, err := restartedRuntime.Load(t.Context(), request.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := restarted.Resume(t.Context(), crashed.Run.ID, crashed.Run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkflowCompleted(t, completed)
	if len(executor.calls) != 1 || !executor.allCallsObservedPersistedIntent() {
		t.Fatalf("effect recovery calls=%v persisted=%v", executor.calls, executor.persisted)
	}
}

type workflowFaultStore struct {
	kernel.Store
	eventType string
	failed    bool
}

func (store *workflowFaultStore) Apply(
	ctx context.Context,
	runID string,
	expectedRevision uint64,
	mutation kernel.StoreMutation,
) (kernel.Snapshot, error) {
	if !store.failed {
		for _, event := range mutation.Events {
			if event.Type == store.eventType {
				store.failed = true
				snapshot, err := store.Store.Apply(ctx, runID, expectedRevision, mutation)
				if err != nil {
					return snapshot, err
				}
				return kernel.Snapshot{}, errInjectedWorkflowCrash
			}
		}
	}
	return store.Store.Apply(ctx, runID, expectedRevision, mutation)
}

func newWorkflowRuntimeWithStore(t *testing.T, store kernel.Store) *kernel.Runtime {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: store, Clock: workflowClock{}, IDs: &workflowIDs{}})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}
