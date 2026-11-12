package conformance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// RunEA1RootAgentStartSettlementSuite covers only actual AR-S P3/P4.
// Memory uses a fresh owner over retained state; durable backends provide
// close/reopen or an independent pool. No bare Kernel admission substitutes
// for StartRun, and no test inserts a completion or settlement marker.
func RunEA1RootAgentStartSettlementSuite(t *testing.T, factory func(*testing.T) (kernel.Store, func() kernel.Store)) {
	for _, phase := range []string{"P3", "P4"} {
		for _, tool := range []bool{false, true} {
			kind := map[bool]string{false: "model-yield", true: "terminal-tool"}[tool]
			t.Run(phase+"/"+kind, func(t *testing.T) { ea1RootStartSettlement(t, factory, phase, tool, false) })
		}
	}
	t.Run("P3/committed-model-receipt-unknown-host", func(t *testing.T) { ea1RootStartSettlement(t, factory, "P3", false, true) })
}

type ea1StartSettlementStore struct {
	kernel.Store
	kernel.ExecutionStore
	kernel.ControlStore
	kernel.ChildExecutionStore
	kernel.OwnedPhysicalExecutionStore
	mode                                string
	fault                               error
	created                             func(kernel.Snapshot)
	markCalls, markCommits, finishCalls int
	original                            kernel.ChildExecutionGrant
}

func (s *ea1StartSettlementStore) Create(ctx context.Context, r kernel.Record, e []kernel.EventDraft) (kernel.Snapshot, error) {
	snap, err := s.Store.Create(ctx, r, e)
	if err == nil && s.created != nil {
		s.created(snap)
	}
	return snap, err
}
func (s *ea1StartSettlementStore) MarkChildExecutionSettled(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	s.markCalls++
	s.original = g
	if s.mode == "P3" {
		return s.fault
	}
	if e := s.ChildExecutionStore.MarkChildExecutionSettled(ctx, g, p); e != nil {
		return e
	}
	s.markCommits++
	if s.mode == "P4" {
		return s.fault
	}
	return nil
}
func (s *ea1StartSettlementStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	s.finishCalls++
	if s.mode == "P3" || s.mode == "P4" {
		return s.fault
	}
	return s.ChildExecutionStore.FinishChildExecution(ctx, g)
}

type ea1StartSettlementPhysical struct {
	mu              sync.Mutex
	rt              *kernel.Runtime
	tool            bool
	requests        []model.Request
	intents         []agent.ModelInvocation
	toolInvocations []plugin.ToolInvocation
	toolRequests    []tools.ExecutionRequest
	receipts        []tools.ExecutionResult
	hostCalls       int
	hostSends       int
	hostUnknown     bool
	hostFault       error
}

func (*ea1StartSettlementPhysical) ProviderName() string { return "ea1-settlement-provider" }
func (*ea1StartSettlementPhysical) BindingFingerprintForRequest(model.Request) string {
	return strings.Repeat("b", 64)
}
func (p *ea1StartSettlementPhysical) Name() string { return "ea1.original-start-callback" }
func (p *ea1StartSettlementPhysical) Run(ctx context.Context, inv plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	p.mu.Lock()
	p.hostCalls++
	p.mu.Unlock()
	if inv.Operation != plugin.RunStart || inv.ParentRunID != "" {
		return kernel.Snapshot{}, errors.New("not the original Root Start callback")
	}
	result, err := next(ctx)
	if p.hostUnknown {
		p.mu.Lock()
		p.hostSends++
		p.mu.Unlock()
		return result, errors.Join(err, p.hostFault)
	}
	return result, err
}
func (p *ea1StartSettlementPhysical) Generate(ctx context.Context, req model.Request) (model.Response, error) {
	snap, err := p.rt.Load(ctx, req.RunID)
	if err != nil {
		return model.Response{}, err
	}
	view, err := agent.ViewState(snap)
	if err != nil || len(view.ModelInvocations) != 1 {
		return model.Response{}, errors.New("missing actual original Model intent")
	}
	intent := view.ModelInvocations[0]
	if intent.Status != agent.ModelInvocationPending || !reflect.DeepEqual(intent.Request, req) || intent.ID != req.InvocationID || intent.ExecutionAttempt != 1 || intent.DispatchAttempt != 1 {
		return model.Response{}, errors.New("physical request not original admitted attempt")
	}
	p.mu.Lock()
	p.requests = append(p.requests, model.CloneRequest(req))
	p.intents = append(p.intents, intent)
	p.mu.Unlock()
	response := model.Response{ResponseID: "original-response", Content: "original durable yield", Continue: true, Usage: &model.Usage{InputTokens: 7, OutputTokens: 3}}
	if p.tool {
		response.Content = ""
		response.Continue = false
		response.ToolCalls = []tools.Call{{ID: "original-call", ToolKey: "effect", Arguments: json.RawMessage(`{"source":"original"}`)}}
	}
	return response, nil
}
func (p *ea1StartSettlementPhysical) Tool(ctx context.Context, inv plugin.ToolInvocation, next plugin.ToolNext) (tools.ExecutionResult, error) {
	p.mu.Lock()
	p.toolInvocations = append(p.toolInvocations, inv)
	p.mu.Unlock()
	return next(ctx)
}
func (p *ea1StartSettlementPhysical) execute(_ context.Context, req tools.ExecutionRequest) (tools.ExecutionResult, error) {
	result := tools.ExecutionResult{Content: json.RawMessage(`{"committed":"original"}`), Receipt: tools.Receipt{ExecutionID: req.Call.ID, Disposition: "committed"}}
	p.mu.Lock()
	p.toolRequests = append(p.toolRequests, req)
	p.receipts = append(p.receipts, result)
	p.mu.Unlock()
	return result, nil
}
func ea1RootStartSettlement(t *testing.T, factory func(*testing.T) (kernel.Store, func() kernel.Store), phase string, withTool, unknownHost bool) {
	t.Helper()
	backing, reopen := factory(t)
	fault := errors.New("test-only original settlement transport fault")
	mode := phase
	if unknownHost {
		mode = "unknown"
	}
	wrapped := &ea1StartSettlementStore{Store: backing, ExecutionStore: backing.(kernel.ExecutionStore), ControlStore: backing.(kernel.ControlStore), ChildExecutionStore: backing.(kernel.ChildExecutionStore), OwnedPhysicalExecutionStore: backing.(kernel.OwnedPhysicalExecutionStore), mode: mode, fault: fault}
	rt, err := kernel.New(kernel.Dependencies{Store: wrapped})
	if err != nil {
		t.Fatal(err)
	}
	physical := &ea1StartSettlementPhysical{rt: rt, tool: withTool, hostUnknown: unknownHost, hostFault: errors.New("original Host send outcome unknown")}
	definition := tools.Definition{Key: "effect", Name: "Original Effect", Description: "frozen terminal effect", InputSchema: json.RawMessage(`{"type":"object","properties":{"source":{"type":"string"}},"required":["source"]}`), Terminal: true}
	catalog, err := tools.NewRegistry([]tools.Registration{{Definition: definition, Handler: tools.HandlerFunc(physical.execute)}})
	if err != nil {
		t.Fatal(err)
	}
	makeRunner := func(runtime *kernel.Runtime, owner string) *agent.Runner {
		r, e := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: physical, Catalog: catalog, Executor: catalog, RunMiddleware: []plugin.RunMiddleware{physical}, ToolMiddleware: []plugin.ToolMiddleware{physical}, RunExecution: &agent.RunExecution{OwnerID: owner, LeaseDuration: 3 * time.Minute}})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	runner := makeRunner(rt, "original-start-owner")
	req := agent.StartRequest{ID: "root-start-settlement", RequestID: "original-request", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "original-thread"}, Goal: "original root settlement", Instructions: "Keep the original source.", Model: "fixture-model", ModelOptions: json.RawMessage(`{"temperature":0.1}`), Limits: agent.Limits{MaxLLMCalls: 2, MaxToolCalls: 1}}
	if withTool {
		req.ToolKeys = []string{"effect"}
	}
	var created kernel.Snapshot
	wrapped.created = func(s kernel.Snapshot) {
		created = s
		if s.Run.ParentRunID != "" || req.OriginFingerprint != "" || !runner.VerifyFrozenStartRequest(s, req) {
			t.Fatal("actual Create did not preserve original Root source")
		}
	}
	_, startErr := runner.StartRun(t.Context(), req)
	if created.Run.ID != req.ID {
		t.Fatal("actual StartRun never created original source")
	}
	if unknownHost {
		if !errors.Is(startErr, physical.hostFault) || wrapped.markCalls != 0 || wrapped.finishCalls != 0 {
			t.Fatalf("unknown Host retired after model receipt: %v", startErr)
		}
	} else if !errors.Is(startErr, fault) || !errors.Is(startErr, agent.ErrRootOutcomeUnknown) {
		t.Fatalf("cutpoint not retained: %v", startErr)
	}
	before, err := rt.Load(t.Context(), req.ID)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := rt.LookupChildExecution(t.Context(), req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if grant.ExecutionOwnerID != "original-start-owner" || grant.ExecutionLeaseID == "" || grant.ExecutionEpoch == 0 || grant.AdmissionOperation != "start" || len(grant.AdmissionSourceHash) != 64 || grant.ExpectedRevision != created.Run.Revision {
		t.Fatal("original owned source binding missing")
	}
	if phase == "P4" && !unknownHost {
		if wrapped.markCommits != 1 || wrapped.finishCalls == 0 {
			t.Fatal("actual original Mark commit/ACK-loss not reached")
		}
	} else if !unknownHost && (wrapped.markCommits != 0 || wrapped.markCalls < 2) {
		t.Fatal("P3 did not retain unmarked callback-return fact")
	}
	completion, completionErr := rt.LookupOwnedPhysicalCallbackReturn(t.Context(), grant)
	if phase == "P3" {
		if completionErr != nil || completion.HostCompleted == unknownHost || !completion.LeaseOwned || completion.ReturnRevision != before.Run.Revision {
			t.Fatalf("original callback return fact: %+v %v", completion, completionErr)
		}
	} else if !errors.Is(completionErr, kernel.ErrNotFound) {
		t.Fatalf("marked ACK-loss fabricated callback-return fallback: %v", completionErr)
	}
	ea1AssertStartSettlementReceipt(t, physical, req, definition, before, withTool)
	freshStore := reopen()
	freshRT, e := kernel.New(kernel.Dependencies{Store: freshStore})
	if e != nil {
		t.Fatal(e)
	}
	fresh := makeRunner(freshRT, "fresh-recovery-owner")
	assertUnchanged := func() {
		t.Helper()
		snap, e := freshRT.Load(t.Context(), req.ID)
		if e != nil || !reflect.DeepEqual(snap, before) {
			t.Fatal("recovery/denial changed full original snapshot")
		}
		ea1AssertStartSettlementReceipt(t, physical, req, definition, snap, withTool)
	}
	original, e := freshRT.LookupChildExecution(t.Context(), req.ID)
	if e != nil || original != grant {
		t.Fatal("reopen lost original immutable scope")
	}
	assertUnchanged()
	// Exact identity remains required even if the caller knows a real marker.
	for _, mutate := range []func(*kernel.ChildExecutionGrant){func(g *kernel.ChildExecutionGrant) { g.ID += "-foreign" }, func(g *kernel.ChildExecutionGrant) { g.ExecutionEpoch++ }, func(g *kernel.ChildExecutionGrant) { g.AdmissionOperation = "resume" }, func(g *kernel.ChildExecutionGrant) { g.AdmissionSourceHash = strings.Repeat("0", 64) }} {
		wrong := grant
		mutate(&wrong)
		if e = freshStore.(kernel.ChildExecutionStore).FinishChildExecution(t.Context(), wrong); !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatalf("foreign authority retired original: %v", e)
		}
		same, e := freshRT.LookupChildExecution(t.Context(), req.ID)
		if e != nil || same != grant {
			t.Fatal("denied identity changed grant")
		}
		assertUnchanged()
	}
	if phase == "P3" {
		if e = freshStore.(kernel.ChildExecutionStore).FinishChildExecution(t.Context(), grant); !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatalf("receipt alone retired unmarked scope: %v", e)
		}
		assertUnchanged()
	}
	e = fresh.ReconcileRootPhysicalExecution(t.Context(), req.ID)
	if unknownHost {
		if !errors.Is(e, agent.ErrRootOutcomeUnknown) {
			t.Fatalf("receipt retired unknown Host: %v", e)
		}
		same, e := freshRT.LookupChildExecution(t.Context(), req.ID)
		if e != nil || same != grant {
			t.Fatal("unknown Host scope lost")
		}
		retained, e := freshRT.LookupOwnedPhysicalCallbackReturn(t.Context(), grant)
		if e != nil || retained != completion {
			t.Fatal("unknown Host completion changed")
		}
		assertUnchanged()
		return
	}
	if e != nil {
		t.Fatalf("original settlement could not recover: %v", e)
	}
	if _, e = freshRT.LookupChildExecution(t.Context(), req.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("scope not retired: %v", e)
	}
	if e = fresh.ReconcileRootPhysicalExecution(t.Context(), req.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("duplicate reconciliation invented work: %v", e)
	}
	assertUnchanged()
}
func ea1AssertStartSettlementReceipt(t *testing.T, p *ea1StartSettlementPhysical, req agent.StartRequest, definition tools.Definition, s kernel.Snapshot, withTool bool) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) != 1 || len(p.intents) != 1 || p.hostCalls != 1 || (p.hostUnknown && p.hostSends != 1) {
		t.Fatal("physical callback replayed")
	}
	inv := p.intents[0]
	actual := p.requests[0]
	canonical := model.CloneRequest(actual)
	canonical.InvocationID = ""
	encoded, e := json.Marshal(struct {
		Request             model.Request `json:"request"`
		CatalogFingerprints []string      `json:"catalogFingerprints"`
		Provider            string        `json:"provider"`
		BindingFingerprint  string        `json:"bindingFingerprint"`
	}{canonical, inv.CatalogFingerprints, inv.Provider, inv.BindingFingerprint})
	digest := sha256.Sum256(encoded)
	if e != nil || inv.RequestHash != hex.EncodeToString(digest[:]) || !reflect.DeepEqual(inv.Request, actual) || actual.RunID != req.ID || actual.Model != req.Model || !reflect.DeepEqual(actual.ModelOptions, req.ModelOptions) || inv.ID != actual.InvocationID || inv.Provider != p.ProviderName() || inv.BindingFingerprint != strings.Repeat("b", 64) || inv.SourceRevision == 0 || inv.ExecutionAttempt != 1 || inv.DispatchAttempt != 1 {
		t.Fatal("full original Model request/hash/provider/attempt mismatch")
	}
	view, e := agent.ViewState(s)
	if e != nil || len(view.ModelInvocations) != 1 {
		t.Fatalf("persisted receipt missing: %v", e)
	}
	settled := view.ModelInvocations[0]
	if settled.ID != inv.ID || settled.RequestHash != inv.RequestHash || settled.Provider != inv.Provider || settled.BindingFingerprint != inv.BindingFingerprint || settled.ExecutionAttempt != 1 || settled.DispatchAttempt != 1 || settled.Status != agent.ModelInvocationConsumed || settled.ProviderResponseID != "original-response" || settled.CompletedAt == nil || settled.ConsumedAt == nil || settled.Usage == nil || settled.Usage.InputTokens != 7 || settled.Usage.OutputTokens != 3 {
		t.Fatal("consumed receipt not original physical result")
	}
	if s.Run.ID != req.ID || s.Run.ParentRunID != "" || s.Run.Actor != req.Actor || s.Run.Thread != req.Thread || s.Run.RequestID != req.RequestID || s.Run.Goal != req.Goal || !reflect.DeepEqual(view.Budget.Limits, req.Limits) {
		t.Fatal("frozen Root source/budget changed")
	}
	if withTool {
		if s.Run.Status != kernel.RunStatusCompleted || len(p.toolInvocations) != 1 || len(p.toolRequests) != 1 || len(p.receipts) != 1 {
			t.Fatal("terminal Tool receipt/count missing")
		}
		ti := p.toolInvocations[0]
		tr := p.toolRequests[0]
		receipt := p.receipts[0]
		want := tools.ExecutionRequest{RunID: req.ID, Call: tools.Call{ID: "original-call", ToolKey: "effect", Arguments: json.RawMessage(`{"source":"original"}`)}}
		// Agent binds the public Call.Name from the original catalog definition.
		want.Call.Name = definition.Name
		if !reflect.DeepEqual(tr, want) || !reflect.DeepEqual(ti.Request, tr) || !reflect.DeepEqual(ti.Definition, definition) || ti.Run.Actor != req.Actor || ti.Run.Thread != req.Thread || receipt.Receipt.ExecutionID != tr.Call.ID || receipt.Receipt.Disposition != "committed" || string(receipt.Content) != `{"committed":"original"}` || view.Budget.Usage.ToolCalls != 1 {
			t.Fatal("full Tool definition/request/receipt source mismatch")
		}
	} else if len(p.toolRequests) != 0 || s.Run.Status != kernel.RunStatusRunning || view.Budget.Usage.ToolCalls != 0 {
		t.Fatal("model-only yield dispatched Tool or became terminal")
	}
}
