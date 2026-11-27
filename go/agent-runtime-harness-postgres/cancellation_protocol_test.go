package harnesspostgres_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

var errProtocolUnknown = errors.New("original Host operation dispatched, receipt unknown")

type protocolFixture struct {
	r           *harness.Runner
	store       generationStore
	runtime     *kernel.Runtime
	agent       *agent.Runner
	relations   *runrelation.Registry
	started     harness.Snapshot
	root, child harness.Invocation
}

func newProtocolFixture(t *testing.T, backend string, cut *protocolCut) protocolFixture {
	t.Helper()
	if cut == nil {
		cut = &protocolCut{}
	}
	r, s, rt, a, req := newGenerationHarnessBackend(t, backend, &generationModel{}, func(base generationStore) generationStore { cut.generationStore = base; return cut })
	started, err := r.StartAgentTurn(t.Context(), req)
	if err != nil || len(started.Subtasks) != 1 {
		t.Fatalf("protocol fixture: %+v %v", started.Turn, err)
	}
	root, _ := harness.TopLevelInvocation(started)
	child, err := s.GetInvocationByExecutionRefID(t.Context(), started.Subtasks[0].RunID)
	if err != nil {
		t.Fatal(err)
	}
	return protocolFixture{r: r, store: s, runtime: rt, agent: a, relations: cut.relations, started: started, root: root, child: child}
}

func (f protocolFixture) controller(t *testing.T, c harness.RuntimeCanceller) *harness.Runner {
	t.Helper()
	route, err := handoff.NewDeferredAgent(f.agent)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := harness.NewBudgetAuthority(harness.BudgetAuthorityDependencies{Store: f.store, Ledgers: f.store, Relations: f.relations, Clock: generationClock{}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := harness.NewRunner(harness.Dependencies{Runtime: f.runtime, Agent: f.agent, Store: f.store, Budget: shared, Handoffs: route, Relations: f.relations, Cancellation: c, Clock: generationClock{}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (f protocolFixture) cancel(t *testing.T, r *harness.Runner, entry string) (harness.Snapshot, error) {
	t.Helper()
	switch entry {
	case "invocation":
		return r.CancelInvocation(t.Context(), f.child.ID, "same original operation")
	case "subtask":
		return r.CancelSubtask(t.Context(), f.started.Turn.ID, f.started.Subtasks[0].ID, "same original operation")
	default:
		return r.Cancel(t.Context(), f.started.Turn.ID, "same original operation")
	}
}

func protocolItems(t *testing.T, s generationStore, turn string) []harness.Item {
	t.Helper()
	items, err := s.ListItems(t.Context(), turn, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

type protocolUnknownCanceller struct {
	runtime          *kernel.Runtime
	rootNative       string
	inside           bool
	calls            atomic.Int64
	entered, release chan struct{}
	pending          sync.WaitGroup
}

func (c *protocolUnknownCanceller) OwnsKernelRuntime(rt *kernel.Runtime) bool { return c.runtime == rt }
func (c *protocolUnknownCanceller) OwnsKernelRunKind(rt *kernel.Runtime, _ kernel.RunKind) bool {
	return c.runtime == rt
}
func (c *protocolUnknownCanceller) Cancel(ctx context.Context, id string, rev uint64, reason string) (kernel.Snapshot, error) {
	if id == c.rootNative {
		return c.runtime.Cancel(ctx, id, rev, reason)
	}
	if _, ok := harness.CancellationEffectFromContext(ctx); !ok {
		return kernel.Snapshot{}, errors.New("missing original cancellation effect identity")
	}
	first := c.calls.Add(1) == 1
	c.pending.Add(1)
	go func() { defer c.pending.Done(); <-c.release }()
	if first {
		close(c.entered)
	}
	if c.inside {
		<-c.release
	}
	return kernel.Snapshot{}, errProtocolUnknown
}

func TestCancellationProtocolUnknownEffectsNeverReenterOrBorrowNativeProof(t *testing.T) {
	for _, backend := range generationBackends {
		for _, entry := range []string{"root", "invocation", "subtask"} {
			for _, inside := range []bool{false, true} {
				for _, recovery := range []string{"native", "original"} {
					name := backend + "/" + entry + "/returned/" + recovery
					if inside {
						name = backend + "/" + entry + "/inside/" + recovery
					}
					t.Run(name, func(t *testing.T) {
						f := newProtocolFixture(t, backend, nil)
						c := &protocolUnknownCanceller{runtime: f.runtime, inside: inside, entered: make(chan struct{}), release: make(chan struct{})}
						original := f.controller(t, c)
						done := make(chan error, 1)
						go func() { _, err := f.cancel(t, original, entry); done <- err }()
						t.Cleanup(func() {
							close(c.release)
							c.pending.Wait()
							if inside {
								<-done
							}
						})
						select {
						case <-c.entered:
						case <-time.After(10 * time.Second):
							t.Fatal("callback not entered")
						}
						if !inside {
							if err := <-done; !errors.Is(err, errProtocolUnknown) {
								t.Fatal(err)
							}
						}
						controller := original
						if recovery == "native" {
							controller = f.r
						}
						if _, err := f.cancel(t, controller, entry); !errors.Is(err, harness.ErrCancellationOutcomeUnknown) {
							t.Fatalf("unknown effect recovery: %v", err)
						}
						if c.calls.Load() != 1 || !harness.ActiveCancellationFence(protocolItems(t, f.store, f.started.Turn.ID)) {
							t.Fatal("unknown operation redispatched or its fence released")
						}
					})
				}
			}
		}
	}
}

func TestCancellationProtocolKernelTerminalDoesNotSignHostReceipt(t *testing.T) {
	for _, backend := range generationBackends {
		t.Run(backend, func(t *testing.T) {
			f := newProtocolFixture(t, backend, nil)
			c := &protocolUnknownCanceller{runtime: f.runtime, entered: make(chan struct{}), release: make(chan struct{})}
			t.Cleanup(func() { close(c.release); c.pending.Wait() })
			r := f.controller(t, c)
			if _, err := f.cancel(t, r, "invocation"); !errors.Is(err, errProtocolUnknown) {
				t.Fatal(err)
			}
			before, err := f.runtime.Load(t.Context(), f.child.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			completed, err := f.agent.Resume(t.Context(), before.Run.ID, before.Run.Revision)
			if err != nil || completed.Run.Status != kernel.RunStatusCompleted {
				t.Fatalf("independent child completion: %+v %v", completed.Run, err)
			}
			if _, err = f.cancel(t, r, "invocation"); !errors.Is(err, harness.ErrCancellationOutcomeUnknown) {
				t.Fatal(err)
			}
			if c.calls.Load() != 1 || !harness.ActiveCancellationFence(protocolItems(t, f.store, f.started.Turn.ID)) {
				t.Fatal("Kernel terminal status replaced Host receipt")
			}
		})
	}
}

func TestCancellationProtocolRootBusyCannotRedispatchUnknownDescendant(t *testing.T) {
	for _, backend := range generationBackends {
		t.Run(backend, func(t *testing.T) {
			f := newProtocolFixture(t, backend, nil)
			c := &protocolUnknownCanceller{runtime: f.runtime, rootNative: f.root.ExecutionRefID, entered: make(chan struct{}), release: make(chan struct{})}
			t.Cleanup(func() { close(c.release); c.pending.Wait() })
			r := f.controller(t, c)
			if _, err := f.cancel(t, r, "root"); !errors.Is(err, errProtocolUnknown) || c.calls.Load() != 1 {
				t.Fatalf("root busy -> child fixture: %v", err)
			}
			if _, err := f.cancel(t, r, "root"); !errors.Is(err, harness.ErrCancellationOutcomeUnknown) {
				t.Fatal(err)
			}
			if c.calls.Load() != 1 || !harness.ActiveCancellationFence(protocolItems(t, f.store, f.started.Turn.ID)) {
				t.Fatal("root-only no-effect proof covered unknown child cleanup")
			}
		})
	}
}

// A stable Host-owned operation ledger is modeled by originalID + known.
// ReconcileCancellation is read-only: it never calls Cancel or mutates Kernel.
type protocolReceiptCanceller struct {
	runtime           *kernel.Runtime
	owner, originalID string
	known             harness.CancellationEffectOutcome
	physicalCancel    bool
	foreignSnapshot   bool
	calls, reads      int
}

func (c *protocolReceiptCanceller) OwnsKernelRuntime(rt *kernel.Runtime) bool { return c.runtime == rt }
func (c *protocolReceiptCanceller) CancellationOwnerID() string               { return c.owner }
func (c *protocolReceiptCanceller) Cancel(ctx context.Context, id string, rev uint64, reason string) (kernel.Snapshot, error) {
	e, ok := harness.CancellationEffectFromContext(ctx)
	if !ok {
		return kernel.Snapshot{}, errors.New("missing claim")
	}
	c.originalID = e.ID
	c.calls++
	if c.physicalCancel {
		return c.runtime.Cancel(ctx, id, rev, reason)
	}
	return kernel.Snapshot{}, errProtocolUnknown
}
func (c *protocolReceiptCanceller) ReconcileCancellation(ctx context.Context, e harness.CancellationEffect) (kernel.Snapshot, harness.CancellationEffectOutcome, error) {
	c.reads++
	if e.ID != c.originalID || c.known == "" {
		return kernel.Snapshot{}, harness.CancellationEffectUnknown, nil
	}
	if c.known == harness.CancellationEffectNoEffect {
		return kernel.Snapshot{}, c.known, nil
	}
	snapshot, err := c.runtime.Load(ctx, e.RunID)
	if c.foreignSnapshot {
		snapshot.Run.Goal = "foreign task"
	}
	return snapshot, c.known, err
}

func TestCancellationProtocolOriginalOwnerReconcilesWithoutRedispatch(t *testing.T) {
	for _, backend := range generationBackends {
		t.Run(backend, func(t *testing.T) {
			f := newProtocolFixture(t, backend, nil)
			c := &protocolReceiptCanceller{runtime: f.runtime, owner: "original-host"}
			r := f.controller(t, c)
			if _, err := f.cancel(t, r, "invocation"); !errors.Is(err, errProtocolUnknown) {
				t.Fatal(err)
			}
			foreign := &protocolReceiptCanceller{runtime: f.runtime, owner: "foreign-host", originalID: c.originalID, known: harness.CancellationEffectSettled}
			if _, err := f.cancel(t, f.controller(t, foreign), "invocation"); !errors.Is(err, harness.ErrCancellationOutcomeUnknown) || foreign.reads != 0 {
				t.Fatalf("foreign owner inherited claim: %v", err)
			}
			if _, err := f.cancel(t, r, "invocation"); !errors.Is(err, harness.ErrCancellationOutcomeUnknown) || c.calls != 1 {
				t.Fatalf("unknown callback replay: %v", err)
			}
			child, err := f.runtime.Load(t.Context(), f.child.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.runtime.Cancel(t.Context(), child.Run.ID, child.Run.Revision, "original owner received remote ACK"); err != nil {
				t.Fatal(err)
			}
			c.known = harness.CancellationEffectSettled
			c.foreignSnapshot = true
			if _, err = f.cancel(t, r, "invocation"); !errors.Is(err, harness.ErrCancellationOutcomeUnknown) {
				t.Fatalf("foreign same-ID receipt accepted: %v", err)
			}
			c.foreignSnapshot = false
			if _, err = f.cancel(t, f.controller(t, c), "invocation"); err != nil {
				t.Fatal(err)
			}
			if c.calls != 1 || harness.ActiveCancellationFence(protocolItems(t, f.store, f.started.Turn.ID)) {
				t.Fatal("owner recovery dispatched again or stranded fence")
			}
		})
	}
}

// One fault injector covers the shared protocol boundaries rather than one
// bespoke test wrapper per reported symptom. Every cut is one-shot.
type protocolCut struct {
	generationStore
	relations                       *runrelation.Registry
	phase, runID, intentID          string
	armed, hit, stopStart, startHit bool
	stopRoot                        bool
	stopInvocation, invocationHit   bool
}

func (s *protocolCut) CreateInvocation(ctx context.Context, inv harness.Invocation) (harness.Invocation, bool, error) {
	if s.stopInvocation && !s.invocationHit && inv.ParentItemID != "" {
		s.invocationHit, s.runID = true, inv.ExecutionRefID
		return harness.Invocation{}, false, errCancellationPhaseCut
	}
	return s.generationStore.CreateInvocation(ctx, inv)
}

func (s *protocolCut) setGenerationRelations(r *runrelation.Registry) { s.relations = r }
func (s *protocolCut) AppendItem(ctx context.Context, item harness.Item) (harness.Item, bool, error) {
	if s.stopStart && !s.startHit && item.ParentItemID == harness.ExecutionDispatchParentItemID {
		s.startHit = true
		s.runID = item.RunID
		return harness.Item{}, false, errCancellationPhaseCut
	}
	var task harness.Subtask
	if item.Kind == harness.ItemSubtask {
		_ = json.Unmarshal(item.Payload, &task)
	}
	intent := item.ParentItemID == harness.CancellationFenceParentItemID
	cut := s.armed && !s.hit && (intent && strings.HasPrefix(s.phase, "intent-") ||
		item.ParentItemID == harness.CancellationEffectParentItemID && strings.HasPrefix(s.phase, "effect-") ||
		strings.HasPrefix(item.ID, "hcanceleffectdone_") && strings.HasPrefix(s.phase, "receipt-") ||
		item.Kind == harness.ItemBudget && strings.HasPrefix(s.phase, "projection-") ||
		task.CancelStatus == "requested" && strings.HasPrefix(s.phase, "requested-") ||
		task.CancelStatus == "confirmed" && strings.HasPrefix(s.phase, "confirmed-") ||
		strings.HasPrefix(item.ParentItemID, "hcancel_") && item.Status == harness.ItemCompleted && strings.HasPrefix(s.phase, "completion-"))
	if cut && strings.HasSuffix(s.phase, "before") {
		s.hit = true
		return harness.Item{}, false, errCancellationPhaseCut
	}
	saved, fresh, err := s.generationStore.AppendItem(ctx, item)
	if err == nil && s.stopRoot && !s.startHit && item.Kind == harness.ItemAgentInvocation &&
		item.Status == harness.ItemStarted && item.ParentItemID == "" {
		s.startHit, s.runID = true, item.RunID
		return harness.Item{}, false, errCancellationPhaseCut
	}
	if err == nil && fresh && intent && s.intentID == "" {
		s.intentID = saved.ID
	}
	if cut && err == nil {
		s.hit = true
		return harness.Item{}, false, errCancellationPhaseCut
	}
	return saved, fresh, err
}
func (s *protocolCut) SaveBudget(ctx context.Context, l budget.Ledger, rev uint64) error {
	cut := s.armed && !s.hit && strings.HasPrefix(s.phase, "budget-") && l.RunCancelled(s.runID)
	if cut && strings.HasSuffix(s.phase, "before") {
		s.hit = true
		return errCancellationPhaseCut
	}
	err := s.generationStore.SaveBudget(ctx, l, rev)
	if cut && err == nil {
		s.hit = true
		return errCancellationPhaseCut
	}
	return err
}
func (s *protocolCut) SettleUncreatedRoot(ctx context.Context, inv harness.Invocation, turn harness.Turn) (harness.Invocation, harness.Turn, error) {
	cut := s.armed && !s.hit && strings.HasPrefix(s.phase, "atomic-")
	if cut && strings.HasSuffix(s.phase, "before") {
		s.hit = true
		return harness.Invocation{}, harness.Turn{}, errCancellationPhaseCut
	}
	a, b, err := s.generationStore.SettleUncreatedRoot(ctx, inv, turn)
	if cut && err == nil {
		s.hit = true
		return harness.Invocation{}, harness.Turn{}, errCancellationPhaseCut
	}
	return a, b, err
}

func TestCancellationProtocolHostDurableCutRecovery(t *testing.T) {
	for _, backend := range generationBackends {
		for _, phase := range []string{"intent-before", "intent-ack-loss", "effect-before", "effect-ack-loss", "receipt-before", "receipt-ack-loss", "completion-before", "completion-ack-loss"} {
			t.Run(backend+"/"+phase, func(t *testing.T) {
				cut := &protocolCut{phase: phase}
				f := newProtocolFixture(t, backend, cut)
				c := &protocolReceiptCanceller{runtime: f.runtime, owner: "stable-host", physicalCancel: true}
				r := f.controller(t, c)
				cut.armed = true
				if _, err := f.cancel(t, r, "invocation"); !errors.Is(err, errCancellationPhaseCut) || !cut.hit {
					t.Fatalf("cut not reached: %t %v", cut.hit, err)
				}
				if phase == "effect-ack-loss" {
					// The first call returned before callback entry. Its original owner
					// irreversibly certifies no dispatch under this exact operation ID.
					for _, item := range protocolItems(t, f.store, f.started.Turn.ID) {
						if item.ParentItemID == harness.CancellationEffectParentItemID {
							c.originalID = item.ID
						}
					}
					c.known = harness.CancellationEffectNoEffect
				}
				if phase == "receipt-before" {
					if _, err := f.cancel(t, r, "invocation"); !errors.Is(err, harness.ErrCancellationOutcomeUnknown) || c.calls != 1 {
						t.Fatalf("terminal Kernel laundered missing Host receipt: %v", err)
					}
					c.known = harness.CancellationEffectSettled
				}
				if _, err := f.cancel(t, f.controller(t, c), "invocation"); err != nil {
					t.Fatal(err)
				}
				if c.calls != 1 || harness.ActiveCancellationFence(protocolItems(t, f.store, f.started.Turn.ID)) {
					t.Fatalf("duplicate effect or stranded receipt: calls=%d", c.calls)
				}
			})
		}
	}
}

func TestCancellationProtocolWaitingNoDispatchAllDurableCuts(t *testing.T) {
	for _, backend := range generationBackends {
		for _, phase := range []string{"none", "intent-before", "intent-ack-loss", "requested-before", "requested-ack-loss", "budget-before", "budget-ack-loss", "confirmed-before", "confirmed-ack-loss", "projection-before", "projection-ack-loss", "completion-before", "completion-ack-loss"} {
			t.Run(backend+"/"+phase, func(t *testing.T) {
				cut := &protocolCut{phase: phase}
				m := &generationModel{}
				r, s, rt, _, req := newGenerationHarnessBackend(t, backend, m, func(base generationStore) generationStore { cut.generationStore = base; return cut })
				req.Config.SharedBudget.MaxConcurrentDescendants = 1
				first, err := r.StartAgentTurn(t.Context(), req)
				if err != nil {
					t.Fatal(err)
				}
				wait, err := r.Delegate(t.Context(), first.Turn.ID, harness.DelegateRequest{MemberID: "researcher", Goal: "capacity waiting target"})
				if err != nil {
					t.Fatal(err)
				}
				cut.runID = wait.Delegation.ChildRunID
				if _, err = rt.Load(t.Context(), cut.runID); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatalf("waiting dispatched: %v", err)
				}
				if _, err = s.GetInvocationByExecutionRefID(t.Context(), cut.runID); !errors.Is(err, harness.ErrNotFound) {
					t.Fatalf("waiting admitted Invocation: %v", err)
				}
				calls := m.calls
				cut.armed = true
				_, firstErr := r.CancelSubtask(t.Context(), first.Turn.ID, wait.Delegation.ID, "cancel no-dispatch child")
				if phase == "none" && firstErr != nil || phase != "none" && (!errors.Is(firstErr, errCancellationPhaseCut) || !cut.hit) {
					t.Fatalf("cut not exercised: %v", firstErr)
				}
				if _, err = r.CancelSubtask(t.Context(), first.Turn.ID, wait.Delegation.ID, "resume original intent"); err != nil {
					t.Fatal(err)
				}
				if _, err = r.Cancel(t.Context(), first.Turn.ID, "stop owning Turn"); err != nil {
					t.Fatal(err)
				}
				if m.calls != calls || harness.ActiveCancellationFence(protocolItems(t, s, first.Turn.ID)) {
					t.Fatal("waiting recovery changed physical effects or stranded fence")
				}
			})
		}
	}
}

func TestCancellationProtocolInitialRootNoDispatchAllDurableCuts(t *testing.T) {
	for _, backend := range generationBackends {
		for _, withBudget := range []bool{false, true} {
			phases := []string{"none", "intent-before", "intent-ack-loss", "atomic-before", "atomic-ack-loss", "completion-before", "completion-ack-loss"}
			if withBudget {
				phases = append(phases, "budget-before", "budget-ack-loss", "projection-before", "projection-ack-loss")
			}
			for _, phase := range phases {
				name := backend + "/plain/" + phase
				if withBudget {
					name = backend + "/budget/" + phase
				}
				t.Run(name, func(t *testing.T) {
					cut := &protocolCut{phase: phase, stopStart: withBudget, stopRoot: !withBudget}
					m := &generationModel{calls: 10}
					r, s, rt, native, req := newGenerationHarnessBackend(t, backend, m, func(base generationStore) generationStore { cut.generationStore = base; return cut })
					req.Config.ToolKeys, req.Config.ToolPolicies = nil, nil
					if !withBudget {
						var err error
						r, err = harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: native, Store: s, Clock: generationClock{}})
						if err != nil {
							t.Fatal(err)
						}
						req.Config.SharedBudget = nil
					}
					if _, err := r.StartAgentTurn(t.Context(), req); !errors.Is(err, errCancellationPhaseCut) || !cut.startHit {
						t.Fatalf("initial admission cut: %v", err)
					}
					inv, err := s.GetInvocationByExecutionRefID(t.Context(), cut.runID)
					if err != nil {
						t.Fatal(err)
					}
					before, err := r.Load(t.Context(), inv.TurnID)
					if err != nil || before.Turn.Status != harness.TurnAccepted {
						t.Fatalf("initial Root fixture: %v", err)
					}
					cut.armed = true
					_, firstErr := r.Cancel(t.Context(), inv.TurnID, "cancel before first physical Start")
					if phase == "none" && firstErr != nil || phase != "none" && (!errors.Is(firstErr, errCancellationPhaseCut) || !cut.hit) {
						t.Fatalf("Root cut not exercised: %v", firstErr)
					}
					stopped, err := r.Cancel(t.Context(), inv.TurnID, "resume original no-dispatch intent")
					if err != nil || stopped.Turn.Status != harness.TurnCancelled {
						t.Fatalf("initial Root recovery: %+v %v", stopped.Turn, err)
					}
					if _, err = rt.Load(t.Context(), cut.runID); !errors.Is(err, kernel.ErrNotFound) {
						t.Fatalf("invented a Kernel Run: %v", err)
					}
					if _, err = r.StartAgentTurn(t.Context(), req); err != nil {
						t.Fatal(err)
					}
					if m.calls != 10 || harness.ActiveCancellationFence(protocolItems(t, s, inv.TurnID)) {
						t.Fatal("initial cancellation dispatched Model or retained fence")
					}
				})
			}
		}
	}
}

func TestCancellationProtocolPreparedHandoffNoDispatchRecoversBothControls(t *testing.T) {
	for _, backend := range generationBackends {
		for _, entry := range []string{"invocation", "subtask"} {
			t.Run(backend+"/"+entry, func(t *testing.T) {
				cut := &protocolCut{phase: "intent-ack-loss"}
				f := newProtocolFixture(t, backend, cut)
				cut.stopStart = true
				if _, err := f.r.Delegate(t.Context(), f.started.Turn.ID, harness.DelegateRequest{MemberID: "researcher", Goal: "admitted but not dispatched"}); !errors.Is(err, errCancellationPhaseCut) || !cut.startHit {
					t.Fatalf("handoff pre-dispatch cut: %v", err)
				}
				child, err := f.store.GetInvocationByExecutionRefID(t.Context(), cut.runID)
				if err != nil {
					t.Fatal(err)
				}
				started, err := f.store.GetItem(t.Context(), child.ParentItemID)
				if err != nil {
					t.Fatal(err)
				}
				var payload struct {
					DelegationID string `json:"delegationID"`
				}
				if err = json.Unmarshal(started.Payload, &payload); err != nil {
					t.Fatal(err)
				}
				operate := func() error {
					if entry == "invocation" {
						_, e := f.r.CancelInvocation(t.Context(), child.ID, "stop no-dispatch child")
						return e
					}
					_, e := f.r.CancelSubtask(t.Context(), f.started.Turn.ID, payload.DelegationID, "stop no-dispatch child")
					return e
				}
				cut.armed = true
				if err = operate(); !errors.Is(err, errCancellationPhaseCut) {
					t.Fatal(err)
				}
				if err = operate(); err != nil {
					t.Fatal(err)
				}
				if _, err = f.r.Cancel(t.Context(), f.started.Turn.ID, "stop Turn"); err != nil {
					t.Fatal(err)
				}
				if harness.ActiveCancellationFence(protocolItems(t, f.store, f.started.Turn.ID)) {
					t.Fatal("prepared child intent stranded")
				}
			})
		}
	}
}

func TestCancellationProtocolFrozenStartedBeforeInvocationIsNoDispatch(t *testing.T) {
	for _, backend := range generationBackends {
		for _, entry := range []string{"subtask", "root"} {
			t.Run(backend+"/"+entry, func(t *testing.T) {
				cut := &protocolCut{phase: "intent-ack-loss"}
				f := newProtocolFixture(t, backend, cut)
				cut.stopInvocation = true
				if _, err := f.r.Delegate(t.Context(), f.started.Turn.ID, harness.DelegateRequest{MemberID: "researcher", Goal: "frozen before Invocation"}); !errors.Is(err, errCancellationPhaseCut) || !cut.invocationHit {
					t.Fatalf("pre-Invocation cut: %v", err)
				}
				var taskID string
				for _, item := range protocolItems(t, f.store, f.started.Turn.ID) {
					if item.Kind == harness.ItemDelegation && item.RunID == cut.runID {
						var p struct {
							DelegationID string `json:"delegationID"`
						}
						if err := json.Unmarshal(item.Payload, &p); err != nil {
							t.Fatal(err)
						}
						taskID = p.DelegationID
					}
				}
				if taskID == "" {
					t.Fatal("missing frozen Started source")
				}
				operate := func() error {
					if entry == "root" {
						_, err := f.r.Cancel(t.Context(), f.started.Turn.ID, "stop frozen subtree")
						return err
					}
					_, err := f.r.CancelSubtask(t.Context(), f.started.Turn.ID, taskID, "stop pre-Invocation target")
					return err
				}
				cut.armed = true
				if err := operate(); !errors.Is(err, errCancellationPhaseCut) {
					t.Fatal(err)
				}
				if err := operate(); err != nil {
					t.Fatal(err)
				}
				if _, err := f.r.Cancel(t.Context(), f.started.Turn.ID, "stop owning Turn"); err != nil {
					t.Fatal(err)
				}
				if harness.ActiveCancellationFence(protocolItems(t, f.store, f.started.Turn.ID)) {
					t.Fatal("pre-Invocation Started source stranded fence")
				}
			})
		}
	}
}

func TestCancellationProtocolWaitingToStartedNoInvocationRetainsHistory(t *testing.T) {
	for _, backend := range generationBackends {
		for _, replacement := range []bool{false, true} {
			name := backend + "/original"
			if replacement {
				name = backend + "/replacement"
			}
			t.Run(name, func(t *testing.T) {
				cut := &protocolCut{phase: "intent-ack-loss"}
				m := &generationModel{}
				r, store, rt, native, req := newGenerationHarnessBackend(t, backend, m, func(base generationStore) generationStore { cut.generationStore = base; return cut })
				req.Config.SharedBudget.MaxConcurrentDescendants = 1
				first, err := r.StartAgentTurn(t.Context(), req)
				if err != nil {
					t.Fatal(err)
				}
				request := harness.DelegateRequest{MemberID: "researcher", Goal: "waiting then admitted before Invocation"}
				wait, err := r.Delegate(t.Context(), first.Turn.ID, request)
				if err != nil {
					t.Fatal(err)
				}
				child, err := rt.Load(t.Context(), first.Subtasks[0].RunID)
				if err != nil {
					t.Fatal(err)
				}
				if ended, finishErr := native.Resume(t.Context(), child.Run.ID, child.Run.Revision); finishErr != nil || ended.Run.Status != kernel.RunStatusCompleted {
					t.Fatalf("release original capacity: %v", finishErr)
				}
				cut.stopInvocation = true
				if _, err = r.Delegate(t.Context(), first.Turn.ID, request); !errors.Is(err, errCancellationPhaseCut) || !cut.invocationHit {
					t.Fatalf("Waiting -> Started cut: %v", err)
				}
				before := protocolItems(t, store, first.Turn.ID)
				if cut.runID != wait.Delegation.ChildRunID {
					t.Fatal("Waiting changed physical identity")
				}
				if !replacement {
					cut.armed = true
					if _, err = r.CancelSubtask(t.Context(), first.Turn.ID, wait.Delegation.ID, "stop pre-Invocation Started"); !errors.Is(err, errCancellationPhaseCut) {
						t.Fatal(err)
					}
					if _, err = r.CancelSubtask(t.Context(), first.Turn.ID, wait.Delegation.ID, "resume original intent"); err != nil {
						t.Fatal(err)
					}
				}
				stopped, err := r.Cancel(t.Context(), first.Turn.ID, "stop original generation")
				if err != nil {
					t.Fatal(err)
				}
				if replacement {
					root, _ := harness.TopLevelInvocation(stopped)
					if _, err = r.RetryInvocation(t.Context(), root.ID); err != nil {
						t.Fatal(err)
					}
					calls := m.calls
					if _, err = r.Cancel(t.Context(), first.Turn.ID, "old Waiting -> Started is history, not current execution"); err != nil || m.calls != calls {
						t.Fatalf("replacement history: calls=%d->%d err=%v", calls, m.calls, err)
					}
				}
				for _, original := range before {
					retained, readErr := store.GetItem(t.Context(), original.ID)
					if readErr != nil || retained.Seq != original.Seq || string(retained.Payload) != string(original.Payload) {
						t.Fatalf("rewrote admission/accounting history: %s %v", original.ID, readErr)
					}
				}
				if harness.ActiveCancellationFence(protocolItems(t, store, first.Turn.ID)) {
					t.Fatal("Waiting -> Started fence stranded")
				}
			})
		}
	}
}

func TestCancellationProtocolHistoricalNestedWaitingRejectsBeforeElection(t *testing.T) {
	for _, backend := range generationBackends {
		t.Run(backend, func(t *testing.T) {
			r, store, _, _, req := newGenerationHarnessBackend(t, backend, &generationModel{})
			req.Config.SharedBudget.MaxConcurrentDescendants = 1
			req.Config.DelegationPolicy.MaxDepth = 2
			first, err := r.StartAgentTurn(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			wait, err := r.DelegateByExecutionRefID(t.Context(), first.Subtasks[0].RunID, harness.DelegateRequest{MemberID: "researcher", Goal: "nested capacity wait"})
			if err != nil {
				t.Fatal(err)
			}
			stopped, err := r.Cancel(t.Context(), first.Turn.ID, "stop first generation")
			if err != nil {
				t.Fatal(err)
			}
			root, _ := harness.TopLevelInvocation(stopped)
			if _, err = r.RetryInvocation(t.Context(), root.ID); err != nil {
				t.Fatal(err)
			}
			before := protocolItems(t, store, first.Turn.ID)
			ledger, err := store.LoadBudget(t.Context(), first.Turn.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.CancelSubtask(t.Context(), first.Turn.ID, wait.Delegation.ID, "obsolete nested parent Run did not rotate"); !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("obsolete waiting target admitted: %v", err)
			}
			after := protocolItems(t, store, first.Turn.ID)
			retained, err := store.LoadBudget(t.Context(), first.Turn.ID)
			if err != nil || len(after) != len(before) || retained.Revision != ledger.Revision || harness.ActiveCancellationFence(after) {
				t.Fatalf("historical nested control mutated successor admission/budget: %v", err)
			}
		})
	}
}

func protocolID(prefix, value string) string {
	sum := sha256.Sum256(append([]byte(strings.TrimSpace(value)), 0))
	return prefix + "_" + hex.EncodeToString(sum[:])[:24]
}

func TestCancellationProtocolPublicAppendCannotMintClaimsOrReceipts(t *testing.T) {
	for _, backend := range generationBackends {
		t.Run(backend, func(t *testing.T) {
			f := newProtocolFixture(t, backend, nil)
			c := &protocolReceiptCanceller{runtime: f.runtime, owner: "original-owner"}
			if _, err := f.cancel(t, f.controller(t, c), "invocation"); !errors.Is(err, errProtocolUnknown) {
				t.Fatal(err)
			}
			var claim, intent harness.Item
			for _, item := range protocolItems(t, f.store, f.started.Turn.ID) {
				if item.ParentItemID == harness.CancellationEffectParentItemID {
					claim = item
				}
				if item.ParentItemID == harness.CancellationFenceParentItemID {
					intent = item
				}
			}
			if claim.ID == "" || intent.ID == "" {
				t.Fatal("missing original authority")
			}
			facts := protocolItems(t, f.store, f.started.Turn.ID)
			if harness.CancellationEffectProjectionReady(facts, "") || harness.CancellationEffectProjectionReady(facts, claim.RunID) || !harness.CancellationEffectProjectionReady(facts, "unrelated-child") {
				t.Fatal("terminal projection crossed effect scope or blocked an unrelated owner")
			}
			var e harness.CancellationEffect
			if err := json.Unmarshal(claim.Payload, &e); err != nil {
				t.Fatal(err)
			}
			e.ID = claim.ID + "-forged"
			raw, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			forged := claim
			forged.ID, forged.Payload, forged.Seq = e.ID, raw, 0
			if _, _, err = f.store.AppendItem(t.Context(), forged); !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("public claim minted: %v", err)
			}
			now := generationClock{}.Now()
			receipt := harness.Item{ID: protocolID("hcanceleffectdone", claim.ID), TurnID: claim.TurnID, RunID: claim.RunID, Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted, ParentItemID: claim.ID, Payload: json.RawMessage(`{"outcome":"settled"}`), CreatedAt: now, UpdatedAt: now}
			if _, _, err = f.store.AppendItem(t.Context(), receipt); !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("public Host ACK minted: %v", err)
			}
			attachment := receipt
			attachment.ID, attachment.Kind, attachment.Payload = "ordinary-attachment", harness.ItemArtifact, json.RawMessage(`{}`)
			if _, _, err = f.store.AppendItem(t.Context(), attachment); !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("ordinary Artifact poisoned an exclusive effect receipt: %v", err)
			}
			completion := receipt
			completion.ID = protocolID("hcancelclosed", intent.ID)
			completion.ParentItemID = intent.ID
			completion.Payload = json.RawMessage(`{"boundary":"cancel_scope_settled"}`)
			if _, _, err = f.store.AppendItem(t.Context(), completion); !errors.Is(err, harness.ErrCancellationOutcomeUnknown) {
				t.Fatalf("global closure skipped unknown effect: %v", err)
			}
			if !harness.ActiveCancellationFence(protocolItems(t, f.store, f.started.Turn.ID)) {
				t.Fatal("raw authority append released fence")
			}
		})
	}
}

// Pause before actual Store boundaries, not by corrupting returned records.
// Another caller observes Kernel terminal while the original Host claim is
// still awaiting admission, then that claim commits before projection CAS.
type protocolProjectionRaceStore struct {
	*protocolCut
	armed                                                        bool
	target                                                       string
	claimOnce, projectionOnce                                    sync.Once
	postOnce                                                     sync.Once
	projectionWins                                               bool
	claimReady, claimProceed, projectionReady, projectionProceed chan struct{}
	projectionApplied, afterProjectionProceed                    chan struct{}
}

func (s *protocolProjectionRaceStore) AppendItem(ctx context.Context, item harness.Item) (harness.Item, bool, error) {
	if s.armed && item.ParentItemID == harness.CancellationEffectParentItemID {
		s.claimOnce.Do(func() { close(s.claimReady); <-s.claimProceed })
	}
	return s.protocolCut.AppendItem(ctx, item)
}
func (s *protocolProjectionRaceStore) UpdateInvocation(ctx context.Context, inv harness.Invocation, rev uint64) (harness.Invocation, error) {
	if s.armed && inv.ID == s.target && inv.Status == harness.InvocationCompleted {
		s.projectionOnce.Do(func() { close(s.projectionReady); <-s.projectionProceed })
	}
	updated, err := s.generationStore.UpdateInvocation(ctx, inv, rev)
	if s.armed && s.projectionWins && inv.ID == s.target && inv.Status == harness.InvocationCompleted && err == nil {
		s.postOnce.Do(func() { close(s.projectionApplied); <-s.afterProjectionProceed })
	}
	return updated, err
}

func TestCancellationProtocolTerminalProjectionCompetesWithLateEffectClaim(t *testing.T) {
	for _, backend := range generationBackends {
		for _, projectionWins := range []bool{false, true} {
			name := backend + "/claim-wins"
			if projectionWins {
				name = backend + "/projection-wins"
			}
			t.Run(name, func(t *testing.T) {
				s := &protocolProjectionRaceStore{protocolCut: &protocolCut{}, projectionWins: projectionWins, claimReady: make(chan struct{}), claimProceed: make(chan struct{}), projectionReady: make(chan struct{}), projectionProceed: make(chan struct{}), projectionApplied: make(chan struct{}), afterProjectionProceed: make(chan struct{})}
				r, store, rt, native, req := newGenerationHarnessBackend(t, backend, &generationModel{}, func(base generationStore) generationStore { s.generationStore = base; return s })
				started, err := r.StartAgentTurn(t.Context(), req)
				if err != nil {
					t.Fatal(err)
				}
				root, _ := harness.TopLevelInvocation(started)
				child, err := store.GetInvocationByExecutionRefID(t.Context(), started.Subtasks[0].RunID)
				if err != nil {
					t.Fatal(err)
				}
				f := protocolFixture{r: r, store: store, runtime: rt, agent: native, relations: s.relations, started: started, root: root, child: child}
				c := &protocolUnknownCanceller{runtime: rt, entered: make(chan struct{}), release: make(chan struct{})}
				controller := f.controller(t, c)
				s.target, s.armed = child.ID, true
				first, second := make(chan error, 1), make(chan error, 1)
				firstFinished, secondFinished := make(chan struct{}), make(chan struct{})
				var releaseClaim, releaseProjection, releasePost sync.Once
				secondStarted := false
				t.Cleanup(func() {
					releaseClaim.Do(func() { close(s.claimProceed) })
					releaseProjection.Do(func() { close(s.projectionProceed) })
					releasePost.Do(func() { close(s.afterProjectionProceed) })
					close(c.release)
					<-firstFinished
					if secondStarted {
						<-secondFinished
					}
					c.pending.Wait()
				})
				wait := func(ch <-chan struct{}, name string) {
					select {
					case <-ch:
					case <-time.After(10 * time.Second):
						t.Fatalf("barrier not reached: %s", name)
					}
				}
				go func() { defer close(firstFinished); _, e := f.cancel(t, controller, "invocation"); first <- e }()
				wait(s.claimReady, "original claim admission")
				before, err := rt.Load(t.Context(), child.ExecutionRefID)
				if err != nil {
					t.Fatal(err)
				}
				completed, err := native.Resume(t.Context(), before.Run.ID, before.Run.Revision)
				if err != nil || completed.Run.Status != kernel.RunStatusCompleted {
					t.Fatalf("natural completion: %v", err)
				}
				secondStarted = true
				go func() { defer close(secondFinished); _, e := f.cancel(t, controller, "invocation"); second <- e }()
				wait(s.projectionReady, "competing terminal CAS")
				if projectionWins {
					releaseProjection.Do(func() { close(s.projectionProceed) })
					wait(s.projectionApplied, "terminal CAS committed before claim")
					releaseClaim.Do(func() { close(s.claimProceed) })
					if err = <-first; !errors.Is(err, harness.ErrConflict) || c.calls.Load() != 0 {
						t.Fatalf("late callback entered terminal owner: calls=%d err=%v", c.calls.Load(), err)
					}
					releasePost.Do(func() { close(s.afterProjectionProceed) })
					if err = <-second; err != nil {
						t.Fatal(err)
					}
					if harness.ActiveCancellationFence(protocolItems(t, store, started.Turn.ID)) {
						t.Fatal("settled projection stranded fence")
					}
					return
				}
				releaseClaim.Do(func() { close(s.claimProceed) })
				wait(c.entered, "original physical callback")
				if err = <-first; !errors.Is(err, errProtocolUnknown) {
					t.Fatal(err)
				}
				releaseProjection.Do(func() { close(s.projectionProceed) })
				if err = <-second; !errors.Is(err, harness.ErrCancellationOutcomeUnknown) {
					t.Fatalf("terminal CAS ignored late Host claim: %v", err)
				}
				retained, err := store.GetInvocation(t.Context(), child.ID)
				if err != nil || retained.Status != harness.InvocationAccepted || c.calls.Load() != 1 || !harness.ActiveCancellationFence(protocolItems(t, store, started.Turn.ID)) {
					t.Fatalf("terminal observation signed unknown Host cleanup: %+v %v", retained, err)
				}
			})
		}
	}
}

func TestCancellationProtocolLegacyIntentCannotInheritCurrentNativeCustody(t *testing.T) {
	for _, backend := range generationBackends {
		t.Run(backend, func(t *testing.T) {
			f := newProtocolFixture(t, backend, nil)
			now := generationClock{}.Now()
			raw, err := json.Marshal(map[string]string{"boundary": "cancel_scope_pending", "runID": f.root.ExecutionRefID})
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = f.store.AppendItem(t.Context(), harness.Item{ID: "legacy-original-cancel", TurnID: f.started.Turn.ID, RunID: f.root.ExecutionRefID, Kind: harness.ItemDiagnostic, Status: harness.ItemStarted, ParentItemID: harness.CancellationFenceParentItemID, Payload: raw, CreatedAt: now, UpdatedAt: now})
			if err != nil {
				t.Fatal(err)
			}
			before, err := f.runtime.Load(t.Context(), f.root.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.r.Cancel(t.Context(), f.started.Turn.ID, "do not re-sign historical custody"); !errors.Is(err, harness.ErrCancellationOutcomeUnknown) {
				t.Fatal(err)
			}
			after, err := f.runtime.Load(t.Context(), f.root.ExecutionRefID)
			if err != nil || after.Run.Revision != before.Run.Revision || !harness.ActiveCancellationFence(protocolItems(t, f.store, f.started.Turn.ID)) {
				t.Fatalf("legacy intent upgraded or mutated Kernel: %v", err)
			}
		})
	}
}
