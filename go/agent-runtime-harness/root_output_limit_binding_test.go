package harness_test

import (
	"context"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// An older custom starter can compile while accidentally dropping a newly
// supplied ceiling. It must not let Harness certify a different native Run as
// satisfying the frozen Turn, or let a later native runner resume that Run.
type rootOutputDifferentStarter struct {
	*agent.Runner
	ceiling int64
	pending bool
}

func (s rootOutputDifferentStarter) StartRun(ctx context.Context, request agent.StartRequest) (kernel.Snapshot, error) {
	request.MaxModelOutputTokens = s.ceiling
	if s.pending {
		return s.Runner.StartRun(ctx, request)
	}
	return s.PrepareRun(ctx, request)
}

type rootOutputBindingFixture struct {
	runs       *memory.Store
	store      *harness.MemoryStore
	original   harness.SessionOptions
	invocation harness.Invocation
	firstError error
	model      *agentSessionModel
}

func newRootOutputBindingFixture(t *testing.T, frozen, actual int64, pending bool) rootOutputBindingFixture {
	t.Helper()
	runs, store := memory.NewStore(), harness.NewMemoryStore()
	var runStore kernel.Store = runs
	if pending {
		runStore = &resumeTurnFaultStore{Store: runs}
	}
	rt, err := kernel.New(kernel.Dependencies{Store: runStore})
	if err != nil {
		t.Fatal(err)
	}
	client := &agentSessionModel{}
	loop, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: client, PrepareModelRequest: harness.MaterializeContextWindowRequest})
	if err != nil {
		t.Fatal(err)
	}
	host, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: rootOutputDifferentStarter{Runner: loop, ceiling: actual, pending: pending},
		Store: store, Context: runtimecontext.NewManager(runtimecontext.Dependencies{})})
	if err != nil {
		t.Fatal(err)
	}
	options := resumeTurnSessionOptions()
	options.Config.MaxModelOutputTokens = frozen
	session, err := host.OpenSession(options)
	if err != nil {
		t.Fatal(err)
	}
	_, firstErr := session.Prompt(t.Context(), "original", "original task")
	sessionID, err := harness.SessionID(options.HostThread)
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := harness.TurnID(sessionID, harness.HostRef{Kind: "conversation_turn", ID: "original"})
	if err != nil {
		t.Fatal(err)
	}
	invocationID, err := harness.AgentInvocationID(turnID, "original")
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := store.GetInvocation(t.Context(), invocationID)
	if err != nil {
		t.Fatal(err)
	}
	return rootOutputBindingFixture{runs: runs, store: store, original: options, invocation: invocation, firstError: firstErr, model: client}
}

func TestRootModelOutputCeilingRejectsDifferentStartProjection(t *testing.T) {
	f := newRootOutputBindingFixture(t, 128, 512, false)
	if !errors.Is(f.firstError, harness.ErrConflict) || len(f.model.requests) != 0 {
		t.Fatalf("Config cap 128 accepted native Run cap 512: err=%v generations=%d", f.firstError, len(f.model.requests))
	}
	turn, err := f.store.GetTurn(t.Context(), f.invocation.TurnID)
	if err != nil || turn.Status != harness.TurnAccepted {
		t.Fatalf("mismatched Start projected execution: %+v err=%v", turn, err)
	}
}

func TestRootModelOutputCeilingRejectsDifferentRunBeforeResume(t *testing.T) {
	for _, pending := range []bool{false, true} {
		for _, managed := range []bool{false, true} {
			name := "prepared"
			if pending {
				name = "pending-intent"
			}
			if managed {
				name += "/managed"
			}
			t.Run(name, func(t *testing.T) {
				f := newRootOutputBindingFixture(t, 128, 512, pending)
				client := &agentSessionModel{}
				options := agentSessionOptions(t, f.runs, f.store, client)
				options.Session.HostThread, options.Session.Actor, options.Session.Thread = f.original.HostThread, f.original.Actor, f.original.Thread
				options.Session.Transcript = nil
				options.Session.Config.MaxModelOutputTokens = 512 // Current handle cannot replace original cap 128.
				options.Context.ResolveWindow = func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
					return runtimecontext.ModelWindow{ContextTokens: 16384, ReservedOutputTokens: 512}, nil
				}
				if managed {
					rt, err := kernel.New(kernel.Dependencies{Store: f.runs, ManagedRecoveryHost: f.store})
					if err != nil {
						t.Fatal(err)
					}
					options.Runtime, options.Context.ManagedRecovery = rt, true
				}
				before, err := options.Runtime.Load(t.Context(), f.invocation.ExecutionRefID)
				if err != nil {
					t.Fatal(err)
				}
				session, err := harness.OpenAgentSession(options)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = session.Resume(t.Context(), "original"); !errors.Is(err, harness.ErrConflict) || len(client.requests) != 0 {
					t.Fatalf("mismatched frozen Config/Run resumed: err=%v generations=%d", err, len(client.requests))
				}
				after, err := options.Runtime.Load(t.Context(), f.invocation.ExecutionRefID)
				if err != nil || after.Run.Revision != before.Run.Revision {
					t.Fatalf("rejected cap mismatch changed Run revision: before=%d after=%d err=%v", before.Run.Revision, after.Run.Revision, err)
				}
			})
		}
	}
}

func TestRootModelOutputCeilingMismatchStillAllowsProtectiveCancel(t *testing.T) {
	for _, failConfig := range []bool{false, true} {
		name := "readable-config"
		if failConfig {
			name = "unavailable-config"
		}
		t.Run(name, func(t *testing.T) {
			f := newRootOutputBindingFixture(t, 128, 512, false)
			client := &agentSessionModel{}
			rt, err := kernel.New(kernel.Dependencies{Store: f.runs})
			if err != nil {
				t.Fatal(err)
			}
			loop, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: client})
			if err != nil {
				t.Fatal(err)
			}
			store := &protectiveCancelReadFaultStore{Store: f.store}
			if failConfig {
				store.failMode = "config"
			}
			host, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: loop, Store: store})
			if err != nil {
				t.Fatal(err)
			}
			cancelled, cancelErr := host.Cancel(t.Context(), f.invocation.TurnID, "owner stop")
			actual, err := rt.Load(t.Context(), f.invocation.ExecutionRefID)
			if err != nil || actual.Run.Status != kernel.RunStatusCancelled || len(client.requests) != 0 {
				t.Fatalf("output policy prevented protective stop: status=%s err=%v cancel=%v generations=%d", actual.Run.Status, err, cancelErr, len(client.requests))
			}
			if !failConfig {
				if cancelErr != nil || cancelled.Turn.Status != harness.TurnCancelled {
					t.Fatalf("stopped Run could not settle cancellation: status=%s err=%v", cancelled.Turn.Status, cancelErr)
				}
				if refreshed, err := host.Refresh(t.Context(), f.invocation.TurnID); err != nil || refreshed.Turn.Status != harness.TurnCancelled {
					t.Fatalf("terminal cancellation was not readable: status=%s err=%v", refreshed.Turn.Status, err)
				}
			}
		})
	}
}

func TestRootModelOutputCeilingLegacyZeroRemainsUnspecified(t *testing.T) {
	f := newRootOutputBindingFixture(t, 0, 512, false)
	if f.firstError != nil || len(f.model.requests) != 0 {
		t.Fatalf("legacy unspecified output ceiling changed: %v generations=%d", f.firstError, len(f.model.requests))
	}
}
