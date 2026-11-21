package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type backdatedSummaryPorts struct{ *summaryCorePorts }

func (ports backdatedSummaryPorts) PlanSummary(ctx context.Context, request model.Request, snapshot kernel.Snapshot) (ContextSummaryPlan, error) {
	plan, err := ports.summaryCorePorts.PlanSummary(ctx, request, snapshot)
	plan.Cursor.UpdatedAt = plan.Cursor.Turn.UpdatedAt.Add(-time.Second)
	return plan, err
}

func TestContextSummaryReconcilesBackdatedCommitTimeAfterACKLoss(t *testing.T) {
	for _, status := range []string{"accepted", "running"} {
		t.Run(status, func(t *testing.T) {
			_, runtime, ports, provider, clock, request := newSummaryCoreFixture(t, nil)
			ports.turn.Status, ports.commitCut = status, "after"
			originalTime := ports.turn.UpdatedAt
			makeRunner := func() *Runner {
				t.Helper()
				wrapped := backdatedSummaryPorts{ports}
				bounded, err := NewBoundedContextAdapter(ports.manager, wrapped)
				if err != nil {
					t.Fatal(err)
				}
				summary, err := NewContextSummaryAdapter(bounded, wrapped)
				if err != nil {
					t.Fatal(err)
				}
				runner, err := NewRunner(Dependencies{Runtime: runtime, Model: provider, Clock: clock,
					RunMiddleware: []plugin.RunMiddleware{bounded}, ContextSummary: summary})
				if err != nil {
					t.Fatal(err)
				}
				return runner
			}
			if _, err := makeRunner().StartRun(t.Context(), request); !errors.Is(err, errSummaryCoreCrash) {
				t.Fatalf("did not lose the Context commit ACK: %v", err)
			}
			pending, err := runtime.Load(t.Context(), request.ID)
			if err != nil {
				t.Fatal(err)
			}
			before, err := ViewState(pending)
			if err != nil || len(before.ModelInvocations) != 1 || ports.current.Window.WorkingState == nil || !ports.turn.UpdatedAt.Equal(originalTime) {
				t.Fatal("the exact Context target or monotonic commit time was not persisted")
			}
			clock.Advance(3 * time.Minute)
			completed, err := makeRunner().Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
			if err != nil {
				t.Fatalf("exact target with an adjusted write time could not recover: %v", err)
			}
			view, err := ViewState(completed)
			if err != nil || completed.Run.Status != kernel.RunStatusCompleted || len(provider.calls()) != 2 ||
				view.Budget.Usage.LLMCalls != 2 || view.Budget.Usage.TotalTokens != 20 || ports.current.Window.WorkingState.Version != 1 ||
				view.ModelInvocations[0].ID != before.ModelInvocations[0].ID || view.ModelInvocations[0].RequestHash != before.ModelInvocations[0].RequestHash {
				t.Fatalf("reconciliation replaced the receipt, state or usage: err=%v view=%+v", err, view)
			}
		})
	}
}
