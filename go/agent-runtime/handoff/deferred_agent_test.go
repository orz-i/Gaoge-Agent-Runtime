package handoff_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type deferredModel struct{ calls int }

func (m *deferredModel) Generate(context.Context, model.Request) (model.Response, error) {
	m.calls++
	return model.Response{Content: "done"}, nil
}

type deferredHost struct {
	calls int
	after error
}

func (*deferredHost) Name() string { return "deferred-test-host" }
func (h *deferredHost) Run(ctx context.Context, _ plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	h.calls++
	s, e := next(ctx)
	return s, errors.Join(e, h.after)
}
func deferredFixture(t *testing.T, host *deferredHost) (*kernel.Runtime, *agent.Runner, *deferredModel) {
	t.Helper()
	rt, e := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if e != nil {
		t.Fatal(e)
	}
	m := &deferredModel{}
	deps := agent.Dependencies{Runtime: rt, Model: m}
	if host != nil {
		deps.RunMiddleware = []plugin.RunMiddleware{host}
	}
	r, e := agent.NewRunner(deps)
	if e != nil {
		t.Fatal(e)
	}
	return rt, r, m
}
func TestDeferredAgentPreparesAndOriginalRunnerContinues(t *testing.T) {
	host := &deferredHost{}
	rt, r, m := deferredFixture(t, host)
	c, e := handoff.NewDeferredAgent(r)
	if e != nil {
		t.Fatal(e)
	}
	parent := verifiedHandoffParent()
	d := handoff.Delegation{ID: "deferred", MemberID: "local", MemberRevision: "original", ChildRunID: "deferred-child", Goal: "finish", Instructions: "original instructions", Model: "test", ModelOptions: json.RawMessage(`{"temperature":0}`), Status: handoff.StatusQueued}
	// Cancelling the parent Tool context after Prepare must not own the later
	// worker's context. No physical generation is allowed inside StartOrLoad.
	ctx, cancel := context.WithCancel(t.Context())
	got, e := c.StartOrLoad(ctx, parent, d)
	cancel()
	if !errors.Is(e, handoff.ErrChildPending) || got.Status != handoff.StatusRunning || m.calls != 0 || host.calls != 1 {
		t.Fatalf("prepare=%+v err=%v model=%d host=%d", got, e, m.calls, host.calls)
	}
	before, e := rt.Load(t.Context(), d.ChildRunID)
	if e != nil {
		t.Fatal(e)
	}
	request := agent.StartRequest{ID: d.ChildRunID, Actor: parent.Run.Actor, Thread: parent.Run.Thread, RequestID: parent.Run.ID + ":" + d.ID, Goal: d.Goal, Instructions: d.Instructions, Model: d.Model, ModelOptions: d.ModelOptions, DeadlineAt: parent.Run.DeadlineAt, Limits: d.Limits}
	if !r.VerifyFrozenStartRequest(before, request) || !handoff.VerifiedChildRun(d, parent.Run, before) {
		t.Fatal("original frozen request changed")
	}
	if !c.ObservesLocalKernelRunner(rt) || !c.UsesLocalAgentRunner(rt, r) || c.OwnsLocalKernelRunner(rt) {
		t.Fatal("observation/continuation incorrectly merged with cancellation")
	}
	_, e = c.StartOrLoad(t.Context(), parent, d)
	if !errors.Is(e, handoff.ErrChildPending) || host.calls != 1 || m.calls != 0 {
		t.Fatalf("duplicate prepared again: %v", e)
	}
	after, e := rt.Load(t.Context(), d.ChildRunID)
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("duplicate changed snapshot", e)
	}
	done, e := r.Resume(t.Context(), before.Run.ID, before.Run.Revision)
	if e != nil || done.Run.Status != kernel.RunStatusCompleted || m.calls != 1 {
		t.Fatalf("worker=%+v err=%v model=%d", done.Run, e, m.calls)
	}
	final, e := c.Refresh(t.Context(), parent, d)
	if e != nil || final.Status != handoff.StatusCompleted || m.calls != 1 {
		t.Fatalf("refresh=%+v err=%v", final, e)
	}
	foreign := parent
	foreign.Run.Actor.ActorID = "foreign"
	if _, e = c.Refresh(t.Context(), foreign, d); !errors.Is(e, handoff.ErrChildRunUnverified) {
		t.Fatalf("foreign origin accepted: %v", e)
	}
}
func TestDeferredAgentProofsRejectReplacementAndRoutedIdentity(t *testing.T) {
	rt, r, _ := deferredFixture(t, nil)
	c, e := handoff.NewDeferredAgent(r)
	if e != nil {
		t.Fatal(e)
	}
	other, e := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: &deferredModel{}})
	if e != nil {
		t.Fatal(e)
	}
	foreign, _, _ := deferredFixture(t, nil)
	if c.UsesLocalAgentRunner(rt, other) || c.UsesLocalAgentRunner(foreign, r) || c.ObservesLocalKernelRunner(foreign) || c.OwnsLocalKernelRunner(foreign) {
		t.Fatal("replacement authority accepted")
	}
	if !c.OwnsLocalKernelRunner(rt) {
		t.Fatal("bare Agent original ownership lost")
	}
	routed, e := handoff.NewRouted(handoff.ChildRunnerResolverFunc(func(context.Context, handoff.Delegation) (handoff.ChildRunner, error) { return r, nil }))
	if e != nil {
		t.Fatal(e)
	}
	if routed.UsesLocalAgentRunner(rt, r) || routed.ObservesLocalKernelRunner(rt) || routed.OwnsLocalKernelRunner(rt) {
		t.Fatal("routed identity promoted to static")
	}
	if _, e = handoff.NewDeferredAgent(nil); !errors.Is(e, handoff.ErrInvalidDelegation) {
		t.Fatal("nil accepted", e)
	}
}
func TestDeferredAgentPreservesWholeCallbackUnknown(t *testing.T) {
	sentinel := errors.New("host tail outcome unknown")
	rt, r, m := deferredFixture(t, &deferredHost{after: sentinel})
	c, e := handoff.NewDeferredAgent(r)
	if e != nil {
		t.Fatal(e)
	}
	parent := verifiedHandoffParent()
	d := handoff.Delegation{ID: "unknown", MemberID: "local", ChildRunID: "unknown-child", Goal: "finish", Model: "test", Status: handoff.StatusQueued}
	_, e = c.StartOrLoad(t.Context(), parent, d)
	if !errors.Is(e, sentinel) || !errors.Is(e, agent.ErrRootOutcomeUnknown) || m.calls != 0 {
		t.Fatalf("unknown lost: %v model=%d", e, m.calls)
	}
	before, e := rt.Load(t.Context(), d.ChildRunID)
	if e != nil {
		t.Fatal(e)
	}
	grant, e := rt.LookupChildExecution(t.Context(), before.Run.ID)
	if e != nil {
		t.Fatal(e)
	}
	completion, e := rt.LookupOwnedPhysicalCallbackReturn(t.Context(), grant)
	if e != nil || completion.HostCompleted {
		t.Fatalf("unknown completion=%+v err=%v", completion, e)
	}
	if _, e = rt.LookupOwnedRetirementForRun(t.Context(), before.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("unknown retired: %v", e)
	}
	_, e = r.Resume(t.Context(), before.Run.ID, before.Run.Revision)
	if !errors.Is(e, agent.ErrRootOutcomeUnknown) || m.calls != 0 {
		t.Fatalf("unknown replayed: %v model=%d", e, m.calls)
	}
	after, e := rt.Load(t.Context(), d.ChildRunID)
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("unknown mutated", e)
	}
	retained, e := rt.LookupChildExecution(t.Context(), before.Run.ID)
	if e != nil || !reflect.DeepEqual(grant, retained) {
		t.Fatal("unknown grant changed", e)
	}
	retainedCompletion, e := rt.LookupOwnedPhysicalCallbackReturn(t.Context(), grant)
	if e != nil || !reflect.DeepEqual(completion, retainedCompletion) {
		t.Fatal("unknown completion changed", e)
	}
}
