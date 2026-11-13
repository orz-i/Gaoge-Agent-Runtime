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

// Only AC-Q P1/P3/P5. ReconcileChildPhysicalGrant consumes an ORIGINAL
// post-callback marker; it cannot Mark from clean state or send new effects.
// Memory reopening means retained state/new runner, not process persistence.
func RunEA1ChildAgentRecoveryPhases(t *testing.T, factory func(*testing.T) (kernel.Store, func() (kernel.Store, kernel.Store))) {
	t.Run("P1/active-callback-before-and-after-Next", func(t *testing.T) { ea1ChildRecoveryPhase(t, factory, "P1", false, false) })
	for _, tool := range []bool{false, true} {
		name := map[bool]string{false: "model-yield", true: "terminal-tool"}[tool]
		t.Run("P3/unmarked-"+name, func(t *testing.T) { ea1ChildRecoveryPhase(t, factory, "unmarked", tool, false) })
		t.Run("P3/marked-"+name, func(t *testing.T) { ea1ChildRecoveryPhase(t, factory, "marked", tool, false) })
	}
	t.Run("P5/two-reconcilers-parent-cancel", func(t *testing.T) { ea1ChildRecoveryPhase(t, factory, "P5", false, false) })
	t.Run("P5/two-reconcilers-ancestor-deadline", func(t *testing.T) { ea1ChildRecoveryPhase(t, factory, "P5", false, true) })
}

type ea1ChildRecoveryPhaseStore struct {
	*ea1AgentAdmissionStore
	mode            string
	fault           error
	marks, finishes int
	beforeFinish    func()
}

func (s *ea1ChildRecoveryPhaseStore) MarkChildExecutionSettled(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	if s.mode == "unmarked" {
		return s.fault
	}
	e := s.ChildExecutionStore.MarkChildExecutionSettled(ctx, g, p)
	if e == nil {
		s.marks++
	}
	return e
}
func (s *ea1ChildRecoveryPhaseStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	if s.beforeFinish != nil {
		s.beforeFinish()
	}
	if s.mode == "marked" || s.mode == "P5" {
		return s.fault
	}
	e := s.ChildExecutionStore.FinishChildExecution(ctx, g)
	if e == nil {
		s.finishes++
	}
	return e
}

type ea1ChildRecoveryPhaseModel struct {
	*ea1StartSettlementPhysical
	before func()
}

func (m *ea1ChildRecoveryPhaseModel) Generate(ctx context.Context, r model.Request) (model.Response, error) {
	if m.before != nil {
		m.before()
	}
	return m.ea1StartSettlementPhysical.Generate(ctx, r)
}

type ea1ChildRecoveryPhaseHost struct {
	t             *testing.T
	request       agent.StartRequest
	physical      *ea1StartSettlementPhysical
	before, after func()
}

func (*ea1ChildRecoveryPhaseHost) Name() string { return "ea1.actual.child.recovery.callback" }
func (h *ea1ChildRecoveryPhaseHost) Run(ctx context.Context, i plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	if i.Operation != plugin.RunStart || i.RunID != h.request.ID || i.ParentRunID != h.request.ParentRunID || i.Actor != h.request.Actor || i.Thread != h.request.Thread || i.Goal != h.request.Goal {
		h.t.Fatal("original child callback source changed")
	}
	h.physical.mu.Lock()
	h.physical.hostCalls++
	h.physical.mu.Unlock()
	if h.before != nil {
		h.before()
	}
	out, e := next(ctx)
	if h.after != nil {
		h.after()
	}
	return out, e
}
func ea1ChildRecoveryPhase(t *testing.T, factory func(*testing.T) (kernel.Store, func() (kernel.Store, kernel.Store)), phase string, withTool, deadline bool) {
	backing, reopen := factory(t)
	admission := ea1AgentAdmissionWrap(t, backing)
	cut := errors.New("actual child callback settlement cut")
	store := &ea1ChildRecoveryPhaseStore{ea1AgentAdmissionStore: admission, mode: phase, fault: cut}
	rt := ea1StartRuntime(t, store)
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "original-actor"}
	thread := kernel.ThreadRef{Kind: "conversation", ID: "original-thread"}
	var due *time.Time
	if deadline {
		v := time.Now().UTC().Add(3 * time.Second)
		due = &v
	}
	grandparent, e := rt.Create(t.Context(), kernel.CreateRequest{ID: "original-grandparent", Kind: "workflow", Actor: actor, Thread: thread, Goal: "grandparent", State: json.RawMessage("{}"), DeadlineAt: due})
	if e != nil {
		t.Fatal(e)
	}
	parent, e := rt.Create(t.Context(), kernel.CreateRequest{ID: "original-parent", ParentRunID: grandparent.Run.ID, Kind: "workflow", Actor: actor, Thread: thread, Goal: "parent", State: json.RawMessage("{}")})
	if e != nil {
		t.Fatal(e)
	}
	request := agent.StartRequest{ID: "original-child-recovery", RequestID: "original-child-request", ParentRunID: parent.Run.ID, OriginFingerprint: agent.FrozenOriginFingerprint([]byte(`{"source":"original-child","context":"isolated"}`)), Actor: actor, Thread: thread, Goal: "original child recovery", Instructions: "Preserve original source.", Model: "fixture-model", ModelOptions: json.RawMessage(`{"temperature":0.1}`), Limits: agent.Limits{MaxLLMCalls: 2, MaxToolCalls: 1}}
	if withTool {
		request.ToolKeys = []string{"effect"}
	}
	physical := &ea1StartSettlementPhysical{rt: rt, tool: withTool}
	physicalModel := &ea1ChildRecoveryPhaseModel{ea1StartSettlementPhysical: physical}
	definition := tools.Definition{Key: "effect", Name: "Original Effect", Description: "frozen terminal effect", InputSchema: json.RawMessage(`{"type":"object","properties":{"source":{"type":"string"}},"required":["source"]}`), Terminal: true}
	registry, e := tools.NewRegistry([]tools.Registration{{Definition: definition, Handler: tools.HandlerFunc(physical.execute)}})
	if e != nil {
		t.Fatal(e)
	}
	host := &ea1ChildRecoveryPhaseHost{t: t, request: request, physical: physical}
	preflightCalls := 0
	makeRunner := func(runtime *kernel.Runtime, owner string) *agent.Runner {
		r, e := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: physicalModel, Catalog: registry, Executor: registry, RunMiddleware: []plugin.RunMiddleware{host}, StartControlPreflight: func(ctx context.Context, actual agent.StartRequest) error {
			preflightCalls++
			if !reflect.DeepEqual(actual, request) {
				return agent.ErrInvalidRequest
			}
			for _, expected := range []kernel.Snapshot{parent, grandparent} {
				loaded, err := runtime.Load(ctx, expected.Run.ID)
				if err != nil {
					return err
				}
				if !ea1RecoveryJSONEqual(loaded, expected) {
					return agent.ErrInvalidRequest
				}
			}
			return nil
		}, ToolMiddleware: []plugin.ToolMiddleware{physical}, RunExecution: &agent.RunExecution{OwnerID: owner, LeaseDuration: 3 * time.Minute}})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	first := makeRunner(rt, "original-owner")
	var created kernel.Snapshot
	admission.afterCreate = func(_ context.Context, s kernel.Snapshot) error {
		created = s
		if !first.VerifyFrozenStartRequest(s, request) || s.Run.ParentRunID != parent.Run.ID {
			t.Fatal("Create lost child origin/parent/source")
		}
		return nil
	}
	// Before native acquisition and after Next has returned, the outer callback
	// still has not returned. Neither a clean state nor a durable Model receipt
	// permits this public recovery entry to invent its settlement marker.
	cuts := 0
	modelCuts := 0
	activeCheck := func() {
		cuts++
		current, e := rt.Load(t.Context(), request.ID)
		if e != nil {
			t.Fatal(e)
		}
		g, e := rt.LookupChildExecution(t.Context(), request.ID)
		if e != nil {
			t.Fatal(e)
		}
		if e = first.ReconcileChildPhysicalGrant(t.Context(), request.ID); !errors.Is(e, kernel.ErrChildExecutionBusy) || !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatal("active clean callback was retired", e)
		}
		ea1ChildRecoveryPhaseUnchanged(t, rt, current, parent, grandparent, g)
		ea1ChildRecoveryPhaseControls(t, rt, current, parent, grandparent, g)
		physical.mu.Lock()
		models := len(physical.requests)
		toolsCount := len(physical.toolRequests)
		physical.mu.Unlock()
		if models != cuts-1 || toolsCount != 0 {
			t.Fatal("receipt-only recovery sent or cut was misplaced")
		}
	}
	if phase == "P1" {
		host.before = activeCheck
		host.after = activeCheck
		physicalModel.before = func() {
			modelCuts++
			current, e := rt.Load(t.Context(), request.ID)
			if e != nil {
				t.Fatal(e)
			}
			g, e := rt.LookupChildExecution(t.Context(), request.ID)
			if e != nil {
				t.Fatal(e)
			}
			if e = first.ReconcileChildPhysicalGrant(t.Context(), request.ID); !errors.Is(e, kernel.ErrChildExecutionBusy) || errors.Is(e, kernel.ErrExecutionConflict) {
				t.Fatal("pending native model should stop at knownSettlement, not Finish", e)
			}
			ea1ChildRecoveryPhaseControls(t, rt, current, parent, grandparent, g)
			if e = backing.(kernel.ExecutionStore).CheckExecution(t.Context(), request.ID, &admission.acquired); e != nil {
				t.Fatal("rejected recovery/control revoked original live native authority", e)
			}
			if len(physical.requests) != 0 || len(physical.toolRequests) != 0 {
				t.Fatal("pre-physical Model cut already sent")
			}
		}
	}
	out, startErr := first.StartRun(t.Context(), request)
	if preflightCalls != 1 || created.Run.ID != request.ID {
		t.Fatalf("exact child preflight/Create not reached: preflight=%d created=%q err=%v", preflightCalls, created.Run.ID, startErr)
	}
	defer func() {
		if preflightCalls != 1 {
			t.Error("receipt-only reconciliation repeated Start preflight")
		}
	}()
	if phase == "P1" {
		if startErr != nil || cuts != 2 || modelCuts != 1 || store.marks != 1 || store.finishes != 1 {
			t.Fatal("actual callback failed after denied recovery", startErr)
		}
		ea1AssertChildRecoveryPhaseReceipt(t, physical, request, definition, out, withTool)
		ea1CompBarrierUnchanged(t, rt, parent)
		ea1CompBarrierUnchanged(t, rt, grandparent)
		if e = first.ReconcileChildPhysicalGrant(t.Context(), request.ID); !errors.Is(e, kernel.ErrNotFound) {
			t.Fatal("retired callback replay", e)
		}
		return
	}
	if !errors.Is(startErr, cut) || !errors.Is(startErr, kernel.ErrChildExecutionBusy) {
		t.Fatal("actual child settlement cut absent", startErr)
	}
	before, e := rt.Load(t.Context(), request.ID)
	if e != nil {
		t.Fatal(e)
	}
	g, e := rt.LookupChildExecution(t.Context(), request.ID)
	if e != nil || g.ChildRunID != request.ID || g.ExpectedRevision != created.Run.Revision || g.IsOwned() {
		t.Fatal("original child grant replaced by Root authority", e)
	}
	ea1AssertChildRecoveryPhaseReceipt(t, physical, request, definition, before, withTool)
	if phase == "unmarked" && store.marks != 0 {
		t.Fatal("unmarked fixture committed marker")
	}
	if phase != "unmarked" && store.marks != 1 {
		t.Fatal("actual whole callback did not commit exactly one marker")
	}
	ea1ChildRecoveryPhaseControls(t, rt, before, parent, grandparent, g)
	a, b := reopen()
	ar, br := ea1StartRuntime(t, a), ea1StartRuntime(t, b)
	ra, rb := makeRunner(ar, "fresh-a"), makeRunner(br, "fresh-b")
	ea1ChildRecoveryPhaseUnchanged(t, ar, before, parent, grandparent, g)
	nativeCalls := [3]int{admission.acquireCalls, admission.renewCalls, admission.releaseCalls}
	defer func() {
		if nativeCalls != [3]int{admission.acquireCalls, admission.renewCalls, admission.releaseCalls} {
			t.Error("reconciliation altered original native owner lifecycle")
		}
	}()
	if phase == "unmarked" {
		for _, r := range []*agent.Runner{ra, rb} {
			e = r.ReconcileChildPhysicalGrant(t.Context(), request.ID)
			if !errors.Is(e, kernel.ErrChildExecutionBusy) || !errors.Is(e, kernel.ErrExecutionConflict) {
				t.Fatal("receipt without original marker retired", e)
			}
			ea1ChildRecoveryPhaseUnchanged(t, ar, before, parent, grandparent, g)
		}
		ea1AssertChildRecoveryPhaseReceipt(t, physical, request, definition, before, withTool)
		return
	}
	if phase == "P5" {
		if deadline {
			delay := time.Until(due.Add(30 * time.Millisecond))
			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-t.Context().Done():
					t.Fatal(t.Context().Err())
				}
			}
		}
		entered := make(chan struct{}, 2)
		release := make(chan struct{})
		barrier := func() {
			entered <- struct{}{}
			select {
			case <-release:
			case <-t.Context().Done():
			}
		}
		sa := &ea1ChildRecoveryPhaseStore{ea1AgentAdmissionStore: ea1AgentAdmissionWrap(t, a), beforeFinish: barrier}
		sb := &ea1ChildRecoveryPhaseStore{ea1AgentAdmissionStore: ea1AgentAdmissionWrap(t, b), beforeFinish: barrier}
		ra = makeRunner(ea1StartRuntime(t, sa), "reconciler-a")
		rb = makeRunner(ea1StartRuntime(t, sb), "reconciler-b")
		results := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		for _, r := range []*agent.Runner{ra, rb} {
			go func(r *agent.Runner) {
				defer wg.Done()
				results <- r.ReconcileChildPhysicalGrant(t.Context(), request.ID)
			}(r)
		}
		for range 2 {
			select {
			case <-entered:
			case <-t.Context().Done():
				close(release)
				wg.Wait()
				t.Fatal("actual Finish barrier absent")
			}
		}
		ea1ChildRecoveryPhaseControls(t, ar, before, parent, grandparent, g)
		if deadline {
			_, _, e = br.Expire(t.Context(), grandparent.Run.ID, grandparent.Run.Revision)
			if !errors.Is(e, kernel.ErrChildExecutionBusy) {
				t.Fatal("due ancestor bypassed unresolved original scope", e)
			}
			ea1ChildRecoveryPhaseUnchanged(t, ar, before, parent, grandparent, g)
		}
		close(release)
		wg.Wait()
		close(results)
		wins, losers := 0, 0
		for err := range results {
			if err == nil {
				wins++
			} else if errors.Is(err, kernel.ErrChildExecutionBusy) && errors.Is(err, kernel.ErrExecutionConflict) {
				losers++
			} else {
				t.Fatal("wrong competing recovery result", err)
			}
		}
		if wins != 1 || losers != 1 || sa.finishes+sb.finishes != 1 || sa.acquireCalls+sb.acquireCalls+sa.renewCalls+sb.renewCalls+sa.releaseCalls+sb.releaseCalls != 0 {
			t.Fatal("two recovery owners did not elect one original Finish")
		}
	} else if e = ra.ReconcileChildPhysicalGrant(t.Context(), request.ID); e != nil {
		t.Fatal("original marked receipt could not retire", e)
	}
	ea1CompBarrierUnchanged(t, ar, before)
	ea1CompBarrierUnchanged(t, ar, parent)
	ea1CompBarrierUnchanged(t, ar, grandparent)
	ea1AssertChildRecoveryPhaseReceipt(t, physical, request, definition, before, withTool)
	// Use an unwrapped runner after the concurrent barrier; never reuse a closed
	// callback gate to accidentally turn sequential retry into claimed concurrency.
	if e = makeRunner(ar, "inspection").ReconcileChildPhysicalGrant(t.Context(), request.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatal("retired original grant replay", e)
	}
	if deadline {
		expired, changed, e := ar.Expire(t.Context(), grandparent.Run.ID, grandparent.Run.Revision)
		if e != nil || !changed || expired.Run.Status != kernel.RunStatusFailed || expired.Run.ErrorCode != "run.deadline_exceeded" || expired.Run.Revision != grandparent.Run.Revision+1 || !ea1RecoveryJSONEqual(expired.State, grandparent.State) {
			t.Fatal("settled scope still blocks due ancestor", e)
		}
	} else {
		cancelled, e := ar.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "original cancel")
		if e != nil {
			t.Fatal("original settlement did not release ancestor", e)
		}
		expected := parent.Run
		expected.Status, expected.Revision = kernel.RunStatusCancelled, parent.Run.Revision+1
		expected.ErrorCode, expected.ErrorDetail = "run.cancelled", "original cancel"
		expected.UpdatedAt, expected.EndedAt = cancelled.Run.UpdatedAt, cancelled.Run.EndedAt
		if !ea1RecoveryJSONEqual(expected, cancelled.Run) || !ea1RecoveryJSONEqual(cancelled.State, parent.State) || cancelled.EventHead != parent.EventHead+1 || cancelled.Run.EndedAt == nil {
			t.Fatal("ancestor cancellation changed original identity/state beyond exact transition")
		}
		ea1CompBarrierUnchanged(t, ar, grandparent)
	}
	ea1CompBarrierUnchanged(t, ar, before)
}
func ea1ChildRecoveryPhaseUnchanged(t *testing.T, rt *kernel.Runtime, child, parent, grandparent kernel.Snapshot, g kernel.ChildExecutionGrant) {
	t.Helper()
	for _, s := range []kernel.Snapshot{child, parent, grandparent} {
		ea1CompBarrierUnchanged(t, rt, s)
	}
	same, e := rt.LookupChildExecution(t.Context(), child.Run.ID)
	if e != nil || same != g {
		t.Fatal("recovery changed exact original child grant", e)
	}
}
func ea1ChildRecoveryPhaseControls(t *testing.T, rt *kernel.Runtime, child, parent, grandparent kernel.Snapshot, g kernel.ChildExecutionGrant) {
	t.Helper()
	for _, s := range []kernel.Snapshot{child, parent, grandparent} {
		want := kernel.ErrChildExecutionBusy
		if s.Run.Status == kernel.RunStatusCompleted {
			want = kernel.ErrTerminal
		}
		_, e := rt.Cancel(t.Context(), s.Run.ID, s.Run.Revision, "competing cancellation")
		if !errors.Is(e, want) {
			t.Fatal("physical child/ancestor control wrong priority", e, want)
		}
		ea1ChildRecoveryPhaseUnchanged(t, rt, child, parent, grandparent, g)
	}
}
func ea1AssertChildRecoveryPhaseReceipt(t *testing.T, p *ea1StartSettlementPhysical, req agent.StartRequest, definition tools.Definition, s kernel.Snapshot, withTool bool) {
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
	var frozen struct {
		OriginFingerprint string `json:"originFingerprint"`
	}
	if json.Unmarshal(s.State, &frozen) != nil || frozen.OriginFingerprint != req.OriginFingerprint || frozen.OriginFingerprint == "" {
		t.Fatal("original child frozen origin lost")
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
		t.Fatal("frozen Child source/budget changed")
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
