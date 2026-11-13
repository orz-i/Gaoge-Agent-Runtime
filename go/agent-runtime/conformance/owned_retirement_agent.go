package conformance

import (
	"context"
	"errors"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"testing"
)

type retirementAgentDeathStore struct {
	managedStorePorts
	dead           bool
	hits, ackCalls int
	grant          kernel.ChildExecutionGrant
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

type retirementAgentModel struct {
	calls    int
	request  model.Request
	response model.Response
}

func (m *retirementAgentModel) Generate(_ context.Context, r model.Request) (model.Response, error) {
	m.calls++
	m.request = model.CloneRequest(r)
	m.response = model.Response{Content: "done", Usage: &model.Usage{InputTokens: 1, OutputTokens: 1}}
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
