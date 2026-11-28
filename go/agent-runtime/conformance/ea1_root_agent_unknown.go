package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// RunEA1RootAgentUnknownSuite covers AR-S P2 only, using actual Agent StartRun
// with the native ExecutionLease protocol, never an arbitrary child Grant.
// Receipt-write failure is after the physical callback. Advancing Agent time
// expires only its model claim; native Store lease expiry is awaited in real
// wall-clock time before reopening, without private-table mutation.
func RunEA1RootAgentUnknownSuite(t *testing.T, factory func(*testing.T) (kernel.Store, func() kernel.Store)) {
	ea1AgentUnknownReceipts(t, factory, false)
}

// Root Resume and Child reconciliation differ in admission authority, not
// in the original physical request, receipt, or lease-expiry evidence.
func ea1AgentUnknownReceipts(t *testing.T, factory func(*testing.T) (kernel.Store, func() kernel.Store), child bool) {
	for _, kind := range []string{"model", "tool"} {
		t.Run(kind, func(t *testing.T) {
			backing, reopen := factory(t)
			native, ok := backing.(kernel.ExecutionStore)
			if !ok {
				t.Fatal("missing native ExecutionStore")
			}
			event := "agent.model_invocation.completed"
			if kind == "tool" {
				event = "tool.completed"
			}
			injected := errors.New("test-only physical response received but original receipt not committed")
			fault := &ea1AgentReceiptFault{Store: backing, ExecutionStore: native, OwnedPhysicalExecutionStore: backing.(kernel.OwnedPhysicalExecutionStore),
				ControlStore: backing.(kernel.ControlStore), ChildExecutionStore: backing.(kernel.ChildExecutionStore), ExecutionQuarantineStore: backing.(kernel.ExecutionQuarantineStore), event: event, injected: injected}
			rt, err := kernel.New(kernel.Dependencies{Store: fault})
			if err != nil {
				t.Fatal(err)
			}
			physical := &ea1UnknownAgentPhysical{tool: kind == "tool", rt: rt}
			catalog, err := tools.NewRegistry([]tools.Registration{{
				Definition: tools.Definition{Key: "effect", Name: "Effect", InputSchema: json.RawMessage(`{"type":"object"}`)},
				Handler:    tools.HandlerFunc(physical.execute),
			}})
			if err != nil {
				t.Fatal(err)
			}
			makeRunner := func(runtime *kernel.Runtime, owner string, offset time.Duration) *agent.Runner {
				r, e := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: physical, Catalog: catalog, Executor: catalog,
					Clock: ea1UnknownAgentClock{offset: offset}, RunExecution: &agent.RunExecution{OwnerID: owner, LeaseDuration: 2 * time.Second},
					Execution: ea1StoreBackedExecutionPolicy()})
				if e != nil {
					t.Fatal(e)
				}
				return r
			}
			first := makeRunner(rt, "first-owner", 0)
			request := agent.StartRequest{ID: "unknown-root", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "original physical attempt"}
			var parent kernel.Snapshot
			if child {
				parent = ea1ChildRecoveryParent(t, rt)
				request.ID, request.ParentRunID, request.RequestID = "unknown-child", parent.Run.ID, "original-child-effect"
				request.OriginFingerprint = agent.FrozenOriginFingerprint([]byte(`{"source":"original-child"}`))
			}
			if kind == "tool" {
				request.ToolKeys = []string{"effect"}
			}
			_, firstErr := first.StartRun(t.Context(), request)
			if !errors.Is(firstErr, injected) || fault.failures != 1 {
				t.Fatalf("receipt failure not reached: %v failures=%d", firstErr, fault.failures)
			}
			pending, err := rt.Load(t.Context(), request.ID)
			if err != nil {
				t.Fatal(err)
			}
			view, err := agent.ViewState(pending)
			if err != nil || len(view.ModelInvocations) != 1 || pending.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("original pending source missing: %v", err)
			}
			calls, toolCalls := physical.counts()
			if calls != 1 || (kind == "tool" && toolCalls != 1) || (kind == "model" && toolCalls != 0) {
				t.Fatalf("first physical counts model=%d tool=%d", calls, toolCalls)
			}
			invocation := view.ModelInvocations[0]
			if invocation.ID == "" || invocation.RunID != request.ID || invocation.RequestHash == "" || invocation.ExecutionAttempt != 1 {
				t.Fatal("original invocation/attempt missing")
			}
			if kind == "model" && invocation.Status != agent.ModelInvocationPending {
				t.Fatal("uncommitted Model receipt fabricated")
			}
			if kind == "tool" {
				var state struct {
					ToolDispatchID string `json:"toolDispatchID"`
				}
				if e := json.Unmarshal(pending.State, &state); e != nil || state.ToolDispatchID != "original-tool-call" {
					t.Fatalf("original Tool dispatch marker missing: %v", e)
				}
				if invocation.Status != agent.ModelInvocationConsumed || invocation.CompletedAt == nil || invocation.ConsumedAt == nil || invocation.RequestHash == "" ||
					len(invocation.Response.ToolCalls) != 1 || invocation.Response.ToolCalls[0].ID != "original-tool-call" ||
					invocation.Response.ToolCalls[0].ToolKey != "effect" || !bytes.Equal(invocation.Response.ToolCalls[0].Arguments, json.RawMessage(`{"source":"original"}`)) {
					t.Fatal("Tool fixture lost exact consumed Model receipt")
				}
			}
			physical.assertOriginal(t, request, invocation)
			originalJSON, err := json.Marshal(pending)
			if err != nil {
				t.Fatal(err)
			}
			// Await real Store expiry, not Agent clock advancement or private SQL.
			lease := fault.lastLease()
			if lease.RunID != request.ID || lease.OwnerID != "first-owner" {
				t.Fatal("fault did not observe native original lease")
			}
			ea1AwaitNativeExpiry(t, native, fault.renewals.latest(t, lease))
			if err = native.CheckExecution(t.Context(), request.ID, &lease); !errors.Is(err, kernel.ErrExecutionConflict) {
				t.Fatalf("old native owner still valid after real expiry: %v", err)
			}
			var grant kernel.ChildExecutionGrant
			if child {
				grant, err = rt.LookupChildExecution(t.Context(), request.ID)
				if err != nil || grant.ChildRunID != request.ID {
					t.Fatalf("original child grant: %v", err)
				}
			}
			fresh := reopen()
			ownerRT, err := kernel.New(kernel.Dependencies{Store: fresh})
			if err != nil {
				t.Fatal(err)
			}
			durable, err := ownerRT.Load(t.Context(), request.ID)
			if err != nil {
				t.Fatal(err)
			}
			durableJSON, err := json.Marshal(durable)
			if err != nil || !bytes.Equal(originalJSON, durableJSON) {
				t.Fatal("original pending source changed across reopen")
			}
			physical.rt = ownerRT
			next := makeRunner(ownerRT, "fresh-owner", 3*time.Minute)
			var resumeErr error
			if child {
				resumeErr = next.ReconcileChildPhysicalGrant(t.Context(), request.ID)
				if !errors.Is(resumeErr, kernel.ErrChildExecutionBusy) {
					t.Fatalf("unknown child reconciliation: %v", resumeErr)
				}
				ea1CompBarrierUnchanged(t, ownerRT, parent)
				same, lookupErr := ownerRT.LookupChildExecution(t.Context(), request.ID)
				if lookupErr != nil || same != grant {
					t.Fatal("unknown recovery changed original grant")
				}
			} else {
				_, resumeErr = next.Resume(t.Context(), request.ID, durable.Run.Revision)
			}
			afterCalls, afterTools := physical.counts()
			// No provider idempotency or affirmative no-effect proof exists here.
			// Reusing InvocationID is not permission for another physical send.
			if afterCalls != calls || afterTools != toolCalls {
				t.Errorf("EA-06 UNKNOWN replay: model %d->%d tool %d->%d resumeErr=%v originalInvocation=%s attempt=%d", calls, afterCalls, toolCalls, afterTools, resumeErr, invocation.ID, invocation.ExecutionAttempt)
			}
			if !child && kind == "tool" && !errors.Is(resumeErr, agent.ErrToolOutcomeUnknown) {
				t.Errorf("Tool unknown must remain typed quarantine: %v", resumeErr)
			}
			// Model has no established ModelUnknown error contract. Assert the safety
			// property instead of inventing an error or accepting TTL as no-effect.
			final, err := ownerRT.Load(t.Context(), request.ID)
			if err != nil {
				t.Fatal(err)
			}
			finalJSON, err := json.Marshal(final)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(originalJSON, finalJSON) {
				t.Errorf("UNKNOWN recovery changed original full Run/invocation/attempt state; resumeErr=%v", resumeErr)
			}
		})
	}
}

type ea1AgentReceiptFault struct {
	kernel.Store
	kernel.ExecutionStore
	kernel.ControlStore
	kernel.ChildExecutionStore
	kernel.ExecutionQuarantineStore
	kernel.OwnedPhysicalExecutionStore
	event    string
	injected error
	failures int
	mu       sync.Mutex
	lease    kernel.ExecutionLease
	renewals ea1LeaseRenewals
}

func (s *ea1AgentReceiptFault) ApplyExecution(ctx context.Context, lease kernel.ExecutionLease, revision uint64, mutation kernel.StoreMutation) (kernel.Snapshot, error) {
	s.mu.Lock()
	s.lease = lease
	s.mu.Unlock()
	for _, event := range mutation.Events {
		if event.Type == s.event && s.failures == 0 {
			s.failures++
			return kernel.Snapshot{}, s.injected
		}
	}
	return s.ExecutionStore.ApplyExecution(ctx, lease, revision, mutation)
}
func (s *ea1AgentReceiptFault) RenewExecution(ctx context.Context, req kernel.ExecutionRenewRequest) (kernel.ExecutionLease, error) {
	renewed, err := s.ExecutionStore.RenewExecution(ctx, req)
	if err == nil {
		s.renewals.record(req.Lease, renewed)
	}
	return renewed, err
}

func (s *ea1AgentReceiptFault) lastLease() kernel.ExecutionLease {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lease
}

type ea1UnknownAgentClock struct{ offset time.Duration }

func (c ea1UnknownAgentClock) Now() time.Time { return time.Now().UTC().Add(c.offset) }

type ea1UnknownAgentPhysical struct {
	mu sync.Mutex
	// A successful Model may spend time reading its original durable intent.
	// The delay is used only by explicit slow-read receipt regression cases.
	modelReadDelay time.Duration
	tool           bool
	models         []model.Request
	rt             *kernel.Runtime
	intents        []agent.ModelInvocation
	tools          []tools.ExecutionRequest
}

func (p *ea1UnknownAgentPhysical) Generate(ctx context.Context, req model.Request) (model.Response, error) {
	if p.modelReadDelay > 0 {
		timer := time.NewTimer(p.modelReadDelay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return model.Response{}, ctx.Err()
		}
	}
	// Check the actual durable pending intent at the physical callback,
	// before normal consumption intentionally prunes the request payload.
	var intent agent.ModelInvocation
	if p.rt != nil {
		snapshot, err := p.rt.Load(ctx, req.RunID)
		if err != nil {
			return model.Response{}, err
		}
		view, err := agent.ViewState(snapshot)
		if err != nil || len(view.ModelInvocations) == 0 {
			return model.Response{}, errors.New("physical Model missing durable invocation")
		}
		intent = view.ModelInvocations[len(view.ModelInvocations)-1]
		actual, err := json.Marshal(req)
		if err != nil {
			return model.Response{}, err
		}
		persisted, err := json.Marshal(intent.Request)
		if err != nil {
			return model.Response{}, err
		}
		if intent.Status != agent.ModelInvocationPending || intent.ID != req.InvocationID || intent.RunID != req.RunID ||
			intent.RequestHash == "" || intent.ExecutionAttempt == 0 || !bytes.Equal(actual, persisted) {
			return model.Response{}, errors.New("physical Model request not bound to original durable pending source")
		}
	}
	p.mu.Lock()
	p.models = append(p.models, model.CloneRequest(req))
	p.intents = append(p.intents, intent)
	p.mu.Unlock()
	if p.tool {
		for _, message := range req.Messages {
			if message.Role == model.RoleTool {
				return model.Response{Content: "done", Usage: &model.Usage{InputTokens: 1, OutputTokens: 1}}, nil
			}
		}
		return model.Response{ToolCalls: []tools.Call{{ID: "original-tool-call", ToolKey: "effect", Arguments: json.RawMessage(`{"source":"original"}`)}}, Usage: &model.Usage{InputTokens: 1, OutputTokens: 1}}, nil
	}
	return model.Response{Content: "physical response", Usage: &model.Usage{InputTokens: 1, OutputTokens: 1}}, nil
}
func (p *ea1UnknownAgentPhysical) execute(_ context.Context, req tools.ExecutionRequest) (tools.ExecutionResult, error) {
	req.Call.Arguments = append(json.RawMessage(nil), req.Call.Arguments...)
	p.mu.Lock()
	p.tools = append(p.tools, req)
	p.mu.Unlock()
	return tools.ExecutionResult{Content: json.RawMessage(`{"committed":true}`), Receipt: tools.Receipt{ExecutionID: req.Call.ID, Disposition: "committed"}}, nil
}
func (p *ea1UnknownAgentPhysical) counts() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.models), len(p.tools)
}
func (p *ea1UnknownAgentPhysical) assertOriginal(t *testing.T, request agent.StartRequest, invocation agent.ModelInvocation) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.models) != 1 || p.models[0].RunID != request.ID || p.models[0].InvocationID != invocation.ID {
		t.Fatal("physical Model not original invocation")
	}
	physical, _ := json.Marshal(p.models[0])
	if len(p.intents) != 1 || p.intents[0].ID != invocation.ID || p.intents[0].RequestHash != invocation.RequestHash || p.intents[0].ExecutionAttempt != invocation.ExecutionAttempt {
		t.Fatal("consumed receipt lost original callback source fingerprint or attempt")
	}
	persisted, _ := json.Marshal(p.intents[0].Request)
	if !bytes.Equal(physical, persisted) {
		t.Fatal("physical Model payload differs from original persisted request")
	}
	if !p.tool {
		current, err := json.Marshal(invocation.Request)
		if err != nil || invocation.Status != agent.ModelInvocationPending || !bytes.Equal(physical, current) {
			t.Fatal("unknown Model did not retain exact pending request")
		}
	}
	if p.tool && (len(p.tools) != 1 || p.tools[0].RunID != request.ID || p.tools[0].Call.ID != "original-tool-call" || p.tools[0].Call.ToolKey != "effect" || !bytes.Equal(p.tools[0].Call.Arguments, json.RawMessage(`{"source":"original"}`))) {
		t.Fatal("physical Tool not original dispatch/source")
	}
}
