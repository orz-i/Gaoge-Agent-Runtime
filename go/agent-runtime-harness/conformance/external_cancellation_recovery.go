package conformance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

type externalCancelKernelPorts interface {
	nativeAuthorityKernelPorts
	kernel.ReturnedRootSettlementStore
	VerifyBoundedToolControl(context.Context, kernel.Snapshot) error
}

var errExternalCancellationCut = errors.New("original external cancellation acknowledgement interrupted")

type externalCancelCut struct {
	externalCancelKernelPorts
	phase          string
	recoveries     int
	hit            bool
	beforeRecovery func()
}

func (s *externalCancelCut) ApplyOwnedPhysicalRecovery(ctx context.Context, g kernel.ChildExecutionGrant, rev uint64, m kernel.StoreMutation, p kernel.OwnedPhysicalRecovery) (kernel.Snapshot, error) {
	s.recoveries++
	phase := fmt.Sprintf("recovery-%d", s.recoveries)
	if hook := s.beforeRecovery; hook != nil {
		s.beforeRecovery = nil
		hook()
	}
	if s.phase == phase+"-before" {
		s.hit = true
		return kernel.Snapshot{}, errExternalCancellationCut
	}
	out, e := s.externalCancelKernelPorts.ApplyOwnedPhysicalRecovery(ctx, g, rev, m, p)
	if e == nil && s.phase == phase+"-after" {
		s.hit = true
		return kernel.Snapshot{}, errExternalCancellationCut
	}
	return out, e
}
func (s *externalCancelCut) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	if s.phase == "retirement-before" {
		s.hit = true
		return errExternalCancellationCut
	}
	e := s.externalCancelKernelPorts.FinishChildExecution(ctx, g, p)
	if e == nil && s.phase == "retirement-after" {
		s.hit = true
		return errExternalCancellationCut
	}
	return e
}
func (s *externalCancelCut) ApplyControl(ctx context.Context, id string, rev uint64, g kernel.ControlGuard, m kernel.StoreMutation) (kernel.Snapshot, bool, error) {
	if g.Kind == kernel.ControlCancel && s.phase == "cancel-before" {
		s.hit = true
		return kernel.Snapshot{}, false, errExternalCancellationCut
	}
	out, revoked, e := s.externalCancelKernelPorts.ApplyControl(ctx, id, rev, g, m)
	if e == nil && g.Kind == kernel.ControlCancel && s.phase == "cancel-after" {
		s.hit = true
		return kernel.Snapshot{}, false, errExternalCancellationCut
	}
	return out, revoked, e
}

func NativeExternalCancellationRecovery(t *testing.T, factory func(*testing.T) NativeAuthorityFixture) {
	for _, mode := range []string{"no-effect", "committed"} {
		for _, phase := range []string{"recovery-1-before", "recovery-1-after", "recovery-2-before", "recovery-2-after", "recovery-3-before", "recovery-3-after", "retirement-before", "retirement-after", "cancel-before", "cancel-after"} {
			t.Run(mode+"/"+phase, func(t *testing.T) {
				ctx := t.Context()
				f := factory(t)
				base := f.Kernel
				cut := &externalCancelCut{externalCancelKernelPorts: base.(externalCancelKernelPorts)}
				f.Kernel = cut
				o := &externalCancelOwner{mode: mode, controls: map[string]agent.ExternalToolControl{}, deadline: time.Now().UTC().Add(time.Hour)}
				b := composeExternalCancel(t, f, o)
				start, e := b.host.StartAgentTurn(ctx, externalCancelRequest(true))
				if e != nil {
					t.Fatal(e)
				}
				root, _ := harness.TopLevelInvocation(start)
				id := root.ExecutionRefID
				source, e := b.agent.LoadRun(ctx, id)
				if e != nil {
					t.Fatal(e)
				}
				originalGrant, e := b.runtime.LookupChildExecution(ctx, id)
				if e != nil {
					t.Fatal(e)
				}
				control := agent.ExternalToolControl{Actor: source.Run.Actor, RunID: id, CallID: "browser", OperationID: "operation-" + id, InputDigest: strings.Repeat("a", 64), ControlID: "original-control"}
				o.controls[id] = control
				cut.phase = phase
				if strings.HasPrefix(phase, "cancel-") || strings.HasPrefix(phase, "retirement-") {
					cut.phase = ""
				}
				_, cancelErr := b.host.Cancel(ctx, start.Turn.ID, "original")
				if cancelErr == nil {
					t.Fatal("cancel before owner")
				}
				if !cut.hit {
					e = b.host.VerifyExternalToolControlScope(ctx, start.Turn.ID, id, control)
					if e != nil && !cut.hit {
						t.Fatal(e)
					}
				}
				if !cut.hit {
					if strings.HasPrefix(phase, "retirement-") {
						cut.phase = phase
					}
					if mode == "no-effect" {
						_, e = b.agent.ReconcileExternalToolNoEffectForControl(ctx, source.Run.Revision, control)
					} else {
						_, e = b.agent.ReconcileExternalToolForControl(ctx, id, source.Run.Revision, "browser")
					}
					if e != nil && !cut.hit {
						t.Fatal(e)
					}
				}
				if !cut.hit {
					cut.phase = phase
					_, continuationErr := b.host.ContinueExternalToolCancellation(ctx, start.Turn.ID, id, control.ControlID, "original")
					if continuationErr != nil && !cut.hit {
						t.Fatalf("continuation before simulated interruption: %v", continuationErr)
					}
				}
				if !cut.hit {
					t.Fatalf("cut missed: %s", phase)
				}
				f.Kernel = base
				if f.Reopen != nil {
					f.Kernel, f.Host, f.Relations = f.Reopen()
				}
				b = composeExternalCancel(t, f, o)
				current, e := b.agent.LoadRun(ctx, id)
				if e != nil {
					t.Fatal(e)
				}
				ledger, e := b.runtime.BudgetStore().LoadBudget(ctx, start.Turn.ID)
				if e != nil {
					t.Fatal(e)
				}
				if strings.HasPrefix(phase, "recovery-1") || strings.HasPrefix(phase, "recovery-2") || phase == "recovery-3-before" {
					if ledger.Reservations["tool:"+id+":browser"].Status != budget.ReservationHeld || ledger.RunCancelled(id) {
						t.Fatal("unknown quota released")
					}
				}
				if current.Run.Status != kernel.RunStatusCancelled {
					if e = b.host.VerifyExternalToolControlScope(ctx, start.Turn.ID, id, control); e != nil {
						t.Fatalf("scope recovery: %v", e)
					}
					if mode == "no-effect" {
						_, e = b.agent.ReconcileExternalToolNoEffectForControl(ctx, source.Run.Revision, control)
					} else {
						_, e = b.agent.ReconcileExternalToolForControl(ctx, id, source.Run.Revision, "browser")
					}
					if e != nil {
						t.Fatalf("receipt-only recovery: %v", e)
					}
				}
				if _, e = b.host.ContinueExternalToolCancellation(ctx, start.Turn.ID, id, control.ControlID, "original"); e != nil {
					t.Fatalf("same-control final recovery: %v", e)
				}
				retired, e := b.runtime.LoadOwnedRetirement(ctx, originalGrant.ID)
				if e != nil || !retired.Valid() || retired.Evidence.Grant != originalGrant {
					t.Fatalf("retirement rotated: %v", e)
				}
				ledger, e = b.runtime.BudgetStore().LoadBudget(ctx, start.Turn.ID)
				want := 0
				if mode == "committed" {
					want = 1
				}
				if e != nil || !ledger.RunCancelled(id) || ledger.View("").Usage.ToolCalls != want || o.prepared != 1 || o.models != 1 {
					t.Fatalf("original effects/accounting: %v %+v models=%d tools=%d", e, ledger.View(""), o.models, o.prepared)
				}
			})
		}
	}
}

// Race a paused receipt CAS with all public continuation paths. The original
// Grant must remain the only owner, and a foreign final control never succeeds.
func NativeExternalCancellationConcurrent(t *testing.T, factory func(*testing.T) NativeAuthorityFixture) {
	f := factory(t)
	base := f.Kernel
	cut := &externalCancelCut{externalCancelKernelPorts: base.(externalCancelKernelPorts)}
	f.Kernel = cut
	o := &externalCancelOwner{mode: "no-effect", controls: map[string]agent.ExternalToolControl{}, deadline: time.Now().UTC().Add(time.Hour)}
	b := composeExternalCancel(t, f, o)
	ctx := t.Context()
	start, e := b.host.StartAgentTurn(ctx, externalCancelRequest(true))
	if e != nil {
		t.Fatal(e)
	}
	root, _ := harness.TopLevelInvocation(start)
	id := root.ExecutionRefID
	s, e := b.agent.LoadRun(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = b.host.Cancel(ctx, start.Turn.ID, "original"); !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatal(e)
	}
	c := agent.ExternalToolControl{Actor: s.Run.Actor, RunID: id, CallID: "browser", OperationID: "operation-" + id, InputDigest: strings.Repeat("a", 64), ControlID: "original-control"}
	o.controls[id] = c
	if e = b.host.VerifyExternalToolControlScope(ctx, start.Turn.ID, id, c); e != nil {
		t.Fatal(e)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	cut.beforeRecovery = func() { close(entered); <-release }
	done := make(chan error, 1)
	go func() { _, err := b.agent.ReconcileExternalToolNoEffectForControl(ctx, s.Run.Revision, c); done <- err }()
	<-entered
	current, e := b.agent.LoadRun(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			parked, e := b.agent.Resume(ctx, id, current.Run.Revision)
			if e != nil || parked.Run.Revision != current.Run.Revision {
				errs <- fmt.Errorf("resume escaped: %w", e)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, e := b.host.ContinueExternalToolCancellation(ctx, start.Turn.ID, id, "foreign", "foreign")
		if e == nil {
			errs <- errors.New("foreign control escaped")
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, e := b.agent.NotifyExternalToolResultReady(ctx, agent.ExternalToolResultSignal{RunID: id, CallID: "browser", OperationID: c.OperationID, InputDigest: c.InputDigest, SignalID: "late-signal"})
		if e == nil {
			errs <- errors.New("notification escaped")
		}
	}()
	wg.Wait()
	close(errs)
	close(release)
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	for e := range errs {
		t.Error(e)
	}
	if _, e = b.host.ContinueExternalToolCancellation(ctx, start.Turn.ID, id, c.ControlID, "original"); e != nil {
		t.Fatal(e)
	}
}
