package conformance

import (
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
)

// AR-C is actual kernel.Runtime.Cancel over real Root Agent state. It has no
// caller actor/origin argument and Root has no ancestor. Harness authority and
// shared-budget elections have separate Harness fixtures, not these claims.
func RunEA1RootAgentCancelPhases(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store)) {
	for _, v := range []string{"missing", "stale", "prepared", "native-before-owned"} {
		t.Run("P0/"+v, func(t *testing.T) { ea1RootCancelPreflight(t, factory, v) })
	}
	t.Run("P1/active-original-callback", func(t *testing.T) { ea1RootCancelActive(t, factory) })
	for _, k := range []string{"host", "model", "tool"} {
		t.Run("P2/"+k, func(t *testing.T) { ea1RootCancelUnknown(t, factory, k) })
	}
	for _, p := range []string{"P3", "P4"} {
		for _, tool := range []bool{false, true} {
			t.Run(p+"/"+map[bool]string{false: "yield", true: "terminal"}[tool], func(t *testing.T) { ea1RootCancelSettlement(t, factory, p, tool) })
		}
	}
	t.Run("P4/actual-cancel-CAS-lost-ACK", func(t *testing.T) { ea1RootCancelACK(t, factory) })
	t.Run("P5/cancel-wins-resume-admission", func(t *testing.T) { ea1RootCancelPreflight(t, factory, "native-before-owned") })
	t.Run("P5/resume-scope-rejects-cancel", func(t *testing.T) { ea1RootCancelActive(t, factory) })
	t.Run("P5/two-cancel-owners", func(t *testing.T) { ea1RootCancelElection(t, factory, false) })
	t.Run("P5/cancel-deadline", func(t *testing.T) { ea1RootCancelElection(t, factory, true) })
}
func ea1CancelRejected(t *testing.T, rt *kernel.Runtime, before kernel.Snapshot, g kernel.ChildExecutionGrant, want error) {
	t.Helper()
	_, e := rt.Cancel(t.Context(), before.Run.ID, before.Run.Revision, " competing cancel ")
	if !errors.Is(e, want) {
		t.Fatalf("actual Cancel: %v want %v", e, want)
	}
	ea1CompBarrierUnchanged(t, rt, before)
	if g.ID != "" {
		same, e := rt.LookupChildExecution(t.Context(), before.Run.ID)
		if e != nil || same != g {
			t.Fatal("Cancel changed original scope/epoch/source")
		}
	} else if _, e = rt.LookupChildExecution(t.Context(), before.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatal("Cancel fabricated scope")
	}
}
func ea1CancelCommitted(t *testing.T, rt *kernel.Runtime, before, after kernel.Snapshot) {
	t.Helper()
	if after.Run.Status != kernel.RunStatusCancelled || after.Run.Revision != before.Run.Revision+1 ||
		after.Run.ErrorCode != "run.cancelled" || after.Run.ErrorDetail != "original cancel" ||
		after.Run.ID != before.Run.ID || after.Run.ParentRunID != "" ||
		after.Run.Actor != before.Run.Actor || after.Run.Thread != before.Run.Thread ||
		after.Run.Goal != before.Run.Goal || after.Run.RequestID != before.Run.RequestID ||
		!after.Run.CreatedAt.Equal(before.Run.CreatedAt) ||
		!ea1RecoveryJSONEqual(after.Run.DeadlineAt, before.Run.DeadlineAt) ||
		!ea1RecoveryJSONEqual(after.State, before.State) || after.Checkpoint != nil || after.Result != nil {
		t.Fatal("Cancel changed original source/state/budget or committed unexpected transition")
	}
	expectedRun := before.Run
	expectedRun.Status, expectedRun.Revision = kernel.RunStatusCancelled, before.Run.Revision+1
	expectedRun.ErrorCode, expectedRun.ErrorDetail = "run.cancelled", "original cancel"
	expectedRun.UpdatedAt, expectedRun.EndedAt = after.Run.UpdatedAt, after.Run.EndedAt
	if !ea1RecoveryJSONEqual(expectedRun, after.Run) || after.Run.EndedAt == nil || after.Run.UpdatedAt.Before(before.Run.UpdatedAt) || after.EventHead != before.EventHead+1 {
		t.Fatal("Cancel changed immutable Run fields or duplicated journal")
	}
	events, e := rt.ListEvents(t.Context(), after.Run.ID, before.EventHead, 10)
	if e != nil || len(events) != 1 || events[0].Seq != after.EventHead || events[0].Type != "run.cancelled" || events[0].Message != "original cancel" {
		t.Fatal("Cancel original journal not exactly once", e)
	}
	ea1CompBarrierUnchanged(t, rt, after)
	ea1CancelRejected(t, rt, after, kernel.ChildExecutionGrant{}, kernel.ErrTerminal)
}
func ea1RootCancelPreflight(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), vector string) {
	f := ea1NewResumeFixture(t, factory, false)
	if vector == "missing" || vector == "stale" {
		id, rev, want := f.request.ID, f.prepared.Run.Revision+1, kernel.ErrConflict
		if vector == "missing" {
			id = "absent-root"
			rev = f.prepared.Run.Revision
			want = kernel.ErrNotFound
		}
		_, e := f.observer.Cancel(t.Context(), id, rev, "original cancel")
		if !errors.Is(e, want) || !kernel.IsControlNoEffect(e) {
			t.Fatalf("Cancel preflight: %v", e)
		}
		ea1CompBarrierUnchanged(t, f.observer, f.prepared)
		f.zero()
		return
	}
	if vector == "prepared" {
		out, e := f.observer.Cancel(t.Context(), f.request.ID, f.prepared.Run.Revision, " original cancel ")
		if e != nil {
			t.Fatal(e)
		}
		ea1CancelCommitted(t, f.observer, f.prepared, out)
		_, e = f.runner(f.rt, "late", true).Resume(t.Context(), out.Run.ID, out.Run.Revision)
		if !errors.Is(e, agent.ErrRunTerminal) {
			t.Fatal("cancelled root resumed", e)
		}
		f.zero()
		return
	}
	var cancelled kernel.Snapshot
	hits := 0
	f.store.beforeOwned = func(ctx context.Context, g kernel.ChildExecutionGrant) error {
		hits++
		if g.AdmissionOperation != "resume" || g.ExpectedRevision != f.prepared.Run.Revision {
			t.Fatal("not original pre-admission")
		}
		var e error
		cancelled, e = f.observer.Cancel(ctx, f.request.ID, f.prepared.Run.Revision, "original cancel")
		if e != nil {
			t.Fatal(e)
		}
		ea1CancelCommitted(t, f.observer, f.prepared, cancelled)
		_, e = f.observer.RenewExecution(ctx, kernel.ExecutionRenewRequest{Lease: f.store.acquired, LeaseDuration: 2 * time.Second})
		if !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatal("Cancel did not revoke old native authority", e)
		}
		return nil
	}
	_, e := f.runner(f.rt, "candidate", true).Resume(t.Context(), f.request.ID, f.prepared.Run.Revision)
	if !errors.Is(e, kernel.ErrExecutionConflict) || hits != 1 {
		t.Fatalf("pre-owned Cancel did not win: %v", e)
	}
	ea1CompBarrierUnchanged(t, f.observer, cancelled)
	f.zero()
}
func ea1RootCancelActive(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store)) {
	f := ea1NewResumeFixture(t, factory, false)
	hits := 0
	var original kernel.ChildExecutionGrant
	check := func(ctx context.Context) {
		hits++
		before, e := f.observer.Load(ctx, f.request.ID)
		if e != nil {
			t.Fatal(e)
		}
		g, e := f.observer.LookupChildExecution(ctx, f.request.ID)
		if e != nil {
			t.Fatal(e)
		}
		if g.AdmissionOperation != "resume" || !g.MatchesExecution(f.store.acquired) || g.ExpectedRevision != f.prepared.Run.Revision {
			t.Fatal("wrong original scope")
		}
		if original.ID == "" {
			original = g
		} else if original != g {
			t.Fatal("scope replaced")
		}
		ea1CancelRejected(t, f.observer, before, g, kernel.ErrChildExecutionBusy)
		if e = f.peer.(kernel.ExecutionStore).CheckExecution(ctx, f.request.ID, &f.store.acquired); e != nil {
			t.Fatal("Cancel revoked active owner", e)
		}
		m, n := f.physical.counts()
		if m != 0 || n != 0 {
			t.Fatal("cut after dispatch")
		}
	}
	f.store.afterOwned = func(ctx context.Context, _ kernel.ChildExecutionGrant) error {
		if f.host.calls != 0 {
			t.Fatal("late admission cut")
		}
		check(ctx)
		return nil
	}
	f.host.before = func(ctx context.Context) error { check(ctx); return nil }
	out, e := f.runner(f.rt, "original", true).Resume(t.Context(), f.request.ID, f.prepared.Run.Revision)
	if e != nil || hits != 2 || f.host.calls != 1 {
		t.Fatalf("original callback could not finish: %v", e)
	}
	ea1CancelRejected(t, f.observer, out, kernel.ChildExecutionGrant{}, kernel.ErrTerminal)
	m, n := f.physical.counts()
	if m != 1 || n != 0 {
		t.Fatal("Cancel caused replay")
	}
}
func ea1RootCancelUnknown(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), kind string) {
	f := ea1NewResumeFixture(t, factory, kind == "tool")
	injected := errors.New("actual Resume physical outcome missing")
	event := "agent.model_invocation.completed"
	if kind == "tool" {
		event = "tool.completed"
	}
	fault := &ea1AgentReceiptFault{Store: f.store, ExecutionStore: f.store, ControlStore: f.store, ChildExecutionStore: f.store, ExecutionQuarantineStore: f.store, OwnedPhysicalExecutionStore: f.store, event: event, injected: injected}
	rt := ea1StartRuntime(t, fault)
	f.physical.rt = rt
	if kind == "host" {
		f.host.unknown = injected
	}
	runner := f.runner(rt, "resume-owner", true)
	_, e := runner.Resume(t.Context(), f.request.ID, f.prepared.Run.Revision)
	if !errors.Is(e, injected) || f.host.calls != 1 {
		t.Fatalf("actual Resume unknown cut missing: %v", e)
	}
	before, e := rt.Load(t.Context(), f.request.ID)
	if e != nil || !runner.VerifyFrozenStartRequest(before, f.request) {
		t.Fatal("original Resume source lost")
	}
	g, e := rt.LookupChildExecution(t.Context(), f.request.ID)
	if e != nil {
		t.Fatal(e)
	}
	lease := f.store.acquired
	if g.AdmissionOperation != "resume" || g.ExpectedRevision != f.prepared.Run.Revision || g.ExecutionOwnerID != lease.OwnerID || g.ExecutionLeaseID != lease.LeaseID || g.ExecutionEpoch != lease.Epoch {
		t.Fatal("unknown lost original Resume authority")
	}
	completion, e := rt.LookupOwnedPhysicalCallbackReturn(t.Context(), g)
	if e != nil || completion.HostCompleted {
		t.Fatalf("actual opaque Host error not retained: %v", e)
	}
	m, n := f.physical.counts()
	if kind == "host" {
		if m != 0 || n != 0 || f.host.sends != 1 || fault.failures != 0 {
			t.Fatal("Host fixture substituted Model/Tool send")
		}
	} else {
		if fault.failures != 1 || m != 1 || (kind == "tool" && n != 1) || (kind == "model" && n != 0) {
			t.Fatal("actual Resume dispatch not reached")
		}
		view, e := agent.ViewState(before)
		if e != nil || len(view.ModelInvocations) != 1 {
			t.Fatal("original invocation missing")
		}
		inv := view.ModelInvocations[0]
		f.physical.assertOriginal(t, f.request, inv)
		original := f.physical.intents[0]
		canonical := model.CloneRequest(original.Request)
		canonical.InvocationID = ""
		sourceBytes, sourceErr := json.Marshal(struct {
			Request             model.Request `json:"request"`
			CatalogFingerprints []string      `json:"catalogFingerprints"`
			Provider            string        `json:"provider"`
			BindingFingerprint  string        `json:"bindingFingerprint"`
		}{canonical, original.CatalogFingerprints, original.Provider, original.BindingFingerprint})
		hash := sha256.Sum256(sourceBytes)
		if sourceErr != nil || original.RequestHash != hex.EncodeToString(hash[:]) || inv.RequestHash != original.RequestHash || inv.ID != original.ID || inv.ExecutionAttempt != original.ExecutionAttempt || inv.DispatchAttempt != original.DispatchAttempt || inv.Provider != original.Provider || inv.BindingFingerprint != original.BindingFingerprint || inv.SourceRevision != original.SourceRevision || inv.SourceRevision < f.prepared.Run.Revision {
			t.Fatal("Resume invocation not exact original source hash")
		}
		if kind == "model" && (inv.Status != agent.ModelInvocationPending || inv.CompletedAt != nil || !ea1RecoveryJSONEqual(inv.Response, model.Response{})) {
			t.Fatal("missing Model receipt fabricated as completed")
		}
		if kind == "tool" {
			var marker struct {
				ToolDispatchID string `json:"toolDispatchID"`
			}
			if json.Unmarshal(before.State, &marker) != nil || marker.ToolDispatchID != "original-tool-call" {
				t.Fatal("original unresolved Tool intent missing")
			}
		}
		if inv.Provider != f.physical.ProviderName() || inv.BindingFingerprint != f.physical.BindingFingerprintForRequest(inv.Request) || inv.ExecutionAttempt != 1 || inv.DispatchAttempt != 1 {
			t.Fatal("original request provider/attempt lost")
		}
		if kind == "tool" {
			ea1StartSettlementJSONEqual(t, "prior consumed Model response", inv.Response, f.physical.response)
		}
	}
	ea1CancelRejected(t, f.observer, before, g, kernel.ErrChildExecutionBusy)
	if e = f.peer.(kernel.ExecutionStore).CheckExecution(t.Context(), f.request.ID, &lease); e != nil {
		t.Fatalf("rejected Cancel revoked original native owner: %v", e)
	}
	delay := time.Until(lease.ExpiresAt.Add(150 * time.Millisecond))
	if delay > 5*time.Second {
		t.Fatal("unbounded native lease fixture")
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
	fresh := f.reopen()
	freshRT := ea1StartRuntime(t, fresh)
	f.physical.rt = freshRT
	// Feature claim/RetryAfter time is advanced independently; native expiry
	// above was awaited against the real authoritative Store clock.
	f.clockOffset = 3 * time.Minute
	next := f.runner(freshRT, "fresh-owner", true)
	if e = fresh.(kernel.ExecutionStore).CheckExecution(t.Context(), f.request.ID, &lease); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("Store native TTL not expired: %v", e)
	}
	for range 2 {
		ea1CancelRejected(t, freshRT, before, g, kernel.ErrChildExecutionBusy)
		_, e = next.Resume(t.Context(), f.request.ID, before.Run.Revision)
		want := agent.ErrRootOutcomeUnknown
		switch kind {
		case "model":
			want = agent.ErrModelOutcomeUnknown
		case "tool":
			want = agent.ErrToolOutcomeUnknown
		}
		if !errors.Is(e, want) {
			t.Fatalf("Resume wrong unknown boundary: %v want %v", e, want)
		}
		ea1CompBarrierUnchanged(t, freshRT, before)
		same, err := freshRT.LookupChildExecution(t.Context(), f.request.ID)
		if err != nil || same != g {
			t.Fatal("TTL recovery changed original scope")
		}
		c, err := freshRT.LookupOwnedPhysicalCallbackReturn(t.Context(), g)
		if err != nil || !ea1RecoveryJSONEqual(c, completion) {
			t.Fatal("TTL upgraded Host unknown")
		}
		a, b := f.physical.counts()
		if a != m || b != n || f.host.calls != 1 || (kind == "host" && f.host.sends != 1) {
			t.Fatal("unknown Resume replayed physical callback")
		}
		if err = fresh.(kernel.ExecutionStore).CheckExecution(t.Context(), f.request.ID, &lease); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatal("recovery revived original epoch")
		}
	}
}
