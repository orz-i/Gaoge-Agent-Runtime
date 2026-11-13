package conformance

import (
	"context"
	"errors"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"testing"
	"time"
)

type retirementAgentDeathStore struct {
	managedStorePorts
	dead           bool
	hits, ackCalls int
	grant          kernel.ChildExecutionGrant
	acquired       kernel.ExecutionLease
}

func (s *retirementAgentDeathStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	if s.dead {
		return errRetirementLostACK
	}
	if e := s.managedStorePorts.FinishChildExecution(ctx, g); e != nil {
		return e
	}
	s.dead = true
	s.hits++
	s.grant = g
	return errRetirementLostACK
}
func (s *retirementAgentDeathStore) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	if s.dead {
		return kernel.OwnedRetirementReceipt{}, errRetirementLostACK
	}
	return s.managedStorePorts.LoadOwnedRetirement(ctx, id)
}
func (s *retirementAgentDeathStore) LookupOwnedRetirementForRun(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	if s.dead {
		return kernel.OwnedRetirementReceipt{}, errRetirementLostACK
	}
	return s.managedStorePorts.LookupOwnedRetirementForRun(ctx, id)
}
func (s *retirementAgentDeathStore) AcknowledgeOwnedRetirement(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.OwnedRetirementAcknowledgement) (kernel.OwnedRetirementAckResult, error) {
	s.ackCalls++
	if s.dead {
		return kernel.OwnedRetirementAckResult{}, errRetirementLostACK
	}
	return s.managedStorePorts.AcknowledgeOwnedRetirement(ctx, g, p)
}

func (s *retirementAgentDeathStore) AcquireExecution(ctx context.Context, r kernel.ExecutionAcquireRequest) (kernel.ExecutionLease, error) {
	l, e := s.managedStorePorts.AcquireExecution(ctx, r)
	if e == nil {
		s.acquired = l
	}
	return l, e
}

type retirementAgentModel struct {
	continues bool
	calls     int
	request   model.Request
	response  model.Response
}

func (m *retirementAgentModel) Generate(_ context.Context, r model.Request) (model.Response, error) {
	m.calls++
	m.request = model.CloneRequest(r)
	m.response = model.Response{Content: "done", Continue: m.continues, Usage: &model.Usage{InputTokens: 1, OutputTokens: 1}}
	return model.CloneResponse(m.response), nil
}
func runRetirementActualAgentRecovery(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	b := factory(t)
	base, ok := b.Store.(managedStorePorts)
	if !ok {
		t.Fatal("native ports missing")
	}
	fault := &retirementAgentDeathStore{managedStorePorts: base}
	rt := retirementRuntime(t, fault)
	physical := &retirementAgentModel{}
	old, e := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: physical})
	if e != nil {
		t.Fatal(e)
	}
	req := agent.StartRequest{ID: "actual-finished-root", RequestID: "original-request", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "original"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "original"}, Goal: "finish exactly once", Model: "fixture"}
	original, e := old.StartRun(t.Context(), req)
	if !errors.Is(e, agent.ErrRootOutcomeUnknown) || !errors.Is(e, errRetirementLostACK) || fault.hits != 1 || physical.calls != 1 {
		t.Fatalf("actual Finish death cut: %v hits=%d sends=%d", e, fault.hits, physical.calls)
	}
	before, e := base.Load(t.Context(), req.ID)
	if e != nil || kernel.ManagedDigest(before) != kernel.ManagedDigest(original) {
		t.Fatalf("original source: %v", e)
	}
	retired, e := base.LoadOwnedRetirement(t.Context(), fault.grant.ID)
	if e != nil || retired.Phase != "RETIRED" {
		t.Fatalf("Finish not durable: %v", e)
	}
	if _, e = base.LookupChildExecution(t.Context(), req.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("grant survived Finish: %v", e)
	}
	originalRequest, originalResponse := model.CloneRequest(physical.request), model.CloneResponse(physical.response)
	reopened := b.Reopen()
	freshRT := retirementRuntime(t, reopened)
	fresh, e := agent.NewRunner(agent.Dependencies{Runtime: freshRT, Model: physical})
	if e != nil {
		t.Fatal(e)
	}
	if e = fresh.ReconcileRootPhysicalExecution(t.Context(), req.ID); e != nil {
		t.Fatalf("public fresh runID recovery: %v", e)
	}
	after, e := freshRT.Load(t.Context(), req.ID)
	if e != nil || kernel.ManagedDigest(after) != kernel.ManagedDigest(before) || physical.calls != 1 || kernel.ManagedDigest(physical.request) != kernel.ManagedDigest(originalRequest) || kernel.ManagedDigest(physical.response) != kernel.ManagedDigest(originalResponse) {
		t.Fatalf("recovery mutated/replayed original source: %v", e)
	}
	acknowledged, e := freshRT.LoadOwnedRetirement(t.Context(), fault.grant.ID)
	if e != nil || acknowledged.Phase != "ACKNOWLEDGED" || acknowledged.Identity() != retired.Identity() {
		t.Fatalf("original receipt not acknowledged: %v", e)
	}
	if e = fresh.ReconcileRootPhysicalExecution(t.Context(), req.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("public runID selected historical ACK: %v", e)
	}
}

func runRetirementTwoGenerations(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	b := factory(t)
	base, ok := b.Store.(managedStorePorts)
	if !ok {
		t.Fatal("native ports missing")
	}
	physical := &retirementAgentModel{continues: true}
	makeRunner := func(store kernel.Store, owner string) *agent.Runner {
		call := agent.CallPolicy{Timeout: time.Second, MaxAttempts: 1}
		r, e := agent.NewRunner(agent.Dependencies{Runtime: retirementRuntime(t, store), Model: physical, Execution: agent.ExecutionPolicy{Model: call, Tool: call}, RunExecution: &agent.RunExecution{OwnerID: owner, LeaseDuration: 5 * time.Second}})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	first := &retirementAgentDeathStore{managedStorePorts: base}
	req := agent.StartRequest{ID: "two-retired-generations", RequestID: "original", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "original"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "original"}, Goal: "two real yields", Model: "fixture", Limits: agent.Limits{MaxLLMCalls: 3}}
	original, e := makeRunner(first, "first-owner").StartRun(t.Context(), req)
	if !errors.Is(e, agent.ErrRootOutcomeUnknown) || !errors.Is(e, errRetirementLostACK) || first.hits != 1 || physical.calls != 1 || original.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("first real yield/Finish death: %v", e)
	}
	r1, e := base.LoadOwnedRetirement(t.Context(), first.grant.ID)
	if e != nil || r1.Phase != "RETIRED" {
		t.Fatalf("first receipt: %v", e)
	}
	firstRequest, firstResponse := model.CloneRequest(physical.request), model.CloneResponse(physical.response)
	b.AdvanceTo(first.acquired.ExpiresAt.Add(100 * time.Millisecond))
	if e = base.CheckExecution(t.Context(), req.ID, &first.acquired); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("original lease not actually expired: %v", e)
	}
	second := &retirementAgentDeathStore{managedStorePorts: base}
	latest, e := makeRunner(second, "second-owner").Resume(t.Context(), req.ID, original.Run.Revision)
	if !errors.Is(e, agent.ErrRootOutcomeUnknown) || !errors.Is(e, errRetirementLostACK) || second.hits != 1 || physical.calls != 2 || latest.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("second real yield/Finish death: %v", e)
	}
	if second.acquired.Epoch != first.acquired.Epoch+1 || second.grant.ID == first.grant.ID || second.acquired.LeaseID == first.acquired.LeaseID {
		t.Fatal("second generation did not acquire new actual authority")
	}
	r2, e := base.LoadOwnedRetirement(t.Context(), second.grant.ID)
	if e != nil || r2.Phase != "RETIRED" {
		t.Fatalf("second receipt: %v", e)
	}
	unchanged, e := base.LoadOwnedRetirement(t.Context(), first.grant.ID)
	if e != nil || kernel.ManagedDigest(unchanged) != kernel.ManagedDigest(r1) {
		t.Fatal("first receipt changed before recovery")
	}
	if firstRequest.InvocationID == physical.request.InvocationID || kernel.ManagedDigest(firstResponse) != kernel.ManagedDigest(physical.response) {
		t.Fatal("two original model calls not distinct, unchanged yields")
	}
	secondRequest, secondResponse := model.CloneRequest(physical.request), model.CloneResponse(physical.response)
	freshStore := b.Reopen()
	fresh := makeRunner(freshStore, "recovery-owner")
	if e = fresh.ReconcileRootPhysicalExecution(t.Context(), req.ID); e != nil {
		t.Fatalf("unique current retirement blocked by historical retired row: %v", e)
	}
	freshRT := retirementRuntime(t, freshStore)
	current, e := freshRT.Load(t.Context(), req.ID)
	if e != nil || kernel.ManagedDigest(current) != kernel.ManagedDigest(latest) || physical.calls != 2 || kernel.ManagedDigest(physical.request) != kernel.ManagedDigest(secondRequest) || kernel.ManagedDigest(physical.response) != kernel.ManagedDigest(secondResponse) {
		t.Fatal("public recovery mutated source or sent model again")
	}
	old, e := freshRT.LoadOwnedRetirement(t.Context(), r1.Evidence.Grant.ID)
	if e != nil || kernel.ManagedDigest(old) != kernel.ManagedDigest(r1) {
		t.Fatal("historical unacknowledged retirement changed")
	}
	ack, e := freshRT.LoadOwnedRetirement(t.Context(), r2.Evidence.Grant.ID)
	if e != nil || ack.Phase != "ACKNOWLEDGED" || ack.Identity() != r2.Identity() {
		t.Fatalf("current r2 not exclusively ACKed: %v", e)
	}
	if e = fresh.ReconcileRootPhysicalExecution(t.Context(), req.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("historical r1 selected after r2 ACK: %v", e)
	}
}
