package harness

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestPR82DelegationToolReceiptRequiresRouteAndFrozenIdentity(t *testing.T) {
	for _, mode := range []string{"fixed-local", "remote-twin", "dynamic-local", "fixed-local-foreign-task"} {
		t.Run(mode, func(t *testing.T) {
			f := newPR82HandoffFixture(t)
			call := tools.Call{ID: "receipt-call", ToolKey: DelegationToolKey, Arguments: []byte("{}")}
			d := f.delegation()
			d.ID = delegationToolID(f.parent, call.ID)
			d.ChildRunID = stableID("hchild", f.parent.ExecutionRefID, d.ID)
			startedID, err := f.runner.recordDelegationItem(t.Context(), f.turn, f.parent, d, ItemStarted, "")
			if err != nil {
				t.Fatal(err)
			}
			if err = f.runner.prepareHandoffChild(t.Context(), f.turn, f.parent, d, startedID); err != nil {
				t.Fatal(err)
			}
			actual := f.runtime
			if mode == "remote-twin" {
				actual, err = kernel.New(kernel.Dependencies{Store: memory.NewStore()})
				if err != nil {
					t.Fatal(err)
				}
			}
			child := &pr82BoundKernelChild{runtime: actual}
			coordinator, err := handoff.New(child)
			if mode == "dynamic-local" {
				coordinator, err = handoff.NewRouted(handoff.ChildRunnerResolverFunc(func(context.Context, handoff.Delegation) (handoff.ChildRunner, error) { return child, nil }))
			}
			if err != nil {
				t.Fatal(err)
			}
			f.runner.handoffs = coordinator
			goal := d.Goal
			if mode == "fixed-local-foreign-task" {
				goal = "unrelated-task"
			}
			local, err := f.runtime.Create(t.Context(), kernel.CreateRequest{ID: d.ChildRunID, Kind: agent.RunKind, Actor: f.parentRun.Run.Actor, Thread: f.parentRun.Run.Thread, RequestID: f.parentRun.Run.ID + ":" + d.ID, Goal: goal, State: []byte("{}")})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "remote-twin" {
				if _, err = actual.Create(t.Context(), kernel.CreateRequest{ID: d.ChildRunID, Kind: agent.RunKind, Actor: f.parentRun.Run.Actor, Thread: f.parentRun.Run.Thread, RequestID: f.parentRun.Run.ID + ":" + d.ID, Goal: d.Goal, State: []byte("{}")}); err != nil {
					t.Fatal(err)
				}
			}
			pending, waitErr := f.runner.localDelegationWait(t.Context(), d)
			if mode == "fixed-local" {
				if waitErr != nil || pending == nil {
					t.Errorf("verified local wait lost: pending=%v err=%v", pending, waitErr)
				}
			} else if pending != nil {
				t.Errorf("unverified route/task acquired local autonomous wait: %v", pending)
			}
			if _, err = f.runtime.Cancel(t.Context(), local.Run.ID, local.Run.Revision, "local terminal"); err != nil {
				t.Fatal(err)
			}
			handler := NewDelegationToolHandler()
			if err = handler.Bind(f.runner); err != nil {
				t.Fatal(err)
			}
			result, err := handler.VerifyCommittedTool(t.Context(), tools.ExecutionRequest{RunID: f.parent.ExecutionRefID, Call: call})
			if mode == "fixed-local" {
				if err != nil || result.Receipt.Disposition != "committed" {
					t.Fatalf("verified local receipt rejected: %+v err=%v", result, err)
				}
			} else if !errors.Is(err, agent.ErrToolOutcomeUnknown) || result.Receipt.Disposition == "committed" {
				t.Fatalf("unverified route/task produced committed local receipt: %+v err=%v", result, err)
			}
			if mode == "remote-twin" {
				remote, loadErr := actual.Load(t.Context(), d.ChildRunID)
				if loadErr != nil || remote.Run.Status != kernel.RunStatusRunning {
					t.Fatalf("remote changed: %+v %v", remote.Run, loadErr)
				}
			}
		})
	}
}
