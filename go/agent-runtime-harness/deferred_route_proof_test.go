package harness

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type deferredProofModel struct{}

func (deferredProofModel) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{Content: "done"}, nil
}

func TestDeferredRouteProofUsesOriginalFrozenDelegation(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "remote-twin"}[remote], func(t *testing.T) {
			f := newPR82HandoffFixture(t)
			original, err := agent.NewRunner(agent.Dependencies{Runtime: f.runtime, Model: deferredProofModel{}})
			if err != nil {
				t.Fatal(err)
			}
			d := f.delegation()
			d.Model = "test"
			d.MemberRevision = "v1"
			if remote {
				d.MemberID = "remote"
			}
			itemID, err := f.runner.recordDelegationItem(t.Context(), f.turn, f.parent, d, ItemStarted, "")
			if err != nil {
				t.Fatal(err)
			}
			if err = f.runner.prepareHandoffChild(t.Context(), f.turn, f.parent, d, itemID); err != nil {
				t.Fatal(err)
			}
			c, err := handoff.NewDeferredAgentWithRouting(original, func(v handoff.Delegation) (handoff.DelegationRoute, error) {
				if v.MemberRevision != "v1" {
					return 0, ErrConflict
				}
				if v.MemberID == "remote" {
					return handoff.DelegationRouteRemote, nil
				}
				return handoff.DelegationRouteLocal, nil
			}, handoff.ChildRunnerResolverFunc(func(context.Context, handoff.Delegation) (handoff.ChildRunner, error) {
				t.Fatal("observation resolved remote")
				return nil, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			f.runner.handoffs = c
			f.runner.agent = original
			child, err := original.PrepareRun(t.Context(), agent.StartRequest{ID: d.ChildRunID, Actor: f.parentRun.Run.Actor, Thread: f.parentRun.Run.Thread, RequestID: f.parentRun.Run.ID + ":" + d.ID, Goal: d.Goal, Model: d.Model})
			if err != nil {
				t.Fatal(err)
			}
			observed, frozen, err := f.runner.localDelegationObservation(t.Context(), d.ChildRunID)
			if remote {
				if !errors.Is(err, ErrHandoffCancellationUnverified) {
					t.Fatalf("remote twin observed: %v", err)
				}
			} else {
				if err != nil || !reflect.DeepEqual(observed, child) || !sameHandoffFrozenExecution(frozen, d) {
					t.Fatalf("local original=%+v err=%v", frozen, err)
				}
				if e := f.runner.verifyLocalApprovalRun(t.Context(), child); e != nil {
					t.Fatalf("original continuation authority rejected: %v", e)
				}
				second, e := agent.NewRunner(agent.Dependencies{Runtime: f.runtime, Model: deferredProofModel{}})
				if e != nil {
					t.Fatal(e)
				}
				f.runner.agent = second
				if e = f.runner.verifyLocalApprovalRun(t.Context(), child); !errors.Is(e, ErrRuntimeCancellationUnverified) {
					t.Fatalf("second Agent accepted: %v", e)
				}
				// Corrupt only the already persisted execution tuple; the
				// classifier must not rescue a source-pair mismatch.
				stored, e := f.store.GetItem(t.Context(), itemID)
				if e != nil {
					t.Fatal(e)
				}
				var payload delegationItemPayload
				if e = json.Unmarshal(stored.Payload, &payload); e != nil {
					t.Fatal(e)
				}
				payload.Execution.MemberRevision = "foreign"
				corrupted := stored
				corrupted.Payload, e = json.Marshal(payload)
				if e != nil {
					t.Fatal(e)
				}
				f.store.mu.Lock()
				f.store.itemIDs[itemID] = corrupted
				f.store.mu.Unlock()
				if _, _, e = f.runner.localDelegationObservation(t.Context(), d.ChildRunID); !errors.Is(e, ErrHandoffCancellationUnverified) {
					t.Fatalf("source drift accepted: %v", e)
				}
				f.store.mu.Lock()
				f.store.itemIDs[itemID] = stored
				f.store.mu.Unlock()
			}
			after, e := f.runtime.Load(t.Context(), d.ChildRunID)
			if e != nil || !reflect.DeepEqual(child, after) {
				t.Fatal("proof changed state", e)
			}
		})
	}
}

// Missing legacy execution source must not be filled from caller metadata to
// lend a new routed composition authority.
func TestDeferredRouteProofRejectsLegacyMissingExecution(t *testing.T) {
	f := newPR82HandoffFixture(t)
	d := f.delegation()
	original, err := agent.NewRunner(agent.Dependencies{Runtime: f.runtime, Model: deferredProofModel{}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := handoff.NewDeferredAgentWithRouting(original, func(handoff.Delegation) (handoff.DelegationRoute, error) { return handoff.DelegationRouteLocal, nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.runner.handoffs = c
	if err = f.runner.prepareHandoffChild(t.Context(), f.turn, f.parent, d, f.startedItemID); err != nil {
		t.Fatal(err)
	}
	_, err = original.PrepareRun(t.Context(), agent.StartRequest{ID: d.ChildRunID, Actor: f.parentRun.Run.Actor, Thread: f.parentRun.Run.Thread, RequestID: f.parentRun.Run.ID + ":" + d.ID, Goal: d.Goal, Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.runner.localDelegationObservation(t.Context(), d.ChildRunID); !errors.Is(err, ErrHandoffCancellationUnverified) {
		t.Fatalf("legacy source fabricated: %v", err)
	}
	if _, err = f.runtime.LookupChildExecution(t.Context(), d.ChildRunID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("read minted grant: %v", err)
	}
}

// Legacy routed compositions still return before reading any Kernel/Store:
// a missing runtime would panic if the old early-return boundary were lost.
func TestDeferredRouteLegacyRoutedObservationRemainsUnavailable(t *testing.T) {
	c, err := handoff.NewRouted(handoff.ChildRunnerResolverFunc(func(context.Context, handoff.Delegation) (handoff.ChildRunner, error) {
		t.Fatal("legacy resolver called by observation")
		return nil, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{handoffs: c}
	if _, _, err = r.localDelegationObservation(t.Context(), "child"); !errors.Is(err, ErrHandoffCancellationUnverified) {
		t.Fatalf("legacy observation=%v", err)
	}
	if err = r.reconcileLocalTerminalHandoffEvidence(t.Context(), Turn{ID: "turn"}); err != nil {
		t.Fatalf("legacy no-op=%v", err)
	}
}
