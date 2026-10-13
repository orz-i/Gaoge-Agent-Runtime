package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
	"gorm.io/gorm"
)

type persistedWaitModel struct{}

func (persistedWaitModel) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{ToolCalls: []tools.Call{{ID: "persisted-call", ToolKey: "test.persisted_wait", Arguments: json.RawMessage(`{}`)}}}, nil
}

func TestExternalWaitRecoveryAndOutboxGeneration(t *testing.T) {
	externalWaitStoreRecovery(t, openKernelStoreTestDB(t))
}

func TestRealPostgresExternalWaitRecoveryAndOutboxGeneration(t *testing.T) {
	db, _ := openIsolatedRealPostgres(t, requireRecoveryPostgres(t))
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	externalWaitStoreRecovery(t, db)
}

func externalWaitStoreRecovery(t *testing.T, db *gorm.DB) {
	t.Helper()
	store := NewKernelStore(db)
	ready := false
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: "test.persisted_wait", Name: "persisted_wait", InputSchema: json.RawMessage(`{"type":"object"}`), Terminal: true},
		Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
			if ready {
				return tools.ExecutionResult{Content: json.RawMessage(`42`), Receipt: tools.Receipt{ExecutionID: "persisted-operation", Disposition: "committed"}}, nil
			}
			deadline := time.Now().Add(time.Minute)
			return tools.ExecutionResult{Content: json.RawMessage(`null`), Receipt: tools.Receipt{ExecutionID: "persisted-operation", Disposition: tools.ReceiptDispositionPending},
				Pending: &tools.PendingExecution{Kind: tools.PendingExternalResult, OperationID: "persisted-operation", InputDigest: strings.Repeat("d", 64), DeadlineAt: &deadline}}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	compose := func() *agent.Runner {
		t.Helper()
		runtime, runtimeErr := kernel.New(kernel.Dependencies{Store: NewKernelStore(db)})
		if runtimeErr != nil {
			t.Fatal(runtimeErr)
		}
		runner, runnerErr := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: persistedWaitModel{}, Catalog: registry, Executor: registry})
		if runnerErr != nil {
			t.Fatal(runnerErr)
		}
		return runner
	}
	first := compose()
	waiting, err := first.StartRun(t.Context(), agent.StartRequest{ID: "persisted-wait", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "wait", ToolKeys: []string{"test.persisted_wait"}})
	if err != nil {
		t.Fatal(err)
	}
	// All runner/runtime/store instances are recreated from durable storage.
	restarted := compose()
	wait, err := restarted.QueryPendingTool(t.Context(), waiting.Run.ID, "persisted-call")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimTransitions(t.Context(), kernel.TransitionClaimRequest{WorkerID: "generation-test", Limit: 32, LeaseDuration: time.Minute, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, claim := range claims {
		for _, event := range claim.Transition.Events {
			if event.Type == agent.EventToolWaitDeadline {
				found = event.Wakeup && event.WakeupAt != nil && event.WakeupGeneration == wait.Generation
			}
		}
	}
	if !found {
		t.Fatal("stored transition lost wait generation or deadline")
	}
	ready = true
	signal := agent.ExternalToolResultSignal{RunID: waiting.Run.ID, CallID: "persisted-call", OperationID: "persisted-operation", InputDigest: strings.Repeat("d", 64), SignalID: "persisted-receipt"}
	signalled, err := restarted.NotifyExternalToolResultReady(t.Context(), signal)
	if err != nil {
		t.Fatal(err)
	}
	restarted = compose()
	completed, err := restarted.Resume(t.Context(), signalled.Run.ID, signalled.Run.Revision)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("recovery=%#v, %v", completed.Run, err)
	}
	view, err := agent.ViewState(completed)
	if err != nil || view.Budget.Usage.ToolCalls != 1 {
		t.Fatalf("consumption=%#v, %v", view, err)
	}
	duplicate, err := restarted.NotifyExternalToolResultReady(t.Context(), signal)
	if err != nil || duplicate.Run.Revision != completed.Run.Revision {
		t.Fatalf("duplicate=%#v, %v", duplicate.Run, err)
	}
}
