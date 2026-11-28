package conformance

import (
	"context"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type noEntryKernelPorts interface {
	kernel.Store
	kernel.ExecutionStore
	kernel.ControlStore
	kernel.ChildExecutionStore
	kernel.OwnedPhysicalExecutionStore
	kernel.OwnedRetirementStore
	kernel.ExecutionCustodyStore
}

var errNoEntryCut = errors.New("entry observation unavailable")
var errNoEntryFinishCut = errors.New("native retirement commit interrupted")

type noEntryKernelCut struct {
	noEntryKernelPorts
	deny, loseEvidence, loseFinish  bool
	grant                           kernel.ChildExecutionGrant
	beforeAdmission, afterAdmission func()
}

func (s *noEntryKernelCut) BeginOwnedPhysicalExecution(ctx context.Context, p kernel.OwnedPhysicalAdmission) (kernel.OwnedPhysicalAdmissionResult, error) {
	if hook := s.beforeAdmission; hook != nil {
		s.beforeAdmission = nil
		hook()
	}
	out, err := s.noEntryKernelPorts.BeginOwnedPhysicalExecution(ctx, p)
	if hook := s.afterAdmission; hook != nil && err == nil {
		s.afterAdmission = nil
		hook()
	}
	return out, err
}

func (s *noEntryKernelCut) CheckOwnedPhysicalEntry(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, v kernel.Snapshot) (kernel.OwnedPhysicalEntryWindow, error) {
	if s.deny {
		s.deny = false
		s.grant = g
		// Fail BEFORE entry; all actual Record/Mark/Finish/ACK writes
		// are performed by the real native Store.
		return kernel.OwnedPhysicalEntryWindow{}, errNoEntryCut
	}
	return s.noEntryKernelPorts.CheckOwnedPhysicalEntry(ctx, g, l, v)
}
func (s *noEntryKernelCut) RecordPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, rev uint64, o kernel.PhysicalCallbackOutcome, p kernel.PhysicalCallbackReturn) error {
	if s.loseEvidence && g == s.grant {
		return errNoEntryCut
	}
	return s.noEntryKernelPorts.RecordPhysicalCallbackReturn(ctx, g, rev, o, p)
}

func (s *noEntryKernelCut) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	if s.loseFinish && g == s.grant {
		s.loseFinish = false
		return errNoEntryFinishCut
	}
	return s.noEntryKernelPorts.FinishChildExecution(ctx, g, p)
}

type noEntryHostCut struct {
	harness.Store
	beforeIntent func()
}

func (s *noEntryHostCut) AppendItem(ctx context.Context, i harness.Item) (harness.Item, bool, error) {
	if s.beforeIntent != nil && i.ParentItemID == harness.CancellationFenceParentItemID {
		hook := s.beforeIntent
		s.beforeIntent = nil
		hook()
	}
	return s.Store.AppendItem(ctx, i)
}

type noEntryModel struct{ calls int }

func (m *noEntryModel) Generate(context.Context, model.Request) (model.Response, error) {
	m.calls++
	return model.Response{Content: "original receipt", Continue: m.calls == 1}, nil
}

// NoEntryCancellation verifies the same Host/Kernel retirement protocol
// across independent Store instances. The interleaving inserts the rejected
// Resume AFTER Cancel's first inspection but BEFORE its intent is committed.
// Native Finish repair must also work under the elected cancellation fence;
// no Host Resume dispatch is ever written.
func NoEntryCancellation(t *testing.T, factory func(*testing.T) (kernel.Store, harness.Store, func() (kernel.Store, harness.Store))) {
	for _, mode := range []string{"receipt-lost", "during-election", "unknown-no-entry", "cancel-before-admission", "cancel-after-admission"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			backing, host, reopen := factory(t)
			ports, ok := backing.(noEntryKernelPorts)
			if !ok {
				t.Fatal("native fixture lacks original retirement ports")
			}
			cut := &noEntryKernelCut{noEntryKernelPorts: ports}
			hostCut := &noEntryHostCut{Store: host}
			m := &noEntryModel{}
			makeRunner := func(k kernel.Store, h harness.Store) (*harness.Runner, *kernel.Runtime) {
				t.Helper()
				rt, e := kernel.New(kernel.Dependencies{Store: k})
				if e != nil {
					t.Fatal(e)
				}
				native, e := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: m})
				if e != nil {
					t.Fatal(e)
				}
				r, e := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: native, Store: h})
				if e != nil {
					t.Fatal(e)
				}
				return r, rt
			}
			r, rt := makeRunner(cut, hostCut)
			initial, e := r.StartAgentTurn(ctx, harness.StartRequest{
				HostThread: harness.HostRef{Kind: "conversation", ID: "no-entry-thread"}, HostTurn: harness.HostRef{Kind: "turn", ID: "no-entry-turn"},
				Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "no-entry-thread"},
				Goal: "one original result", Config: harness.ConfigSnapshot{Environment: harness.VersionRef{ID: "test", Revision: 1}, AgentSpec: harness.AgentSpec{Model: "fixture"}},
			})
			if e != nil || initial.Turn.Status != harness.TurnRunning || m.calls != 1 {
				t.Fatalf("original Start: %+v %v calls=%d", initial.Turn, e, m.calls)
			}
			root, ok := harness.TopLevelInvocation(initial)
			if !ok {
				t.Fatal("root missing")
			}
			before, e := rt.Load(ctx, root.ExecutionRefID)
			if e != nil {
				t.Fatal(e)
			}
			if mode == "cancel-before-admission" || mode == "cancel-after-admission" {
				stop, _ := makeRunner(backing, host)
				var stopped harness.Snapshot
				var stopErr error
				hook := func() { stopped, stopErr = stop.Cancel(ctx, initial.Turn.ID, "competing stop") }
				if mode == "cancel-before-admission" {
					cut.beforeAdmission = hook
				} else {
					cut.afterAdmission = hook
				}
				_, resumeErr := r.ResumeTurn(ctx, initial.Turn.ID)
				if mode == "cancel-before-admission" {
					if stopErr != nil || stopped.Turn.Status != harness.TurnCancelled || resumeErr == nil || m.calls != 1 {
						t.Fatalf("native cancellation did not win before admission: stop=%s %v resume=%v calls=%d", stopped.Turn.Status, stopErr, resumeErr, m.calls)
					}
				} else if !errors.Is(stopErr, kernel.ErrChildExecutionBusy) || resumeErr != nil || m.calls != 2 {
					t.Fatalf("native admission did not win: stop=%v resume=%v calls=%d", stopErr, resumeErr, m.calls)
				}
				items, err := host.ListItems(ctx, initial.Turn.ID, 0, 1000)
				if err != nil || harness.PendingExecutionDispatch(items, root.ID) || harness.ActiveCancellationFence(items) {
					t.Fatalf("native competition left independent authority: %v", err)
				}
				for _, item := range items {
					if phase, ok := harness.VerifiedExecutionDispatchGrant(item); ok && phase == "resume" {
						t.Fatal("native competition wrote a Host Resume grant")
					}
				}
				return
			}
			failResume := func() {
				cut.deny = true
				cut.loseEvidence = mode == "unknown-no-entry"
				cut.loseFinish = mode != "unknown-no-entry"
				_, e := r.ResumeTurn(ctx, initial.Turn.ID)
				if !errors.Is(e, errNoEntryCut) || m.calls != 1 || cut.grant.ID == "" {
					t.Fatalf("original entry cut: %v calls=%d", e, m.calls)
				}
				if mode != "unknown-no-entry" && !errors.Is(e, errNoEntryFinishCut) {
					t.Fatalf("native Finish cut not reached: %v", e)
				}
				items, e := host.ListItems(ctx, initial.Turn.ID, 0, 1000)
				if e != nil || harness.PendingExecutionDispatch(items, root.ID) {
					t.Fatalf("native resume still wrote a Host dispatch: %v", e)
				}
				if mode != "unknown-no-entry" {
					returned, e := rt.LookupPhysicalCallbackReturn(ctx, cut.grant)
					if e != nil || !returned.MatchesNotEnteredSnapshot(cut.grant, before) {
						t.Fatalf("exact original no-entry missing: %v", e)
					}
				}
			}
			var cancelled harness.Snapshot
			if mode == "during-election" {
				hostCut.beforeIntent = failResume
				cancelled, e = r.Cancel(ctx, initial.Turn.ID, "original stop")
			} else {
				failResume()
				// No reliance on the original observer, wrapper, or Runner.
				backing, host = reopen()
				r, rt = makeRunner(backing, host)
				cancelled, e = r.Cancel(ctx, initial.Turn.ID, "original stop")
			}
			current, readErr := rt.Load(ctx, root.ExecutionRefID)
			items, itemErr := host.ListItems(ctx, initial.Turn.ID, 0, 1000)
			if readErr != nil || itemErr != nil || m.calls != 1 {
				t.Fatalf("control lost original identity or reentered model: %v %v calls=%d", readErr, itemErr, m.calls)
			}
			if mode == "unknown-no-entry" {
				if !errors.Is(e, kernel.ErrChildExecutionBusy) || kernel.ManagedDigest(current) != kernel.ManagedDigest(before) || harness.PendingExecutionDispatch(items, root.ID) {
					t.Fatalf("unknown retirement improperly granted cancellation: %v", e)
				}
				if g, e := rt.LookupChildExecution(ctx, root.ExecutionRefID); e != nil || g != cut.grant {
					t.Fatalf("unknown original native fence was erased: %v", e)
				}
				return
			}
			if e != nil || cancelled.Turn.Status != harness.TurnCancelled || current.Run.Status != kernel.RunStatusCancelled ||
				harness.PendingExecutionDispatch(items, root.ID) || harness.ActiveCancellationFence(items) {
				t.Fatalf("Cancel failed exact no-entry closure: %v turn=%s run=%s", e, cancelled.Turn.Status, current.Run.Status)
			}
			for _, item := range items {
				if phase, valid := harness.VerifiedExecutionDispatchGrant(item); valid && phase == "resume" {
					t.Fatal("native cancellation manufactured a Resume grant")
				}
			}
			receipt, e := rt.LoadOwnedRetirement(ctx, cut.grant.ID)
			if e != nil || !receipt.Valid() || !receipt.Evidence.Completion.MatchesNotEnteredSnapshot(cut.grant, before) {
				t.Fatalf("original no-entry retirement not recovered: %v", e)
			}
		})
	}
}
