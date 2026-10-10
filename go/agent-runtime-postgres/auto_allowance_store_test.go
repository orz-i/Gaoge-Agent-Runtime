package postgres

import (
	"context"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type p2PostgresModel struct{ calls int }

func (client *p2PostgresModel) Generate(context.Context, model.Request) (model.Response, error) {
	client.calls++
	return model.Response{Content: "provisional completion"}, nil
}

type p2PostgresCorrection struct{ checks int }

func (policy *p2PostgresCorrection) ValidateCompletion(context.Context, kernel.Run, model.Response) (agent.CompletionCorrection, error) {
	policy.checks++
	if policy.checks == 1 {
		return agent.CompletionCorrection{Code: "repair", Message: "Correct this provisional result"}, nil
	}
	return agent.CompletionCorrection{}, nil
}

func TestAutoAllowanceRunPersistsAndResumesAcrossSQLStoreReopen(t *testing.T) {
	t.Parallel()
	db := openKernelStoreTestDB(t)
	modelClient := &p2PostgresModel{}
	correction := &p2PostgresCorrection{}
	newWorker := func() (*kernel.Runtime, *agent.Runner) {
		t.Helper()
		runtime, err := kernel.New(kernel.Dependencies{Store: NewKernelStore(db)})
		if err != nil {
			t.Fatal(err)
		}
		runner, err := agent.NewRunner(agent.Dependencies{
			Runtime: runtime, Model: modelClient,
			CompletionPolicies: []agent.CompletionPolicy{correction},
			Limits:             agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 4},
			DeferResumption:    true,
		})
		if err != nil {
			t.Fatal(err)
		}
		return runtime, runner
	}
	_, firstWorker := newWorker()
	waiting, err := firstWorker.StartRun(t.Context(), agent.StartRequest{
		ID: "auto-sql-run", RequestID: "auto-sql-request", Goal: "complete with a correction",
		Actor:         kernel.ActorRef{TenantID: "tenant", ActorID: "operator"},
		Thread:        kernel.ThreadRef{Kind: "conversation", ID: "auto-thread"},
		CallAllowance: &agent.CallAllowance{LLMCalls: 1},
		AutoAllowance: &agent.AutoAllowancePolicy{
			ModelStep: 1, MaxModelRenewals: 1, MaxNoProgressWindow: 1,
		},
	})
	if err != nil || waiting.Run.Status != kernel.RunStatusRunning || modelClient.calls != 1 {
		t.Fatalf("auto grant not persisted at SQL worker yield: %#v calls=%d err=%v", waiting.Run, modelClient.calls, err)
	}
	secondRuntime, secondWorker := newWorker()
	loaded, err := secondRuntime.Load(t.Context(), waiting.Run.ID)
	if err != nil || loaded.Run.Revision != waiting.Run.Revision ||
		string(loaded.State) != string(waiting.State) {
		t.Fatalf("new SQL worker lost allowance snapshot: %#v err=%v", loaded.Run, err)
	}
	originalView, err := agent.ViewState(loaded)
	if err != nil || originalView.AutoAllowance.ModelRenewals != 1 ||
		originalView.CallAllowance.LLMCalls != 2 || originalView.Budget.Usage.LLMCalls != 1 {
		t.Fatalf("persisted auto policy/counters diverged: %#v err=%v", originalView, err)
	}
	done, err := secondWorker.Resume(t.Context(), loaded.Run.ID, loaded.Run.Revision)
	if err != nil || done.Run.Status != kernel.RunStatusCompleted ||
		done.Run.ID != waiting.Run.ID || modelClient.calls != 2 || correction.checks != 2 {
		t.Fatalf("SQL restored worker failed same Run: %#v calls=%d checks=%d err=%v",
			done.Run, modelClient.calls, correction.checks, err)
	}
	view, err := agent.ViewState(done)
	if err != nil || view.AutoAllowance.ModelRenewals != 1 ||
		view.Budget.Usage.LLMCalls != 2 || view.Budget.Limits.MaxLLMCalls != 4 {
		t.Fatalf("SQL execution charges/ceiling lost: %#v err=%v", view, err)
	}
}
