package harness_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type noEntryExpiryStore struct {
	*memory.Store
	clock  *resumeTurnClock
	expire bool
	grant  kernel.ChildExecutionGrant
}

func (s *noEntryExpiryStore) CheckOwnedPhysicalEntry(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, v kernel.Snapshot) (kernel.OwnedPhysicalEntryWindow, error) {
	if s.expire {
		s.grant = g
		s.expire = false
		s.clock.now = l.ExpiresAt.Add(time.Second)
	}
	return s.Store.CheckOwnedPhysicalEntry(ctx, g, l, v)
}

type noEntryReceiptFaultStore struct {
	*harness.MemoryStore
	mode  string
	fired bool
}

var errNoEntryReceipt = errors.New("lost harness no-entry receipt")

func (s *noEntryReceiptFaultStore) AppendItem(ctx context.Context, item harness.Item) (harness.Item, bool, error) {
	if !s.fired && s.mode != "" && strings.Contains(string(item.Payload), "native_agent_callback_not_entered") {
		s.fired = true
		if s.mode == "before-write" {
			return harness.Item{}, false, errNoEntryReceipt
		}
		_, _, err := s.MemoryStore.AppendItem(ctx, item)
		if err != nil {
			return harness.Item{}, false, err
		}
		return harness.Item{}, false, errNoEntryReceipt
	}
	return s.MemoryStore.AppendItem(ctx, item)
}

func TestNoEntryRetirementClosesHarnessDispatch(t *testing.T) {
	for _, mode := range []string{"direct", "before-write", "after-write"} {
		t.Run(mode, func(t *testing.T) { testNoEntryRetirementClosesHarnessDispatch(t, mode) })
	}
}
func testNoEntryRetirementClosesHarnessDispatch(t *testing.T, mode string) {
	clock := newResumeTurnClock()
	base := &noEntryExpiryStore{Store: memory.NewStoreWithExecutionClock(clock), clock: clock}
	hostStore := &noEntryReceiptFaultStore{MemoryStore: harness.NewMemoryStore()}
	if mode != "direct" {
		hostStore.mode = mode
	}
	calls := 0
	provider := resumeTurnModelFunc(func(context.Context, model.Request) (model.Response, error) {
		calls++
		return model.Response{Content: "original result", Continue: calls == 1}, nil
	})
	configure := func(d *agent.Dependencies) {
		d.RunExecution = &agent.RunExecution{OwnerID: "audit", LeaseDuration: time.Minute}
		d.Execution = agent.ExecutionPolicy{Model: agent.CallPolicy{Timeout: time.Second, MaxAttempts: 1}, Tool: agent.CallPolicy{Timeout: time.Second, MaxAttempts: 1}}
	}
	host, rt := newResumeTurnHarness(t, base, hostStore, provider, clock, configure)
	initial, e := host.StartAgentTurn(t.Context(), testStartRequest())
	if e != nil || initial.Turn.Status != harness.TurnRunning || calls != 1 {
		t.Fatalf("fixture start: %v %s %d", e, initial.Turn.Status, calls)
	}
	root, ok := harness.TopLevelInvocation(initial)
	if !ok {
		t.Fatal("root absent")
	}
	before, e := rt.Load(t.Context(), root.ExecutionRefID)
	if e != nil {
		t.Fatal(e)
	}
	base.expire = true
	_, e = host.ResumeTurn(t.Context(), initial.Turn.ID)
	t.Logf("first resume: %v", e)
	firstErr := e
	if !errors.Is(e, kernel.ErrExecutionConflict) || calls != 1 || base.grant.ID == "" {
		t.Fatalf("not actual pre-entry denial: %v %d", e, calls)
	}
	receipt, re := rt.LoadOwnedRetirement(t.Context(), base.grant.ID)
	if re != nil || !receipt.Valid() || receipt.Phase != "ACKNOWLEDGED" || !receipt.Evidence.Completion.MatchesNotEnteredSnapshot(base.grant, before) {
		t.Fatalf("Kernel no-entry not retired: %+v %v", receipt, re)
	}
	if _, e = rt.LookupChildExecution(t.Context(), root.ExecutionRefID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("Kernel fence remains: %v", e)
	}
	items, e := hostStore.ListItems(t.Context(), initial.Turn.ID, 0, 1000)
	if e != nil {
		t.Fatal(e)
	}
	pending := harness.PendingExecutionDispatch(items, "")
	for _, item := range items {
		if phase, ok := harness.VerifiedExecutionDispatchGrant(item); ok && phase == "resume" {
			boundary, ok := harness.VerifiedExecutionDispatchReceipt(item, items)
			t.Logf("resume dispatch %s receipt=%t boundary=%q", item.ID, ok, boundary)
		}
	}
	t.Logf("Kernel retired; Harness pending=%t", pending)
	if pending != (mode == "before-write") || (mode != "direct" && (!hostStore.fired || !errors.Is(firstErr, errNoEntryReceipt))) {
		t.Fatalf("unexpected dispatch persistence outcome mode=%s pending=%t error=%v", mode, pending, e)
	}
	// Reopen the Runner rather than relying on its original call frame.
	host, _ = newResumeTurnHarness(t, base, hostStore, provider, clock, configure)
	after, e := host.ResumeTurn(t.Context(), initial.Turn.ID)
	t.Logf("second resume: status=%s error=%v model calls=%d", after.Turn.Status, e, calls)
	if e != nil || after.Turn.Status != harness.TurnCompleted || calls != 2 {
		t.Fatalf("proven unentered callback did not close Harness dispatch/retry: pending=%t error=%v", pending, e)
	}
}

func TestNoEntryRetirementPreservesPreparedCustody(t *testing.T) {
	clock := newResumeTurnClock()
	base := &noEntryExpiryStore{Store: memory.NewStoreWithExecutionClock(clock), clock: clock}
	rt, e := kernel.New(kernel.Dependencies{Store: base, Clock: clock})
	if e != nil {
		t.Fatal(e)
	}
	calls := 0
	r, e := agent.NewRunner(agent.Dependencies{Runtime: rt, Clock: clock, Model: resumeTurnModelFunc(func(context.Context, model.Request) (model.Response, error) {
		calls++
		return model.Response{Content: "not called"}, nil
	})})
	if e != nil {
		t.Fatal(e)
	}
	prepared, e := r.PrepareRun(t.Context(), agent.StartRequest{ID: "prepared-audit", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "never dispatched"})
	if e != nil || calls != 0 || !r.VerifiedDeferredRootControl(t.Context(), rt, prepared) {
		t.Fatalf("initial prepared custody missing: %v", e)
	}
	base.expire = true
	out, e := r.Resume(t.Context(), prepared.Run.ID, prepared.Run.Revision)
	if !errors.Is(e, kernel.ErrExecutionConflict) || calls != 0 || kernel.ManagedDigest(out) != kernel.ManagedDigest(prepared) {
		t.Fatalf("fixture failed: %v %d", e, calls)
	}
	receipt, re := rt.LoadOwnedRetirement(t.Context(), base.grant.ID)
	if re != nil || receipt.Phase != "ACKNOWLEDGED" || !receipt.Evidence.Completion.MatchesNotEnteredSnapshot(base.grant, prepared) {
		t.Fatalf("no-entry not retired: %v", re)
	}
	verified := r.VerifiedDeferredRootControl(t.Context(), rt, prepared)
	t.Logf("unchanged prepared source + exact acknowledged no-entry: original native cancellation custody=%t", verified)
	if !verified {
		t.Fatal("no-effect admission invalidated actual earlier prepared custody")
	}
}

func TestNoEntryApprovalPreservesPendingDecision(t *testing.T) {
	clock := newResumeTurnClock()
	base := &noEntryExpiryStore{Store: memory.NewStoreWithExecutionClock(clock), clock: clock}
	store := harness.NewMemoryStore()
	rt, err := kernel.New(kernel.Dependencies{Store: base, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := harness.NewFrozenApprovalPolicy(store)
	if err != nil {
		t.Fatal(err)
	}
	registry := newApprovalTestRegistry(t)
	approvals, err := interaction.New(rt)
	if err != nil {
		t.Fatal(err)
	}
	provider := &approvalTestModel{}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: provider, Catalog: registry, Executor: registry, Approvals: interactionadapter.New(approvals), ApprovalPolicies: []plugin.ApprovalPolicy{policy}, Limits: agent.Limits{MaxLLMCalls: 3, MaxToolCalls: 2}, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	host, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: direct, Store: store, Clock: clock, Catalog: registry})
	if err != nil {
		t.Fatal(err)
	}
	config := harness.ConfigSnapshot{ToolPolicies: []harness.ToolPolicySnapshot{{Key: approvalTestToolKey, DefinitionVersion: "v1", ApprovalCapability: approvalTestCapabilityPerCall, ApprovalMode: "always"}}, AgentSpec: harness.AgentSpec{ToolKeys: []string{approvalTestToolKey}}}
	waiting := startApprovalHarness(t, host, config)
	checkpoint := latestApprovalCheckpoint(t, waiting)
	base.expire = true
	request := harness.ResolveApprovalRequest{CheckpointID: checkpoint, Decision: harness.ApprovalApprove, Comment: "unentered attempt"}
	_, err = host.ResolveApproval(t.Context(), waiting.Turn.ID, request)
	if !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("expected denied entry: %v", err)
	}
	still, err := host.Load(t.Context(), waiting.Turn.ID)
	if err != nil || still.Turn.Status != harness.TurnWaitingInput || latestApprovalCheckpoint(t, still) != checkpoint || approvalItemCount(still.Items, harness.ItemCompleted) != 0 || provider.calls != 1 || harness.PendingExecutionDispatch(still.Items, "") {
		t.Fatalf("unentered approval changed original pending request or dispatched effects: %v", err)
	}
	request.Comment = "actual authorized decision"
	next, err := host.ResolveApproval(t.Context(), waiting.Turn.ID, request)
	if err != nil || approvalItemCount(next.Items, harness.ItemCompleted) != 1 {
		t.Fatalf("original approval no longer actionable: %v", err)
	}
}
