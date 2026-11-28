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
			t.Run(phase+"/"+kind, func(t *testing.T) { ea1RootSettlement(t, factory, phase, tool, false, "start", false) })
		}
	}
	t.Run("P3/committed-model-receipt-unknown-host", func(t *testing.T) { ea1RootSettlement(t, factory, "P3", false, true, "start", false) })
}

type ea1StartSettlementStore struct {
	kernel.Store
	kernel.ExecutionStore
	kernel.ControlStore
	kernel.ChildExecutionStore
	kernel.OwnedPhysicalExecutionStore
	mode                                  string
	fault                                 error
	created                               func(kernel.Snapshot)
	markCalls, finishCommits, finishCalls int
	original                              kernel.ChildExecutionGrant
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
	return s.ChildExecutionStore.MarkChildExecutionSettled(ctx, g, p)
}
func (s *ea1StartSettlementStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	s.finishCalls++
	s.original = g
	if s.mode == "P3" {
		return s.fault
	}
	err := s.ChildExecutionStore.FinishChildExecution(ctx, g, p)
	if err == nil {
		s.finishCommits++
		if s.mode == "P4" {
			return s.fault
		}
	}
	return err
}

// P4 uses an actually committed final receipt; a new process observes it
// without invoking a callback or requiring any intermediate marker.
func ea1AssertFinishedOriginal(t *testing.T, rt *kernel.Runtime, g kernel.ChildExecutionGrant, s kernel.Snapshot) {
	t.Helper()
	r, e := rt.LoadOwnedRetirement(t.Context(), g.ID)
	if e != nil || !r.Valid() || r.Evidence.Grant != g || !reflect.DeepEqual(r.Evidence.Snapshot, s) || !r.Evidence.Completion.HostCompleted || !r.Evidence.Completion.LeaseOwned {
		t.Fatalf("original committed Finish missing: %v %+v", e, r)
	}
	if _, e = rt.LookupChildExecution(t.Context(), s.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("Finish retained grant: %v", e)
	}
}

type ea1StartSettlementPhysical struct {
	mu   sync.Mutex
	rt   *kernel.Runtime
	tool bool
	// Fault-injection latency before the physical Model reads its real Store.
	// Zero for all existing scenarios; used to prove timeout-independent P2.
	modelReadDelay  time.Duration
	requests        []model.Request
	responses       []model.Response
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
func (p *ea1StartSettlementPhysical) Generate(ctx context.Context, req model.Request) (model.Response, error) {
	if p.modelReadDelay > 0 {
		timer := time.NewTimer(p.modelReadDelay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return model.Response{}, ctx.Err()
		}
	}
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
	p.mu.Lock()
	p.responses = append(p.responses, model.CloneResponse(response))
	p.mu.Unlock()
	return response, nil
}
func (p *ea1StartSettlementPhysical) execute(ctx context.Context, req tools.ExecutionRequest) (tools.ExecutionResult, error) {
	snapshot, err := p.rt.Load(ctx, req.RunID)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	result := tools.ExecutionResult{Content: json.RawMessage(`{"committed":"original"}`), Receipt: tools.Receipt{ExecutionID: req.Call.ID, Disposition: "committed"}}
	p.mu.Lock()
	p.toolInvocations = append(p.toolInvocations, plugin.ToolInvocation{Run: snapshot.Run, Request: tools.CloneExecutionRequest(req)})
	p.toolRequests = append(p.toolRequests, req)
	p.receipts = append(p.receipts, result)
	p.mu.Unlock()
	return result, nil
}
func ea1RootSettlement(t *testing.T, factory func(*testing.T) (kernel.Store, func() kernel.Store), phase string, withTool, unknownHost bool, operation string, cancelBefore bool) {
	t.Helper()
	backing, reopen := factory(t)
	fault := errors.New("test-only original settlement transport fault")
	mode := phase
	if unknownHost {
		mode = "unknown"
	}
	initialMode := mode
	if operation == "resume" {
		initialMode = "setup"
	}
	wrapped := &ea1StartSettlementStore{Store: backing, ExecutionStore: backing.(kernel.ExecutionStore), ControlStore: backing.(kernel.ControlStore), ChildExecutionStore: backing.(kernel.ChildExecutionStore), OwnedPhysicalExecutionStore: backing.(kernel.OwnedPhysicalExecutionStore), mode: initialMode, fault: fault}
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
	otherOperation := "resume"
	if operation == "resume" {
		otherOperation = "start"
	}
	makeRunner := func(runtime *kernel.Runtime, owner string) *agent.Runner {
		r, e := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: physical, Catalog: catalog, Executor: catalog, RunExecution: &agent.RunExecution{OwnerID: owner, LeaseDuration: 3 * time.Minute}})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	owner := "original-" + operation + "-owner"
	runner := makeRunner(rt, owner)
	setup := runner
	if operation == "resume" {
		setup, err = agent.NewRunner(agent.Dependencies{Runtime: rt, Model: physical, Catalog: catalog, Executor: catalog, RunExecution: &agent.RunExecution{OwnerID: "prepare-only-owner", LeaseDuration: 3 * time.Minute}})
		if err != nil {
			t.Fatal(err)
		}
	}
	req := agent.StartRequest{ID: "root-" + operation + "-settlement", RequestID: "original-request", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "original-thread"}, Goal: "original root settlement", Instructions: "Keep the original source.", Model: "fixture-model", ModelOptions: json.RawMessage(`{"temperature":0.1}`), Limits: agent.Limits{MaxLLMCalls: 2, MaxToolCalls: 1}}
	if withTool {
		req.ToolKeys = []string{"effect"}
	}
	var created kernel.Snapshot
	wrapped.created = func(s kernel.Snapshot) {
		created = s
		if s.Run.ParentRunID != "" || req.OriginFingerprint != "" || !setup.VerifyFrozenStartRequest(s, req) {
			t.Fatal("actual Create did not preserve original Root source")
		}
	}
	var startErr error
	var expectedRevision uint64
	if operation == "resume" {
		prepared, err := setup.PrepareRun(t.Context(), req)
		if err != nil || created.Run.ID != req.ID || prepared.Run.Status != kernel.RunStatusRunning {
			t.Fatalf("actual PrepareRun did not create resumable source: %v", err)
		}
		if _, err = rt.LookupChildExecution(t.Context(), req.ID); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("setup scope not retired before Resume: %v", err)
		}
		physical.mu.Lock()
		setupDispatched := len(physical.requests) != 0 || len(physical.responses) != 0 || len(physical.toolRequests) != 0 || physical.hostCalls != 0
		physical.mu.Unlock()
		if setupDispatched {
			t.Fatal("PrepareRun dispatched physical work")
		}
		// Arm after real deferred creation and its setup scope retirement.
		wrapped.mode = mode
		wrapped.markCalls, wrapped.finishCommits, wrapped.finishCalls = 0, 0, 0
		expectedRevision = prepared.Run.Revision
		_, startErr = runner.Resume(t.Context(), req.ID, expectedRevision)
	} else {
		_, startErr = runner.StartRun(t.Context(), req)
		if created.Run.ID != req.ID {
			t.Fatal("actual StartRun never created original source")
		}
		expectedRevision = created.Run.Revision
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
	cancelSettled := func(runtime *kernel.Runtime) {
		t.Helper()
		if withTool {
			ea1CancelRejected(t, runtime, before, kernel.ChildExecutionGrant{}, kernel.ErrTerminal)
		} else {
			out, e := runtime.Cancel(t.Context(), req.ID, before.Run.Revision, "original cancel")
			if e != nil {
				t.Fatal("settled original receipt still blocked cancellation", e)
			}
			ea1CancelCommitted(t, runtime, before, out)
		}
	}
	if phase == "P4" && wrapped.finishCommits == 1 {
		if wrapped.markCalls != 0 || wrapped.finishCalls != 1 {
			t.Fatal("retirement was not one transaction")
		}
		ea1AssertStartSettlementReceipt(t, physical, req, definition, before, withTool)
		freshStore := reopen()
		freshRT := ea1StartRuntime(t, freshStore)
		ea1AssertFinishedOriginal(t, freshRT, wrapped.original, before)
		fresh := makeRunner(freshRT, "fresh-recovery-owner")
		if e := fresh.ReconcileRootPhysicalExecution(t.Context(), req.ID); e != nil {
			t.Fatalf("final receipt recovery: %v", e)
		}
		ea1AssertFinishedOriginal(t, freshRT, wrapped.original, before)
		if cancelBefore || operation == "resume" {
			cancelSettled(freshRT)
		}
		return
	}
	grant, err := rt.LookupChildExecution(t.Context(), req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if grant.ExecutionOwnerID != owner || grant.ExecutionLeaseID == "" || grant.ExecutionEpoch == 0 || grant.AdmissionOperation != operation || len(grant.AdmissionSourceHash) != 64 || grant.ExpectedRevision != expectedRevision {
		t.Fatal("original owned source binding missing")
	}
	if wrapped.markCalls != 0 || wrapped.finishCommits != 0 || (!unknownHost && wrapped.finishCalls != 1) {
		t.Fatal("wrong precommit Finish boundary")
	}
	completion := ea1AssertOriginalRootCompletion(t, rt, grant, before, !unknownHost, true)
	ea1AssertStartSettlementReceipt(t, physical, req, definition, before, withTool)
	wantCancel := kernel.ErrChildExecutionBusy
	if before.Run.Status == kernel.RunStatusCompleted {
		wantCancel = kernel.ErrTerminal
	}
	if cancelBefore {
		ea1CancelRejected(t, rt, before, grant, wantCancel)
	}
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
	if cancelBefore {
		ea1CancelRejected(t, freshRT, before, grant, wantCancel)
	}
	// Exact identity remains required even if the caller knows a real marker.
	for _, mutate := range []func(*kernel.ChildExecutionGrant){func(g *kernel.ChildExecutionGrant) { g.ID += "-foreign" }, func(g *kernel.ChildExecutionGrant) { g.ExecutionEpoch++ }, func(g *kernel.ChildExecutionGrant) { g.AdmissionOperation = otherOperation }, func(g *kernel.ChildExecutionGrant) { g.AdmissionSourceHash = strings.Repeat("0", 64) }} {
		wrong := grant
		mutate(&wrong)
		if e = freshStore.(kernel.ChildExecutionStore).FinishChildExecution(t.Context(), wrong, kernel.ChildExecutionSettlement{}); !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatalf("foreign authority retired original: %v", e)
		}
		same, e := freshRT.LookupChildExecution(t.Context(), req.ID)
		if e != nil || same != grant {
			t.Fatal("denied identity changed grant")
		}
		assertUnchanged()
	}
	if phase == "P3" {
		if e = freshStore.(kernel.ChildExecutionStore).FinishChildExecution(t.Context(), grant, kernel.ChildExecutionSettlement{}); !errors.Is(e, kernel.ErrExecutionConflict) {
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
	if e = fresh.ReconcileRootPhysicalExecution(t.Context(), req.ID); e != nil {
		t.Fatalf("duplicate reconciliation invented work: %v", e)
	}
	assertUnchanged()
	if cancelBefore {
		cancelSettled(freshRT)
	}
}
func ea1AssertStartSettlementReceipt(t *testing.T, p *ea1StartSettlementPhysical, req agent.StartRequest, definition tools.Definition, s kernel.Snapshot, withTool bool) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) != 1 || len(p.responses) != 1 || len(p.intents) != 1 || p.hostCalls != 1 || (p.hostUnknown && p.hostSends != 1) {
		t.Fatal("physical callback replayed")
	}
	inv := p.intents[0]
	actual := p.requests[0]
	canonical := model.CloneRequest(actual)
	canonical.InvocationID = ""
	digest, e := ea1ModelIntentEvidenceHash(canonical, inv.CatalogFingerprints, inv.Provider, inv.BindingFingerprint)
	if e != nil || inv.RequestHash != digest || !reflect.DeepEqual(inv.Request, actual) || actual.RunID != req.ID || actual.Model != req.Model || !reflect.DeepEqual(actual.ModelOptions, req.ModelOptions) || inv.ID != actual.InvocationID || inv.Provider != p.ProviderName() || inv.BindingFingerprint != strings.Repeat("b", 64) || inv.SourceRevision == 0 || inv.ExecutionAttempt != 1 || inv.DispatchAttempt != 1 {
		t.Fatal("full original Model request/hash/provider/attempt mismatch")
	}
	var frozen struct {
		OriginFingerprint string `json:"originFingerprint"`
	}
	if req.ParentRunID != "" && (json.Unmarshal(s.State, &frozen) != nil || frozen.OriginFingerprint != req.OriginFingerprint || frozen.OriginFingerprint == "") {
		t.Fatal("original frozen child origin lost")
	}
	view, e := agent.ViewState(s)
	if e != nil || len(view.ModelInvocations) != 1 {
		t.Fatalf("persisted receipt missing: %v", e)
	}
	settled := view.ModelInvocations[0]
	if settled.ID != inv.ID || settled.SourceRevision != inv.SourceRevision || settled.RequestHash != inv.RequestHash || settled.Provider != inv.Provider || settled.BindingFingerprint != inv.BindingFingerprint || settled.ExecutionAttempt != 1 || settled.DispatchAttempt != 1 || settled.Status != agent.ModelInvocationConsumed || settled.ProviderResponseID != "original-response" || settled.CompletedAt == nil || settled.ConsumedAt == nil || settled.Usage == nil || settled.Usage.InputTokens != 7 || settled.Usage.OutputTokens != 3 {
		t.Fatal("consumed receipt not original physical result")
	}
	// Compare the whole provider response, including optional fields and raw JSON,
	// through its persisted JSON representation rather than only receipt metadata.
	ea1StartSettlementJSONEqual(t, "durable Model response", settled.Response, p.responses[0])
	if s.Run.ID != req.ID || s.Run.ParentRunID != req.ParentRunID || s.Run.Actor != req.Actor || s.Run.Thread != req.Thread || s.Run.RequestID != req.RequestID || s.Run.Goal != req.Goal || !reflect.DeepEqual(view.Budget.Limits, req.Limits) {
		t.Fatal("frozen Agent source/budget changed")
	}
	if withTool {
		if s.Run.Status != kernel.RunStatusCompleted || len(p.toolInvocations) != 1 || len(p.toolRequests) != 1 || len(p.receipts) != 1 {
			t.Fatal("terminal Tool receipt/count missing")
		}
		ti := p.toolInvocations[0]
		tr := p.toolRequests[0]
		receipt := p.receipts[0]
		if s.Result == nil || s.Result.ContentType != "application/json" {
			t.Fatal("terminal durable result missing or wrong content type")
		}
		ea1StartSettlementJSONEqual(t, "terminal durable Tool result", s.Result.Content, receipt.Content)
		want := tools.ExecutionRequest{RunID: req.ID, Call: tools.Call{ID: "original-call", ToolKey: "effect", Arguments: json.RawMessage(`{"source":"original"}`)}}
		// Agent binds the public Call.Name from the original catalog definition.
		want.Call.Name = definition.Name
		if !reflect.DeepEqual(tr, want) || !reflect.DeepEqual(ti.Request, tr) || !reflect.DeepEqual(ti.Definition, definition) || ti.Run.Actor != req.Actor || ti.Run.Thread != req.Thread || ti.Run.ParentRunID != req.ParentRunID || receipt.Receipt.ExecutionID != tr.Call.ID || receipt.Receipt.Disposition != "committed" || string(receipt.Content) != `{"committed":"original"}` || view.Budget.Usage.ToolCalls != 1 {
			t.Fatal("full Tool definition/request/receipt source mismatch")
		}
	} else if len(p.toolRequests) != 0 || s.Run.Status != kernel.RunStatusRunning || view.Budget.Usage.ToolCalls != 0 {
		t.Fatal("model-only yield dispatched Tool or became terminal")
	}
}

// JSON normalization preserves every serialized field while ignoring harmless
// object-key order and whitespace changes introduced by durable storage.
func ea1StartSettlementJSONEqual(t *testing.T, label string, actual, expected any) {
	t.Helper()
	normalize := func(value any) any {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("%s marshal: %v", label, err)
		}
		decoder := json.NewDecoder(strings.NewReader(string(encoded)))
		decoder.UseNumber()
		var normalized any
		if err := decoder.Decode(&normalized); err != nil {
			t.Fatalf("%s normalize: %v", label, err)
		}
		return normalized
	}
	if !reflect.DeepEqual(normalize(actual), normalize(expected)) {
		t.Fatalf("%s differs from actual physical return: got %#v want %#v", label, actual, expected)
	}
}

// The new retirement protocol records the actual whole-callback fact before
// either owner Mark or its recovered retry. Compare the entire immutable fact.
func ea1AssertOriginalRootCompletion(t *testing.T, rt *kernel.Runtime, grant kernel.ChildExecutionGrant, source kernel.Snapshot, hostCompleted, leaseOwned bool) kernel.OwnedPhysicalCallbackCompletion {
	t.Helper()
	got, e := rt.LookupOwnedPhysicalCallbackReturn(t.Context(), grant)
	if e != nil {
		t.Fatal(e)
	}
	outcome := kernel.OwnedPhysicalCallbackOutcome{HostCompleted: hostCompleted, LeaseOwned: leaseOwned, ControlCustody: got.ControlCustody}
	hash := sha256.Sum256(kernel.OwnedPhysicalCallbackReturnPayload(source, outcome))
	want := kernel.OwnedPhysicalCallbackCompletion{GrantID: grant.ID, ReturnRevision: source.Run.Revision, ReturnSourceHash: hex.EncodeToString(hash[:]), HostCompleted: hostCompleted, LeaseOwned: leaseOwned, ControlCustody: got.ControlCustody}
	if !got.ControlCustody.Valid() || got != want {
		t.Fatalf("original whole callback binding: got=%+v want=%+v err=%v", got, want, e)
	}
	return got
}
