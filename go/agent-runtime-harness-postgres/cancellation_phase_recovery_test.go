package harnesspostgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

var errCancellationPhaseCut = errors.New("original cancellation durable phase interrupted")

type cancellationPhaseCut struct {
	generationStore
	phase, intentID string
	armed, hit      bool
	relations       *runrelation.Registry
	budget          *harness.BudgetAuthority
}

func (s *cancellationPhaseCut) setGenerationRelations(relations *runrelation.Registry) {
	s.relations = relations
}

func (s *cancellationPhaseCut) AppendItem(ctx context.Context, item harness.Item) (harness.Item, bool, error) {
	intent := item.ParentItemID == harness.CancellationFenceParentItemID
	projection := item.Kind == harness.ItemBudget
	completion := strings.HasPrefix(item.ParentItemID, "hcancel_") && item.Status == harness.ItemCompleted
	cut := s.armed && !s.hit && (intent && strings.HasPrefix(s.phase, "intent-") ||
		projection && strings.HasPrefix(s.phase, "projection-") || completion && strings.HasPrefix(s.phase, "completion-"))
	if cut && strings.HasSuffix(s.phase, "before") {
		s.hit = true
		return harness.Item{}, false, errCancellationPhaseCut
	}
	saved, fresh, err := s.generationStore.AppendItem(ctx, item)
	if err == nil && fresh && intent && s.intentID == "" {
		s.intentID = saved.ID
	}
	if err == nil && cut {
		s.hit = true
		return harness.Item{}, false, errCancellationPhaseCut
	}
	return saved, fresh, err
}

// Preserve the real backend's native capabilities while injecting only the
// first terminal control COMMIT. Accounting is part of this transaction.
type generationKernelPorts interface {
	kernel.Store
	kernel.ExecutionStore
	kernel.PhysicalExecutionStore
	kernel.ExecutionCustodyStore
	kernel.ExecutionQuarantineStore
	budget.LedgerStore
}
type generationControlCut struct {
	generationKernelPorts
	phase *cancellationPhaseCut
}

func (s *cancellationPhaseCut) setGenerationBudget(b *harness.BudgetAuthority) { s.budget = b }
func (s *cancellationPhaseCut) wrapGenerationKernel(k kernel.Store) kernel.Store {
	return &generationControlCut{generationKernelPorts: k.(generationKernelPorts), phase: s}
}
func (s *generationControlCut) Commit(ctx context.Context, id string, revision uint64, m kernel.StoreMutation, a kernel.CommitAuthority) (kernel.Snapshot, bool, error) {
	p := s.phase
	cut := p.armed && !p.hit && strings.HasPrefix(p.phase, "control-") && a.Control != nil && a.Control.Kind == kernel.ControlCancel
	if cut && strings.HasSuffix(p.phase, "before") {
		p.hit = true
		return kernel.Snapshot{}, false, errCancellationPhaseCut
	}
	out, applied, err := s.generationKernelPorts.Commit(ctx, id, revision, m, a)
	if err == nil && cut {
		p.hit = true
		return kernel.Snapshot{}, false, errCancellationPhaseCut
	}
	return out, applied, err
}

func TestNativeRootCancellationReplaysEveryDurablePhase(t *testing.T) {
	for _, backend := range generationBackends {
		for _, replacement := range []bool{false, true} {
			for _, phase := range []string{"intent-before", "intent-ack-loss", "control-before", "control-ack-loss", "projection-before", "projection-ack-loss", "completion-before", "completion-ack-loss"} {
				name := backend + "/original/" + phase
				if replacement {
					name = backend + "/replacement/" + phase
				}
				t.Run(name, func(t *testing.T) {
					var cut *cancellationPhaseCut
					m := &generationModel{failFirst: replacement, delegateEveryRoot: true}
					r, store, rt, native, request := newGenerationHarnessBackend(t, backend, m, func(base generationStore) generationStore {
						cut = &cancellationPhaseCut{generationStore: base, phase: phase}
						return cut
					})
					started, err := r.StartAgentTurn(t.Context(), request)
					if replacement {
						if started.Turn.Status != harness.TurnFailed {
							t.Fatalf("known original failure: %+v %v", started.Turn, err)
						}
						root, _ := harness.TopLevelInvocation(started)
						started, err = r.RetryInvocation(t.Context(), root.ID)
					}
					if err != nil || started.Turn.Status != harness.TurnRunning || len(started.Subtasks) != 1 {
						t.Fatalf("native returned-root fixture: %+v %v", started.Turn, err)
					}
					before, err := store.ListItems(t.Context(), started.Turn.ID, 0, 500)
					if err != nil {
						t.Fatal(err)
					}
					calls := m.calls
					cut.armed = true
					if _, err = r.Cancel(t.Context(), started.Turn.ID, "original elected cancellation"); !errors.Is(err, errCancellationPhaseCut) || !cut.hit {
						t.Fatalf("phase not exercised: hit=%t err=%v", cut.hit, err)
					}
					// Reconstruct the Harness/controller over the persisted original
					// route and Store. No remembered phase or fresh intent is used.
					shared := cut.budget
					route, err := handoff.NewDeferredAgent(native)
					if err != nil {
						t.Fatal(err)
					}
					fresh, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: native, Store: store, Budget: shared, Handoffs: route, Relations: cut.relations, Clock: generationClock{}})
					if err != nil {
						t.Fatal(err)
					}
					stopped, err := fresh.Cancel(t.Context(), started.Turn.ID, "resume exact cancellation")
					if err != nil || stopped.Turn.Status != harness.TurnCancelled || m.calls != calls {
						t.Fatalf("original cancellation did not recover without Model effects: status=%s calls=%d->%d err=%v", stopped.Turn.Status, calls, m.calls, err)
					}
					items, err := store.ListItems(t.Context(), started.Turn.ID, 0, 500)
					if err != nil || harness.ActiveCancellationFence(items) {
						t.Fatalf("stranded cancellation fence: %v", err)
					}
					if cut.intentID != "" {
						var intent harness.Item
						var receipts []harness.Item
						for _, item := range items {
							if item.ID == cut.intentID {
								intent = item
							}
							if item.ParentItemID == cut.intentID {
								receipts = append(receipts, item)
							}
						}
						if !harness.VerifiedCancellationFenceReceipt(intent, receipts) {
							t.Fatal("original intent did not retain its unique completion")
						}
					}
					for _, original := range before {
						retained, err := store.GetItem(t.Context(), original.ID)
						if err != nil || retained.Seq != original.Seq || string(retained.Payload) != string(original.Payload) {
							t.Fatalf("original accounting/effect history changed: %s %v", original.ID, err)
						}
					}
				})
			}
		}
	}
}

type uncertainPhaseCanceller struct{ calls int }

func (c *uncertainPhaseCanceller) Cancel(context.Context, string, uint64, string) (kernel.Snapshot, error) {
	c.calls++
	return kernel.Snapshot{}, errors.New("unknown external cleanup")
}

func TestRootPhaseRecoveryDoesNotBorrowCustomCancellerAuthority(t *testing.T) {
	for _, backend := range generationBackends {
		t.Run(backend, func(t *testing.T) {
			var cut *cancellationPhaseCut
			r, store, rt, native, request := newGenerationHarnessBackend(t, backend, &generationModel{}, func(base generationStore) generationStore {
				cut = &cancellationPhaseCut{generationStore: base, phase: "intent-ack-loss"}
				return cut
			})
			started, err := r.StartAgentTurn(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			cut.armed = true
			if _, err = r.Cancel(t.Context(), started.Turn.ID, "original intent"); !errors.Is(err, errCancellationPhaseCut) {
				t.Fatal(err)
			}
			custom := &uncertainPhaseCanceller{}
			route, err := handoff.NewDeferredAgent(native)
			if err != nil {
				t.Fatal(err)
			}
			foreign, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: native, Store: store, Handoffs: route, Relations: cut.relations, Cancellation: custom})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = foreign.Cancel(t.Context(), started.Turn.ID, "borrow original native intent"); err == nil || custom.calls != 0 {
				t.Fatalf("unknown Host cleanup inherited native proof: calls=%d err=%v", custom.calls, err)
			}
			items, err := store.ListItems(t.Context(), started.Turn.ID, 0, 500)
			if err != nil || !harness.ActiveCancellationFence(items) {
				t.Fatalf("uncertain Host route lost its protective fence: %v", err)
			}
		})
	}
}
