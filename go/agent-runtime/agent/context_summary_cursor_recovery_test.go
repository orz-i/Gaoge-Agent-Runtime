package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
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

func TestContextSummaryRecoversWithFrozenCheckpointWriteProfile(t *testing.T) {
	for _, transition := range []struct {
		name           string
		writer, reader runtimecontext.CheckpointWriteProfile
		frozenProfile  runtimecontext.CheckpointWriteProfile
		encoding       string
	}{
		{"legacy-to-lossless", runtimecontext.CheckpointWriteLegacy, runtimecontext.CheckpointWriteLosslessJSON, "", ""},
		{"lossless-to-legacy", runtimecontext.CheckpointWriteLosslessJSON, runtimecontext.CheckpointWriteLegacy,
			runtimecontext.CheckpointWriteLosslessJSON, runtimecontext.CheckpointContentHashEncodingLosslessJSON},
	} {
		for _, cut := range []string{"before", "after"} {
			t.Run(transition.name+"/"+cut, func(t *testing.T) {
				_, runtime, ports, provider, clock, request := newSummaryCoreFixture(t, nil)
				// Both profiles can make a new decision from a legacy source.
				if ports.source.ContentHashEncoding != "" {
					t.Fatal("expected a legacy source")
				}
				ports.manager = runtimecontext.NewManager(runtimecontext.Dependencies{CheckpointWriteProfile: transition.writer})
				ports.commitCut = cut
				runner := summaryCoreRunner(t, runtime, ports, provider, clock, nil)
				if _, err := runner.StartRun(t.Context(), request); !errors.Is(err, errSummaryCoreCrash) {
					t.Fatalf("did not stop at the Context commit: %v", err)
				}

				pending, err := runtime.Load(t.Context(), request.ID)
				if err != nil {
					t.Fatal(err)
				}
				view, err := ViewState(pending)
				if err != nil || len(view.ModelInvocations) != 1 || view.ModelInvocations[0].Status != ModelInvocationCompleted {
					t.Fatalf("expected a frozen, unconsumed summary receipt: %v", err)
				}
				frozen := view.ModelInvocations[0]
				binding, err := json.Marshal(frozen.ContextSummary)
				if err != nil || frozen.ContextSummary.TargetCheckpointWriteProfile != transition.frozenProfile ||
					bytes.Contains(binding, []byte("\"targetCheckpointWriteProfile\":")) != (transition.frozenProfile != "") {
					t.Fatalf("unexpected frozen Checkpoint profile: %s, %v", binding, err)
				}
				targetBefore := ports.current.ID
				if (targetBefore != ports.source.ID) != (cut == "after") {
					t.Fatal("fault did not establish the expected Context cursor")
				}

				clock.Advance(3 * time.Minute)
				ports.manager = runtimecontext.NewManager(runtimecontext.Dependencies{CheckpointWriteProfile: transition.reader})
				restarted := summaryCoreRunner(t, runtime, ports, provider, clock, nil)
				completed, err := restarted.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
				if err != nil {
					t.Fatalf("cannot recover frozen summary: %v", err)
				}
				final, err := ViewState(completed)
				if err != nil || completed.Run.Status != kernel.RunStatusCompleted || len(provider.calls()) != 2 ||
					len(final.ModelInvocations) != 2 || final.ModelInvocations[0].ID != frozen.ID ||
					final.ModelInvocations[0].RequestHash != frozen.RequestHash {
					t.Fatalf("recovery changed the summary receipt or replayed provider work: %v", err)
				}
				if !runtimecontext.ValidCheckpoint(ports.current) || ports.current.ContentHashEncoding != transition.encoding ||
					ports.current.ParentCheckpointID != ports.source.ID || (cut == "after" && ports.current.ID != targetBefore) ||
					!VerifyWorkingStateSummaryOrigin(completed, ports.current.Window.WorkingState, ports.source.ScopeID, request.Actor, request.Thread) {
					t.Fatalf("recovery installed an unexpected target: %s encoding=%q", ports.current.ID, ports.current.ContentHashEncoding)
				}
			})
		}
	}
}
