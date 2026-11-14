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

	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// RunEA1RootAgentControlSettlementSuite covers AR-A P3/P4/P5. Root has no
// ancestor. These APIs expose neither Workflow compensation nor shared-budget
// election; the entire original Budget is nevertheless protected on rejection.
func RunEA1RootAgentControlSettlementSuite(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store)) {
	for _, entry := range []string{"approval", "toolwait"} {
		for _, phase := range []string{"P3", "P4", "P5", "P5-run-deadline"} {
			t.Run(entry+"/"+phase, func(t *testing.T) { ea1ControlSettlement(t, factory, entry, phase) })
		}
	}
	t.Run("toolwait/P5-pending-deadline", func(t *testing.T) { ea1ControlSettlement(t, factory, "toolwait", "P5-pending-deadline") })
}

type ea1ControlApprovalPolicy struct{}

func (ea1ControlApprovalPolicy) Name() string { return "ea1.control.approval" }
func (ea1ControlApprovalPolicy) Approval(context.Context, plugin.ToolInvocation) (plugin.ApprovalRequirement, error) {
	return plugin.ApprovalRequired, nil
}

type ea1ControlMiddleware struct {
	hook  func()
	calls int
}

func (*ea1ControlMiddleware) Name() string { return "ea1.control.callback" }
func (m *ea1ControlMiddleware) Run(ctx context.Context, inv plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	if inv.Operation != plugin.RunResolveApproval || inv.ParentRunID != "" {
		return kernel.Snapshot{}, errors.New("not actual Root approval")
	}
	m.calls++
	if m.hook != nil {
		m.hook()
	}
	return next(ctx)
}

func ea1ControlSettlement(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), entry, phase string) {
	t.Helper()
	ctx := t.Context()
	backing, peerStore, reopen := factory(t)
	fault := errors.New("original control settlement transport fault")
	wrapped := &ea1StartSettlementStore{Store: backing, ExecutionStore: backing.(kernel.ExecutionStore), ControlStore: backing.(kernel.ControlStore), ChildExecutionStore: backing.(kernel.ChildExecutionStore), OwnedPhysicalExecutionStore: backing.(kernel.OwnedPhysicalExecutionStore), mode: "setup", fault: fault}
	rt, err := kernel.New(kernel.Dependencies{Store: wrapped})
	if err != nil {
		t.Fatal(err)
	}
	peerRT, err := kernel.New(kernel.Dependencies{Store: peerStore})
	if err != nil {
		t.Fatal(err)
	}
	physical := &ea1StartSettlementPhysical{rt: rt, tool: true}
	definition := tools.Definition{Key: "effect", Name: "Original Effect", Description: "original control effect", InputSchema: json.RawMessage(`{"type":"object","properties":{"source":{"type":"string"}},"required":["source"]}`), Terminal: true}
	var pendingRequest tools.ExecutionRequest
	var pendingResult tools.ExecutionResult
	var verifiedRequest tools.ExecutionRequest
	verifiedResult := tools.ExecutionResult{Content: json.RawMessage(`{"committed":"original-control"}`), Receipt: tools.Receipt{ExecutionID: "original-call", Disposition: "committed"}}
	toolCalls, verifyCalls := 0, 0
	var verifyHook func()
	pendingDeadline := time.Now().UTC().Add(3 * time.Minute)
	if phase == "P5-pending-deadline" {
		pendingDeadline = time.Now().UTC().Add(500 * time.Millisecond)
	}
	catalog, err := tools.NewRegistry([]tools.Registration{{Definition: definition, Handler: tools.HandlerFunc(func(_ context.Context, req tools.ExecutionRequest) (tools.ExecutionResult, error) {
		toolCalls++
		pendingRequest = req
		pendingResult = tools.ExecutionResult{Content: json.RawMessage(`null`), Receipt: tools.Receipt{ExecutionID: req.Call.ID, Disposition: tools.ReceiptDispositionPending}, Pending: &tools.PendingExecution{Kind: tools.PendingExternalResult, OperationID: "original-operation", InputDigest: strings.Repeat("a", 64), DeadlineAt: &pendingDeadline}}
		return tools.CloneExecutionResult(pendingResult), nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	middleware := &ea1ControlMiddleware{}
	var resolvedCheckpoint *kernel.Checkpoint
	makeRunner := func(runtime *kernel.Runtime, owner string, control bool) *agent.Runner {
		approvals, e := interaction.New(runtime)
		if e != nil {
			t.Fatal(e)
		}
		deps := agent.Dependencies{Runtime: runtime, Model: physical, Catalog: catalog, Executor: catalog, RunExecution: &agent.RunExecution{OwnerID: owner, LeaseDuration: 3 * time.Minute}, ToolOutcomeVerifier: agent.ToolOutcomeVerifierFunc(func(_ context.Context, req tools.ExecutionRequest) (tools.ExecutionResult, error) {
			verifyCalls++
			verifiedRequest = req
			if !reflect.DeepEqual(req, pendingRequest) {
				return tools.ExecutionResult{}, errors.New("verifier request changed original call")
			}
			if verifyHook != nil {
				verifyHook()
			}
			return tools.CloneExecutionResult(verifiedResult), nil
		})}
		if entry == "approval" {
			deps.Approvals = ea1ControlApprovalCapture{ApprovalHandler: interactionadapter.New(approvals), returned: func(cp *kernel.Checkpoint) {
				raw, e := json.Marshal(cp)
				if e != nil {
					t.Fatal(e)
				}
				if e = json.Unmarshal(raw, &resolvedCheckpoint); e != nil {
					t.Fatal(e)
				}
			}}
			deps.ApprovalPolicies = []plugin.ApprovalPolicy{ea1ControlApprovalPolicy{}}
		}
		if control {
			deps.RunMiddleware = []plugin.RunMiddleware{middleware}
		}
		runner, e := agent.NewRunner(deps)
		if e != nil {
			t.Fatal(e)
		}
		return runner
	}
	setup := makeRunner(rt, "original-start-owner", false)
	req := agent.StartRequest{ID: "root-control-settlement", RequestID: "original-request", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "original-thread"}, Goal: "original control", Instructions: "Keep original source", Model: "fixture-model", ModelOptions: json.RawMessage(`{"temperature":0.1}`), ToolKeys: []string{"effect"}, Limits: agent.Limits{MaxLLMCalls: 2, MaxToolCalls: 1}}
	if phase == "P5-run-deadline" {
		deadline := time.Now().UTC().Add(time.Second)
		req.DeadlineAt = &deadline
	}
	var created kernel.Snapshot
	wrapped.created = func(s kernel.Snapshot) {
		created = s
		if !setup.VerifyFrozenStartRequest(s, req) || s.Run.ParentRunID != "" || req.OriginFingerprint != "" {
			t.Fatal("original Root Create source changed")
		}
	}
	initial, err := setup.StartRun(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if created.Run.ID != req.ID {
		t.Fatal("missing actual Create")
	}
	initialView, err := agent.ViewState(initial)
	if err != nil {
		t.Fatal(err)
	}
	if len(initialView.ModelInvocations) != 1 || len(physical.responses) != 1 || len(physical.requests) != 1 {
		t.Fatal("original Model intent/return missing")
	}
	intent := initialView.ModelInvocations[0]
	if intent.Status != agent.ModelInvocationConsumed || intent.ExecutionAttempt != 1 || intent.DispatchAttempt != 1 || intent.RequestHash != physical.intents[0].RequestHash || intent.Provider != physical.ProviderName() || intent.BindingFingerprint != strings.Repeat("b", 64) {
		t.Fatal("original Model binding/attempt lost")
	}
	// The consumed invocation legitimately prunes Request. Verify the full
	// original tuple captured at Generate, then bind durable identity to it.
	originalIntent := physical.intents[0]
	ea1StartSettlementJSONEqual(t, "original Model request", originalIntent.Request, physical.requests[0])
	canonical := model.CloneRequest(physical.requests[0])
	canonical.InvocationID = ""
	encoded, e := json.Marshal(struct {
		Request             model.Request `json:"request"`
		CatalogFingerprints []string      `json:"catalogFingerprints"`
		Provider            string        `json:"provider"`
		BindingFingerprint  string        `json:"bindingFingerprint"`
	}{canonical, originalIntent.CatalogFingerprints, originalIntent.Provider, originalIntent.BindingFingerprint})
	digest := sha256.Sum256(encoded)
	if e != nil || originalIntent.RequestHash != hex.EncodeToString(digest[:]) || intent.ID != originalIntent.ID || intent.RunID != originalIntent.RunID || intent.SourceRevision != originalIntent.SourceRevision || intent.Model != originalIntent.Model || intent.Provider != originalIntent.Provider || intent.BindingFingerprint != originalIntent.BindingFingerprint || intent.ExecutionAttempt != originalIntent.ExecutionAttempt || intent.DispatchAttempt != originalIntent.DispatchAttempt {
		t.Fatal("durable invocation lost original full source tuple")
	}
	// CatalogFingerprints is pruned with Request; the original tuple is sealed by the retained hash above.
	ea1StartSettlementJSONEqual(t, "original Model response", intent.Response, physical.responses[0])
	var originalGrant kernel.ChildExecutionGrant
	var wait *agent.PendingTool
	signal := agent.ExternalToolResultSignal{RunID: req.ID, CallID: "original-call", OperationID: "original-operation", InputDigest: strings.Repeat("a", 64), SignalID: "original-signal"}
	runner := makeRunner(rt, "original-control-owner", entry == "approval")
	if entry == "approval" {
		if initial.Run.Status != kernel.RunStatusWaitingInput || initial.Checkpoint == nil || toolCalls != 0 {
			t.Fatal("not actual unresolved approval")
		}
		if _, e := rt.LookupChildExecution(ctx, req.ID); !errors.Is(e, kernel.ErrNotFound) {
			t.Fatalf("setup approval scope not retired: %v", e)
		}
	} else {
		if toolCalls != 1 || initial.Run.Status != kernel.RunStatusRunning {
			t.Fatal("not actual pending Tool")
		}
		originalGrant, err = rt.LookupChildExecution(ctx, req.ID)
		if err != nil {
			t.Fatal(err)
		}
		if originalGrant.AdmissionOperation != "start" || originalGrant.ExpectedRevision != created.Run.Revision {
			t.Fatal("pending Tool original start authority lost")
		}
		wait, err = runner.QueryPendingTool(ctx, req.ID, "original-call")
		if err != nil {
			t.Fatal(err)
		}
		// PersistToolWait clones the actual descriptor and clamps only its
		// deadline to the earlier frozen Run deadline; never mutate the return.
		durablePending := tools.ClonePending(pendingResult.Pending)
		if initial.Run.DeadlineAt != nil && (durablePending.DeadlineAt == nil || initial.Run.DeadlineAt.Before(*durablePending.DeadlineAt)) {
			clamped := initial.Run.DeadlineAt.UTC()
			durablePending.DeadlineAt = &clamped
		}
		if wait.Generation == 0 || wait.CallID != pendingRequest.Call.ID || wait.ToolKey != definition.Key || !reflect.DeepEqual(wait.PendingExecution, *durablePending) {
			t.Fatal("pending Tool full correlation changed")
		}
		if phase == "P5-pending-deadline" {
			ea1ControlWaitUntil(*wait.DeadlineAt)
			_, e := runner.ResumeToolWait(ctx, req.ID, initial.Run.Revision, wait.Generation)
			if !errors.Is(e, agent.ErrToolOutcomeUnknown) || !errors.Is(e, agent.ErrToolOutcomeOverdue) {
				t.Fatalf("pending deadline discarded unknown: %v", e)
			}
			ea1ControlUnchanged(t, rt, initial, originalGrant)
			if verifyCalls != 0 || toolCalls != 1 {
				t.Fatal("deadline dispatched physical work")
			}
			return
		}
		if phase == "P5" {
			// Two real ready deliveries race the same original receipt CAS.
			var deliveries sync.WaitGroup
			outcomes := make(chan error, 2)
			deliveryPeer := makeRunner(peerRT, "signal-delivery-peer", false)
			for index := range 2 {
				delivery := runner
				if index == 1 {
					delivery = deliveryPeer
				}
				deliveries.Add(1)
				go func() {
					defer deliveries.Done()
					_, e := delivery.NotifyExternalToolResultReady(ctx, signal)
					outcomes <- e
				}()
			}
			deliveries.Wait()
			close(outcomes)
			for e := range outcomes {
				if e != nil {
					t.Fatalf("same-signal CAS not idempotent: %v", e)
				}
			}
			initial, err = rt.Load(ctx, req.ID)
		} else {
			initial, err = runner.NotifyExternalToolResultReady(ctx, signal)
		}
		if err != nil {
			t.Fatal(err)
		}
		wait, err = runner.QueryPendingTool(ctx, req.ID, "original-call")
		if err != nil {
			t.Fatal(err)
		}
		if wait.SignalID != signal.SignalID {
			t.Fatal("trusted ready signal missing")
		}
	}
	// Arm only the actual control operation, never setup Start.
	wrapped.markCalls, wrapped.markCommits, wrapped.finishCalls = 0, 0, 0
	if phase == "P3" || phase == "P4" {
		wrapped.mode = phase
	}
	var contested kernel.ChildExecutionGrant
	compete := func() {
		before, e := rt.Load(ctx, req.ID)
		if e != nil {
			t.Fatal(e)
		}
		contested, e = rt.LookupChildExecution(ctx, req.ID)
		if e != nil {
			t.Fatal(e)
		}
		_, e = peerRT.Cancel(ctx, req.ID, before.Run.Revision, "competing control")
		if !errors.Is(e, kernel.ErrChildExecutionBusy) {
			t.Fatalf("Cancel passed physical owner: %v", e)
		}
		ea1ControlUnchanged(t, rt, before, contested)
		_, e = peerRT.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: req.ID, OwnerID: "competitor", LeaseDuration: time.Minute})
		if !errors.Is(e, kernel.ErrChildExecutionBusy) {
			t.Fatalf("Acquire passed physical owner: %v", e)
		}
		ea1ControlUnchanged(t, rt, before, contested)
		peer := makeRunner(peerRT, "competing-control-owner", false)
		if entry == "approval" {
			_, e = peer.ResolveApproval(ctx, req.ID, before.Run.Revision, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
			// Public Root entry rejects its retained scope before Acquire.
			if !errors.Is(e, agent.ErrRootOutcomeUnknown) {
				t.Fatalf("duplicate approval bypassed retained Root outcome: %v", e)
			}
		} else {
			same, e := peer.NotifyExternalToolResultReady(ctx, signal)
			if e != nil || !reflect.DeepEqual(same, before) {
				t.Fatalf("duplicate signal not idempotent: %v", e)
			}
			wrong := signal
			wrong.InputDigest = strings.Repeat("b", 64)
			if _, e = peer.NotifyExternalToolResultReady(ctx, wrong); !errors.Is(e, agent.ErrToolSignalConflict) {
				t.Fatalf("foreign signal accepted: %v", e)
			}
		}
		ea1ControlUnchanged(t, rt, before, contested)
		if phase == "P5-run-deadline" {
			ea1ControlWaitUntil(*before.Run.DeadlineAt)
			limit := time.Now().Add(time.Second)
			for {
				_, applied, expireErr := peerRT.Expire(ctx, req.ID, before.Run.Revision)
				ea1ControlUnchanged(t, rt, before, contested)
				if errors.Is(expireErr, kernel.ErrChildExecutionBusy) && !applied {
					break
				}
				if !errors.Is(expireErr, kernel.ErrDeadline) || applied || time.Now().After(limit) {
					t.Fatalf("real deadline did not compete at Store fence: applied=%v err=%v", applied, expireErr)
				}
				time.Sleep(time.Millisecond)
			}
		}
	}
	if strings.HasPrefix(phase, "P5") {
		if entry == "approval" {
			middleware.hook = compete
		} else {
			verifyHook = compete
		}
	}
	var out kernel.Snapshot
	var operationErr error
	if entry == "approval" {
		out, operationErr = runner.ResolveApproval(ctx, req.ID, initial.Run.Revision, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
	} else {
		out, operationErr = runner.ResumeToolWait(ctx, req.ID, initial.Run.Revision, wait.Generation)
	}
	middleware.hook = nil
	verifyHook = nil
	if phase == "P5-run-deadline" {
		if contested.ID == "" {
			t.Fatal("deadline never reached actual callback")
		}
		if len(physical.requests) != 1 {
			t.Fatal("deadline replayed Model")
		}
		latest, loadErr := rt.Load(ctx, req.ID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if entry == "approval" {
			// Approval is a control transition: ValidateControl rejects the
			// due Run before committing its resolved checkpoint.
			if !errors.Is(operationErr, kernel.ErrDeadline) || toolCalls != 0 || !reflect.DeepEqual(latest, initial) {
				t.Fatalf("due approval mutated gate or dispatched: status=%s err=%v", out.Run.Status, operationErr)
			}
		} else {
			// The original COMMIT is already known. Recovery and consumption
			// of its receipt do not call invokePendingTool's dispatch policy;
			// a local terminal receipt can still finish after the Run deadline.
			if operationErr != nil || toolCalls != 1 || verifyCalls != 1 {
				t.Fatalf("known receipt failed after deadline: %v", operationErr)
			}
			ea1ControlTerminalResult(t, latest, verifiedResult)
			if _, e := rt.LookupChildExecution(ctx, req.ID); !errors.Is(e, kernel.ErrNotFound) {
				t.Fatalf("known receipt scope retained: %v", e)
			}
		}
		return
	}

	if phase == "P5" {
		if operationErr != nil {
			t.Fatal(operationErr)
		}
	} else if !errors.Is(operationErr, fault) {
		t.Fatalf("actual settlement cutpoint not reached: %v", operationErr)
	}
	before, err := rt.Load(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	finalView, viewErr := agent.ViewState(before)
	if viewErr != nil || len(finalView.ModelInvocations) != 1 {
		t.Fatalf("control lost original Model receipt: %v", viewErr)
	}
	ea1StartSettlementJSONEqual(t, "unchanged full Model invocation", finalView.ModelInvocations[0], intent)
	if before.Run.Actor != req.Actor || before.Run.Thread != req.Thread || before.Run.ParentRunID != "" || before.Run.RequestID != req.RequestID {
		t.Fatal("control changed original Root source")
	}
	if entry == "approval" {
		ea1StartSettlementJSONEqual(t, "approval Budget unchanged", finalView.Budget, initialView.Budget)
		if resolvedCheckpoint == nil || resolvedCheckpoint.Status != kernel.CheckpointResolved || resolvedCheckpoint.ResolvedAt == nil || resolvedCheckpoint.ID != initial.Checkpoint.ID || resolvedCheckpoint.Kind != initial.Checkpoint.Kind || !resolvedCheckpoint.CreatedAt.Equal(initial.Checkpoint.CreatedAt) {
			t.Fatal("actual handler did not resolve original checkpoint")
		}
		ea1StartSettlementJSONEqual(t, "original approval payload", resolvedCheckpoint.Payload, initial.Checkpoint.Payload)
		ea1StartSettlementJSONEqual(t, "actual approve response", resolvedCheckpoint.Response, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
		ea1StartSettlementJSONEqual(t, "full durable actual approval checkpoint", before.Checkpoint, resolvedCheckpoint)

		if before.Checkpoint == nil || initial.Checkpoint == nil || before.Checkpoint.ID != initial.Checkpoint.ID {
			t.Fatal("approval checkpoint source lost")
		}
	}
	if entry == "approval" {
		if middleware.calls != 1 || before.Run.Status != kernel.RunStatusRunning || toolCalls != 0 || before.Checkpoint == nil || before.Checkpoint.ID != initial.Checkpoint.ID || before.Checkpoint.Status == kernel.CheckpointPending {
			t.Fatal("explicit native approval must only resolve checkpoint, not dispatch Tool")
		}
	} else if verifyCalls != 1 || !reflect.DeepEqual(verifiedRequest, pendingRequest) || toolCalls != 1 {
		t.Fatal("original Tool verifier/dispatch mismatch")
	}
	if phase == "P5" {
		if entry == "toolwait" {
			ea1ControlTerminalResult(t, before, verifiedResult)
		}
		return
	}
	grant, err := rt.LookupChildExecution(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if grant.ExecutionLeaseID == "" || grant.ExecutionEpoch == 0 || len(grant.AdmissionSourceHash) != 64 {
		t.Fatal("control lost original native/source authority")
	}
	if phase == "P3" {
		completion, e := rt.LookupOwnedPhysicalCallbackReturn(ctx, grant)
		if e != nil || !completion.HostCompleted || !completion.LeaseOwned {
			t.Fatalf("real original callback return missing: %v", e)
		}
	}
	if entry == "approval" {
		ea1AssertOriginalRootCompletion(t, rt, grant, before, true, true)
	}
	if entry == "toolwait" && grant != originalGrant {
		t.Fatal("ToolWait invented new authority")
	}
	if entry == "approval" && (grant.AdmissionOperation != "approval" || grant.ExpectedRevision != initial.Run.Revision || grant.ExecutionOwnerID != "original-control-owner") {
		t.Fatal("approval source authority changed")
	}
	if phase == "P3" && wrapped.markCommits != 0 {
		t.Fatal("P3 unexpectedly marked")
	}
	if phase == "P4" && ((entry == "approval" && (wrapped.markCalls != 2 || wrapped.markCommits != 2 || wrapped.finishCalls != 1)) || (entry == "toolwait" && wrapped.markCommits != 1)) {
		t.Fatal("P4 actual Mark ACK loss not reached")
	}
	if wrapped.markCalls == 0 {
		t.Fatal("actual Mark never reached")
	}
	if entry == "toolwait" {
		var state struct {
			RecoveredTools []struct {
				Call   tools.Call            `json:"call"`
				Wait   *agent.PendingTool    `json:"wait"`
				Result tools.ExecutionResult `json:"result"`
			} `json:"recoveredTools"`
		}
		if err = json.Unmarshal(before.State, &state); err != nil || len(state.RecoveredTools) != 1 {
			t.Fatalf("durable original receipt absent: %v", err)
		}
		recovered := state.RecoveredTools[0]
		ea1StartSettlementJSONEqual(t, "full durable Tool receipt", recovered.Result, verifiedResult)
		ea1StartSettlementJSONEqual(t, "full recovered original Call", recovered.Call, pendingRequest.Call)
		ea1StartSettlementJSONEqual(t, "full recovered original wait", recovered.Wait, wait)
	}
	freshStore := reopen()
	freshRT, err := kernel.New(kernel.Dependencies{Store: freshStore})
	if err != nil {
		t.Fatal(err)
	}
	fresh := makeRunner(freshRT, "fresh-control-recovery", false)
	ea1ControlUnchanged(t, freshRT, before, grant)
	wrong := grant
	wrong.ExecutionEpoch++
	if err = freshStore.(kernel.ChildExecutionStore).FinishChildExecution(ctx, wrong); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("foreign epoch retired scope: %v", err)
	}
	ea1ControlUnchanged(t, freshRT, before, grant)
	if err = fresh.ReconcileRootPhysicalExecution(ctx, req.ID); err != nil {
		t.Fatal(err)
	}
	after, err := freshRT.Load(ctx, req.ID)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatal("retirement changed committed control/receipt")
	}
	if err = fresh.ReconcileRootPhysicalExecution(ctx, req.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("duplicate retirement: %v", err)
	}
	if entry == "toolwait" {
		completed, e := fresh.Resume(ctx, req.ID, after.Run.Revision)
		if e != nil {
			t.Fatal(e)
		}
		ea1ControlTerminalResult(t, completed, verifiedResult)
		if verifyCalls != 1 || toolCalls != 1 || len(physical.requests) != 1 {
			t.Fatal("fresh consumption replayed provider/verifier/Tool")
		}
	} else if toolCalls != 0 || len(physical.requests) != 1 {
		t.Fatal("approval recovery dispatched new physical work")
	}
}

func ea1ControlWaitUntil(deadline time.Time) {
	if delay := time.Until(deadline); delay > 0 {
		time.Sleep(delay)
	}
}
func ea1ControlUnchanged(t *testing.T, rt *kernel.Runtime, before kernel.Snapshot, grant kernel.ChildExecutionGrant) {
	t.Helper()
	after, e := rt.Load(t.Context(), before.Run.ID)
	if e != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("denial changed full snapshot/Budget/Usage: %v", e)
	}
	retained, e := rt.LookupChildExecution(t.Context(), before.Run.ID)
	if e != nil || retained != grant {
		t.Fatalf("denial changed original grant/native epoch: %v", e)
	}
}
func ea1ControlTerminalResult(t *testing.T, s kernel.Snapshot, result tools.ExecutionResult) {
	t.Helper()
	if s.Run.Status != kernel.RunStatusCompleted || s.Result == nil || s.Result.ContentType != "application/json" {
		t.Fatal("verified original Tool did not produce terminal JSON result")
	}
	ea1StartSettlementJSONEqual(t, "terminal durable original Tool result", s.Result.Content, result.Content)
	view, err := agent.ViewState(s)
	if err != nil || view.Budget.Usage.ToolCalls != 1 || view.PendingTool != nil {
		t.Fatalf("receipt not charged exactly once/retired: %v", err)
	}
}

// Observe the actual first-party adapter return without inventing a checkpoint.
type ea1ControlApprovalCapture struct {
	plugin.ApprovalHandler
	returned func(*kernel.Checkpoint)
}

func (h ea1ControlApprovalCapture) ResolveToolApproval(cp *kernel.Checkpoint, response plugin.ApprovalResponse) (*kernel.Checkpoint, error) {
	result, err := h.ApprovalHandler.ResolveToolApproval(cp, response)
	if err == nil {
		h.returned(result)
	}
	return result, err
}
