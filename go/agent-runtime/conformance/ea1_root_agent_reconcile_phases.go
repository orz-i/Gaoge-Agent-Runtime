package conformance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// AR-Q P0..P4 use actual public recovery after an actual Root Start/send.
// N2 is limited to absence of NEW physical dispatch in receipt-only recovery;
// the original physical owner and its competing-control fence remain tested.
func RunEA1RootAgentReconcilePhases(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store)) {
	t.Run("P2/model-host-evidence", func(t *testing.T) { ea1RootReconcilePhase(t, factory, "P2-evidence", "model") })
	for _, phase := range []string{"P0", "P1", "P2", "P3", "P4"} {
		for _, kind := range []string{"model", "tool"} {
			t.Run(phase+"/"+kind, func(t *testing.T) { ea1RootReconcilePhase(t, factory, phase, kind) })
			if phase == "P4" {
				t.Run(phase+"/"+kind+"/single-mark-ack-loss-no-finish-hold", func(t *testing.T) { ea1RootReconcilePhase(t, factory, phase, kind, false) })
			}
		}
	}
}

type ea1RootRecoveryFault struct {
	*ea1AgentReceiptFault
	phase         string
	cuts          int
	recoveryGrant kernel.ChildExecutionGrant
	holdFinish    bool
	finishHolds   int
	heldSource    kernel.Snapshot
}

func (s *ea1RootRecoveryFault) ApplyOwnedPhysicalRecovery(ctx context.Context, g kernel.ChildExecutionGrant, rev uint64, m kernel.StoreMutation, p kernel.OwnedPhysicalRecovery) (kernel.Snapshot, error) {
	out, err := s.OwnedPhysicalExecutionStore.ApplyOwnedPhysicalRecovery(ctx, g, rev, m, p)
	if err == nil && s.phase == "P3" && s.cuts == 0 {
		s.cuts++
		s.recoveryGrant = g
		return kernel.Snapshot{}, s.injected
	}
	return out, err
}
func (s *ea1RootRecoveryFault) MarkChildExecutionSettled(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	err := s.ChildExecutionStore.MarkChildExecutionSettled(ctx, g, p)
	if err == nil && s.phase == "P4" && s.cuts == 0 {
		s.cuts++
		s.recoveryGrant = g
		return s.injected
	}
	return err
}

// P4 hold adds a SECOND, explicit precommit fault after genuine Mark ACK
// loss. The paired no-hold case proves the single original fault is recovered.
func (s *ea1RootRecoveryFault) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	if s.phase == "P4" && s.holdFinish && s.cuts == 1 && g == s.recoveryGrant {
		source, e := s.Load(ctx, g.ChildRunID)
		if e != nil {
			return e
		}
		s.finishHolds++
		s.heldSource = source
		return s.injected
	}
	return s.ChildExecutionStore.FinishChildExecution(ctx, g)
}

type ea1RootRecoveryPhysical struct {
	*ea1UnknownAgentPhysical
	response model.Response
	binding  string
}

func (p *ea1RootRecoveryPhysical) ProviderName() string { return "ea1-reconcile-provider" }
func (p *ea1RootRecoveryPhysical) BindingFingerprintForRequest(model.Request) string {
	sum := sha256.Sum256([]byte("original-route:" + p.binding))
	return hex.EncodeToString(sum[:])
}
func (p *ea1RootRecoveryPhysical) Generate(ctx context.Context, r model.Request) (model.Response, error) {
	out, e := p.ea1UnknownAgentPhysical.Generate(ctx, r)
	out.ResponseID = "physical-response:" + r.InvocationID
	p.response = model.CloneResponse(out)
	return out, e
}
func ea1RootReconcilePhase(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), phase, kind string, holdOption ...bool) {
	backing, peer, reopen := factory(t)
	injected := errors.New("EA1 original receipt/settlement committed ACK fault")
	event := "agent.model_invocation.completed"
	if kind == "tool" {
		event = "tool.completed"
	}
	initial := &ea1AgentReceiptFault{Store: backing, ExecutionStore: backing.(kernel.ExecutionStore), ControlStore: backing.(kernel.ControlStore), ChildExecutionStore: backing.(kernel.ChildExecutionStore), ExecutionQuarantineStore: backing.(kernel.ExecutionQuarantineStore), OwnedPhysicalExecutionStore: backing.(kernel.OwnedPhysicalExecutionStore), event: event, injected: injected}
	fault := &ea1RootRecoveryFault{ea1AgentReceiptFault: initial}
	rt := ea1StartRuntime(t, fault)
	competitor := ea1StartRuntime(t, peer)
	physical := &ea1RootRecoveryPhysical{ea1UnknownAgentPhysical: &ea1UnknownAgentPhysical{tool: kind == "tool", rt: rt}, binding: "original"}
	registry, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "effect", Name: "Effect", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(physical.execute)}})
	if err != nil {
		t.Fatal(err)
	}
	lookups := 0
	var verify func()
	unavailable := false
	modelVerifier := agent.ModelOutcomeVerifierFunc(func(_ context.Context, r model.Request) (model.Response, error) {
		lookups++
		if len(physical.models) != 1 || !ea1RecoveryJSONEqual(model.CloneRequest(r), model.CloneRequest(physical.models[0])) {
			t.Fatal("verifier changed full original Model request")
		}
		if verify != nil {
			verify()
		}
		if unavailable {
			return model.Response{}, agent.ErrModelOutcomeUnknown
		}
		return model.CloneResponse(physical.response), nil
	})
	var verifiedToolResult tools.ExecutionResult
	toolVerifier := agent.ToolOutcomeVerifierFunc(func(_ context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
		lookups++
		if len(physical.tools) != 1 || !ea1RecoveryJSONEqual(r, physical.tools[0]) {
			t.Fatal("verifier changed original Tool source")
		}
		if verify != nil {
			verify()
		}
		if unavailable {
			return tools.ExecutionResult{}, agent.ErrToolOutcomeUnknown
		}
		verifiedToolResult = tools.ExecutionResult{Content: json.RawMessage(`{"committed":true}`), Receipt: tools.Receipt{ExecutionID: r.Call.ID, Disposition: "committed"}}
		return tools.CloneExecutionResult(verifiedToolResult), nil
	})
	runnerFor := func(runtime *kernel.Runtime, owner string) *agent.Runner {
		var host []plugin.RunMiddleware
		if phase == "P2-evidence" {
			host = []plugin.RunMiddleware{&ea1AdmissionMiddleware{}}
		}
		r, e := agent.NewRunner(agent.Dependencies{Runtime: runtime, RunMiddleware: host, Model: physical, Catalog: registry, Executor: registry, ModelOutcomeVerifier: modelVerifier, ToolOutcomeVerifier: toolVerifier, RunExecution: &agent.RunExecution{OwnerID: owner, LeaseDuration: 2 * time.Second}, Execution: agent.ExecutionPolicy{Model: agent.CallPolicy{Timeout: 100 * time.Millisecond, MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}, Tool: agent.CallPolicy{Timeout: 100 * time.Millisecond, MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}}})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	runner := runnerFor(rt, "original-owner")
	request := agent.StartRequest{ID: "root-reconcile", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "frozen original recovery"}
	if kind == "tool" {
		request.ToolKeys = []string{"effect"}
	}
	_, err = runner.StartRun(t.Context(), request)
	if !errors.Is(err, injected) || initial.failures != 1 {
		t.Fatalf("actual physical receipt fault missing: %v", err)
	}
	before, err := rt.Load(t.Context(), request.ID)
	if err != nil {
		t.Fatal(err)
	}
	var rootSource struct {
		OriginFingerprint string `json:"originFingerprint"`
	}
	if json.Unmarshal(before.State, &rootSource) != nil || before.Run.ParentRunID != "" || rootSource.OriginFingerprint != "" || !runner.VerifyFrozenStartRequest(before, request) {
		t.Fatal("Root original source mismatched")
	}
	view, err := agent.ViewState(before)
	if err != nil || len(view.ModelInvocations) != 1 {
		t.Fatal("original invocation missing")
	}
	inv := view.ModelInvocations[0]
	if inv.Provider != physical.ProviderName() || inv.BindingFingerprint != physical.BindingFingerprintForRequest(inv.Request) || inv.ExecutionAttempt != 1 || inv.DispatchAttempt != 1 {
		t.Fatal("original provider/attempt missing")
	}
	physical.assertOriginal(t, request, inv)
	grant, err := rt.LookupChildExecution(t.Context(), request.ID)
	if err != nil {
		t.Fatal(err)
	}
	completion, err := rt.LookupOwnedPhysicalCallbackReturn(t.Context(), grant)
	if err != nil || completion.HostCompleted != (phase != "P2-evidence") || completion.GrantID != grant.ID {
		t.Fatalf("positive recovery lacks actual whole-callback completion: %+v %v", completion, err)
	}
	lease := initial.lastLease()
	if grant.ExecutionOwnerID != lease.OwnerID || grant.ExecutionLeaseID != lease.LeaseID || grant.ExecutionEpoch != lease.Epoch {
		t.Fatal("original native binding lost")
	}
	models, toolCalls := physical.counts()
	unchanged := func(runtime *kernel.Runtime) {
		t.Helper()
		ea1CompBarrierUnchanged(t, runtime, before)
		g, e := runtime.LookupChildExecution(t.Context(), request.ID)
		if e != nil || g != grant {
			t.Fatal("rejected recovery changed original grant")
		}
		c, e := runtime.LookupOwnedPhysicalCallbackReturn(t.Context(), g)
		if e != nil || !ea1RecoveryJSONEqual(c, completion) {
			t.Fatal("original callback completion changed")
		}
		m, n := physical.counts()
		if m != models || n != toolCalls {
			t.Fatal("recovery physically replayed")
		}
	}
	reconcile := func(r *agent.Runner, revision uint64, call string) (kernel.Snapshot, error) {
		if kind == "model" {
			return r.ReconcileModelOutcome(t.Context(), request.ID, revision)
		}
		return r.ReconcileToolOutcome(t.Context(), request.ID, revision, call)
	}
	if phase == "P2-evidence" {
		evidence, e := reconcile(runner, before.Run.Revision, "original-tool-call")
		if !errors.Is(e, agent.ErrRootOutcomeUnknown) || evidence.Run.Revision != before.Run.Revision+1 {
			t.Fatalf("Host-unknown evidence purpose failed: %v", e)
		}
		v, e := agent.ViewState(evidence)
		if e != nil || len(v.ModelInvocations) != 1 || v.ModelInvocations[0].Status != agent.ModelInvocationCompleted || v.ModelInvocations[0].ID != inv.ID || v.ModelInvocations[0].RequestHash != inv.RequestHash || v.ModelInvocations[0].ExecutionAttempt != inv.ExecutionAttempt || v.ModelInvocations[0].DispatchAttempt != inv.DispatchAttempt || !ea1RecoveryJSONEqual(v.ModelInvocations[0].Response, physical.response) {
			t.Fatal("evidence lost exact original receipt")
		}
		var oldState, newState map[string]json.RawMessage
		if json.Unmarshal(before.State, &oldState) != nil || json.Unmarshal(evidence.State, &newState) != nil {
			t.Fatal("invalid evidence state")
		}
		delete(oldState, "modelInvocations")
		delete(newState, "modelInvocations")
		if !ea1RecoveryJSONEqual(oldState, newState) || !ea1RecoveryJSONEqual(evidence.Checkpoint, before.Checkpoint) || evidence.Run.Status != before.Run.Status || evidence.Run.ErrorCode != before.Run.ErrorCode || evidence.Run.ErrorDetail != before.Run.ErrorDetail {
			t.Fatal("evidence consumed or changed other state")
		}
		fresh := reopen()
		freshRT := ea1StartRuntime(t, fresh)
		physical.rt = freshRT
		next := runnerFor(freshRT, "fresh-owner")
		_, e = reconcile(next, evidence.Run.Revision, "original-tool-call")
		if !errors.Is(e, agent.ErrRootOutcomeUnknown) {
			t.Fatalf("Hostfalse evidence upgraded: %v", e)
		}
		ea1CompBarrierUnchanged(t, freshRT, evidence)
		if e = next.ReconcileRootPhysicalExecution(t.Context(), request.ID); !errors.Is(e, agent.ErrRootOutcomeUnknown) {
			t.Fatalf("Hostfalse scope retired: %v", e)
		}
		ea1CompBarrierUnchanged(t, freshRT, evidence)
		g, e := freshRT.LookupChildExecution(t.Context(), request.ID)
		if e != nil || g != grant {
			t.Fatal("evidence changed original scope")
		}
		c, e := freshRT.LookupOwnedPhysicalCallbackReturn(t.Context(), g)
		if e != nil || !ea1RecoveryJSONEqual(c, completion) {
			t.Fatal("Hostfalse upgraded")
		}
		m, n := physical.counts()
		if m != models || n != toolCalls {
			t.Fatal("evidence physically replayed")
		}
		return
	}
	if phase == "P0" {
		if e := runner.ReconcileRootPhysicalExecution(t.Context(), "missing-root"); !errors.Is(e, kernel.ErrNotFound) {
			t.Fatalf("missing original Root accepted: %v", e)
		}
		if kind == "model" {
			_, err = runner.ReconcileModelOutcome(t.Context(), "missing-root", before.Run.Revision)
		} else {
			_, err = runner.ReconcileToolOutcome(t.Context(), "missing-root", before.Run.Revision, "original-tool-call")
		}
		if !errors.Is(err, kernel.ErrNotFound) || lookups != 0 {
			t.Fatalf("missing Run reached lookup: %v", err)
		}
		unchanged(rt)
		_, err = reconcile(runner, before.Run.Revision+1, "original-tool-call")
		if !errors.Is(err, kernel.ErrConflict) || lookups != 0 {
			t.Fatalf("stale recovery reached verifier: %v", err)
		}
		unchanged(rt)
		if kind == "model" {
			physical.binding = "foreign"
			_, err = reconcile(runner, before.Run.Revision, "original-tool-call")
			physical.binding = "original"
			if !errors.Is(err, agent.ErrProviderBindingMismatch) {
				t.Fatalf("foreign provider accepted: %v", err)
			}
		} else {
			_, err = reconcile(runner, before.Run.Revision, "foreign-call")
			if !errors.Is(err, agent.ErrToolOutcomeUnknown) {
				t.Fatalf("foreign Tool accepted: %v", err)
			}
		}
		if lookups != 0 {
			t.Fatal("foreign recovery reached lookup")
		}
		unchanged(rt)
		if e := backing.(kernel.ExecutionStore).CheckExecution(t.Context(), request.ID, &lease); e != nil {
			t.Fatalf("preflight revoked original authority: %v", e)
		}
		return
	}
	if phase == "P1" {
		verify = func() {
			if e := peer.(kernel.ExecutionStore).CheckExecution(t.Context(), request.ID, &lease); e != nil {
				t.Fatalf("original owner not active during receipt-only lookup: %v", e)
			}
			_, e := competitor.Apply(t.Context(), request.ID, before.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: before.State, Checkpoint: before.Checkpoint})
			if !errors.Is(e, kernel.ErrExecutionRequired) {
				t.Fatalf("unbound Apply escaped native authority gate: %v", e)
			}
			unchanged(competitor)
			if e = peer.(kernel.ExecutionStore).CheckExecution(t.Context(), request.ID, &lease); e != nil {
				t.Fatalf("unbound Apply revoked original owner: %v", e)
			}
			// Original native identity is valid, but WithExecution alone cannot
			// mint the original physical scope's first-party write permit.
			nativeOnly, e := competitor.WithExecution(lease)
			if e != nil {
				t.Fatal(e)
			}
			_, e = nativeOnly.Apply(t.Context(), request.ID, before.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: before.State, Checkpoint: before.Checkpoint})
			if !errors.Is(e, kernel.ErrChildExecutionBusy) {
				t.Fatalf("native-only Apply escaped physical recovery fence: %v", e)
			}
			unchanged(competitor)
			if e = peer.(kernel.ExecutionStore).CheckExecution(t.Context(), request.ID, &lease); e != nil {
				t.Fatalf("native-only Apply revoked original owner: %v", e)
			}
			_, e = competitor.Cancel(t.Context(), request.ID, before.Run.Revision, "competing cancel")
			if !errors.Is(e, kernel.ErrChildExecutionBusy) {
				t.Fatalf("Cancel escaped: %v", e)
			}
			unchanged(competitor)
			_, e = competitor.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: request.ID, OwnerID: "foreign", LeaseDuration: 2 * time.Second})
			if !errors.Is(e, kernel.ErrChildExecutionBusy) {
				t.Fatalf("Acquire escaped: %v", e)
			}
			unchanged(competitor)
			e = competitor.ReleaseExecution(t.Context(), lease)
			if !errors.Is(e, kernel.ErrChildExecutionBusy) {
				t.Fatalf("Release escaped: %v", e)
			}
			unchanged(competitor)
			if e = peer.(kernel.ExecutionStore).CheckExecution(t.Context(), request.ID, &lease); e != nil {
				t.Fatalf("competing control revoked original owner: %v", e)
			}
		}
		unavailable = true
		_, err = reconcile(runner, before.Run.Revision, "original-tool-call")
		if lookups != 1 || (kind == "model" && !errors.Is(err, agent.ErrModelOutcomeUnknown)) || (kind == "tool" && !errors.Is(err, agent.ErrToolOutcomeUnknown)) {
			t.Fatalf("actual read-only recovery not exercised: %v", err)
		}
		unchanged(rt)
		return
	}
	if phase == "P2" {
		unavailable = true
		_, err = reconcile(runner, before.Run.Revision, "original-tool-call")
		if err == nil {
			t.Fatal("unknown accepted")
		}
		unchanged(rt)
		delay := time.Until(lease.ExpiresAt.Add(150 * time.Millisecond))
		if delay > 5*time.Second {
			t.Fatal("invalid native expiry fixture")
		}
		if delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-t.Context().Done():
				t.Fatal(t.Context().Err())
			}
		}
		fresh := reopen()
		freshRT := ea1StartRuntime(t, fresh)
		physical.rt = freshRT
		next := runnerFor(freshRT, "fresh-owner")
		if e := fresh.(kernel.ExecutionStore).CheckExecution(t.Context(), request.ID, &lease); !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatalf("Store lease did not expire: %v", e)
		}
		_, err = reconcile(next, before.Run.Revision, "original-tool-call")
		if kind == "model" && !errors.Is(err, agent.ErrModelOutcomeUnknown) || kind == "tool" && !errors.Is(err, agent.ErrToolOutcomeUnknown) {
			t.Fatalf("unknown contract lost: %v", err)
		}
		unchanged(freshRT)
		if e := next.ReconcileRootPhysicalExecution(t.Context(), request.ID); !errors.Is(e, agent.ErrRootOutcomeUnknown) {
			t.Fatalf("unknown Root retired: %v", e)
		}
		unchanged(freshRT)
		return
	}
	holdFinish := phase == "P4"
	if len(holdOption) > 0 {
		holdFinish = holdOption[0]
	}
	fault.holdFinish = holdFinish
	fault.phase = phase
	_, err = reconcile(runner, before.Run.Revision, "original-tool-call")
	noHoldSuccess := phase == "P4" && !holdFinish
	if fault.cuts != 1 || fault.recoveryGrant != grant || (noHoldSuccess && err != nil) || (!noHoldSuccess && !errors.Is(err, injected)) {
		t.Fatalf("actual %s commit/ACK cut: cuts=%d noHold=%v err=%v", phase, fault.cuts, noHoldSuccess, err)
	}
	expectedHolds := 0
	if phase == "P4" && holdFinish {
		expectedHolds = 1
	}
	if fault.finishHolds != expectedHolds {
		t.Fatalf("exact Finish precommit hold: got %d want %d", fault.finishHolds, expectedHolds)
	}
	committed, err := rt.Load(t.Context(), request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !runner.VerifyFrozenStartRequest(committed, request) || committed.Run.Revision != before.Run.Revision+1 {
		t.Fatal("receipt CAS changed source or wrong revision")
	}
	committedView, err := agent.ViewState(committed)
	if err != nil {
		t.Fatal(err)
	}
	if kind == "model" {
		got := committedView.ModelInvocations[0]
		if got.ID != inv.ID || got.ExecutionAttempt != inv.ExecutionAttempt || got.DispatchAttempt != inv.DispatchAttempt || got.Provider != inv.Provider || got.BindingFingerprint != inv.BindingFingerprint || got.RequestHash != inv.RequestHash || got.Status != agent.ModelInvocationCompleted || !ea1RecoveryJSONEqual(got.Response, physical.response) {
			t.Fatal("recovery receipt not original physical attempt")
		}
	} else {
		var state struct {
			ToolDispatchID string `json:"toolDispatchID"`
			RecoveredTools []struct {
				Call   tools.Call            `json:"call"`
				Result tools.ExecutionResult `json:"result"`
			} `json:"recoveredTools"`
		}
		if json.Unmarshal(committed.State, &state) != nil || state.ToolDispatchID != "" || len(state.RecoveredTools) != 1 || !ea1RecoveryJSONEqual(state.RecoveredTools[0].Call, physical.tools[0].Call) || state.RecoveredTools[0].Result.Receipt.ExecutionID != "original-tool-call" || state.RecoveredTools[0].Result.Receipt.Disposition != "committed" || !ea1RecoveryJSONEqual(tools.CloneExecutionResult(state.RecoveredTools[0].Result), tools.CloneExecutionResult(verifiedToolResult)) {
			t.Fatal("original Tool receipt not durable")
		}
	}
	var accepted kernel.OwnedRetirementReceipt
	if noHoldSuccess {
		accepted, err = rt.LoadOwnedRetirement(t.Context(), grant.ID)
		if err != nil || !accepted.Valid() || accepted.Phase != "ACKNOWLEDGED" || accepted.Evidence.Grant != grant || !ea1RecoveryJSONEqual(accepted.Evidence.Snapshot, committed) || accepted.Evidence.Completion != completion || accepted.ACKIdentity != accepted.Identity() {
			t.Fatalf("single Mark ACK loss did not preserve exact acknowledged retirement: %v", err)
		}
		if _, err = rt.LookupChildExecution(t.Context(), request.ID); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("single-fault success retained active grant: %v", err)
		}
		if err = backing.(kernel.ExecutionStore).CheckExecution(t.Context(), request.ID, &lease); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("single-fault success retained owned lease: %v", err)
		}
	} else if phase == "P4" {
		if !ea1RecoveryJSONEqual(fault.heldSource, committed) {
			t.Fatal("Finish hold source differs from committed full receipt")
		}
		current, e := rt.LookupChildExecution(t.Context(), request.ID)
		if e != nil || current != grant {
			t.Fatalf("held Finish lost original grant: %v", e)
		}
		c, e := rt.LookupOwnedPhysicalCallbackReturn(t.Context(), grant)
		if e != nil || c != completion {
			t.Fatalf("held Finish changed original callback: %v", e)
		}
		if _, e = rt.LoadOwnedRetirement(t.Context(), grant.ID); !errors.Is(e, kernel.ErrNotFound) {
			t.Fatalf("precommit hold fabricated retirement: %v", e)
		}
	}
	fresh := reopen()
	freshRT := ea1StartRuntime(t, fresh)
	physical.rt = freshRT
	next := runnerFor(freshRT, "fresh-owner")
	durable, e := freshRT.Load(t.Context(), request.ID)
	if e != nil || !ea1RecoveryJSONEqual(durable, committed) {
		t.Fatal("receipt changed across reopen")
	}
	e = next.ReconcileRootPhysicalExecution(t.Context(), request.ID)
	if noHoldSuccess {
		if !errors.Is(e, kernel.ErrNotFound) {
			t.Fatalf("already acknowledged recovery selected history: %v", e)
		}
		retained, readErr := freshRT.LoadOwnedRetirement(t.Context(), grant.ID)
		if readErr != nil || !ea1RecoveryJSONEqual(retained, accepted) {
			t.Fatalf("acknowledged retirement changed on reopen: %v", readErr)
		}
	} else if e != nil {
		t.Fatalf("actual first-party Root recovery failed: %v", e)
	}
	if _, e = freshRT.LookupChildExecution(t.Context(), request.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("original scope not retired: %v", e)
	}
	ea1CompBarrierUnchanged(t, freshRT, committed)
	if e = fresh.(kernel.ExecutionStore).CheckExecution(t.Context(), request.ID, &lease); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("retired scope retained original native authority: %v", e)
	}
	if e = next.ReconcileRootPhysicalExecution(t.Context(), request.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("retired recovery recreated scope: %v", e)
	}
	m, n := physical.counts()
	if m != models || n != toolCalls || lookups != 1 {
		t.Fatal("receipt recovery replayed dispatch/lookup")
	}
}
func ea1RecoveryJSONEqual(a, b any) bool {
	x, e := json.Marshal(a)
	y, f := json.Marshal(b)
	return e == nil && f == nil && bytes.Equal(x, y)
}
