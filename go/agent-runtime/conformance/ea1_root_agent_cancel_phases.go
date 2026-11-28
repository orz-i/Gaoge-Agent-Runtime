package conformance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// AR-C is actual kernel.Runtime.Cancel over real Root Agent state. It has no
// caller actor/origin argument and Root has no ancestor. Harness authority and
// shared-budget elections have separate Harness fixtures, not these claims.
func RunEA1RootAgentCancelPhases(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store)) {
	for _, v := range []string{"missing", "stale", "prepared", "native-before-owned"} {
		t.Run("P0/"+v, func(t *testing.T) { ea1RootCancelPreflight(t, factory, v) })
	}
	t.Run("P1/active-original-callback", func(t *testing.T) { ea1RootCancelActive(t, factory) })
	for _, k := range []string{"model", "tool"} {
		t.Run("P2/"+k, func(t *testing.T) { ea1RootCancelUnknown(t, factory, k) })
	}
	// Reproduce slow, successful Model-side intent reads independently
	// of the Store implementation or scheduler.
	t.Run("P2/model-delayed-store-read", func(t *testing.T) {
		ea1RootCancelUnknownWithModelReadDelay(t, factory, "model", 175*time.Millisecond)
	})
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
	if !errors.Is(e, kernel.ErrConflict) || hits != 1 {
		t.Fatalf("pre-owned Cancel did not win: %v", e)
	}
	ea1CompBarrierUnchanged(t, f.observer, cancelled)
	f.zero()
}
func ea1RootCancelActive(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store)) {
	f := ea1NewResumeFixture(t, factory, false)
	hits := 0
	var original kernel.ChildExecutionGrant
	check := func(ctx context.Context, entered bool) {
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
		e = f.peer.(kernel.ExecutionStore).CheckExecution(ctx, f.request.ID, &f.store.acquired)
		if entered && e != nil || !entered && (!errors.Is(e, kernel.ErrExecutionConflict) || !errors.Is(e, kernel.ErrChildExecutionBusy)) {
			t.Fatal("Cancel changed original entry qualification", e)
		}
		m, n := f.physical.counts()
		if m != 0 || n != 0 {
			t.Fatal("cut after dispatch")
		}
	}
	f.store.afterOwned = func(ctx context.Context, _ kernel.ChildExecutionGrant) error {
		check(ctx, false)
		return nil
	}
	f.store.afterEntry = func(ctx context.Context) error { check(ctx, true); return nil }
	out, e := f.runner(f.rt, "original", true).Resume(t.Context(), f.request.ID, f.prepared.Run.Revision)
	if e != nil || hits != 2 {
		t.Fatalf("original callback could not finish: %v", e)
	}
	ea1CancelRejected(t, f.observer, out, kernel.ChildExecutionGrant{}, kernel.ErrTerminal)
	m, n := f.physical.counts()
	if m != 1 || n != 0 {
		t.Fatal("Cancel caused replay")
	}
}
func ea1RootCancelUnknown(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), kind string) {
	ea1RootCancelUnknownWithModelReadDelay(t, factory, kind, 0)
}
func ea1RootCancelUnknownWithModelReadDelay(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), kind string, delay time.Duration) {
	ea1RootUnknownResume(t, factory, kind, delay, true)
}
