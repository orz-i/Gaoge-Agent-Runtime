package callbackscope

import (
	"context"
	"errors"
	"testing"
)

func TestManagedSuspensionRequiresActualQuiescentFrame(t *testing.T) {
	a := new(int)
	s := New(Binding{RunID: "run", GrantID: "grant"}, []any{a})
	ctx, ticket := Enter(Bind(context.Background(), s), "run", a, "original-input")
	if _, ok := ManagedSuspension(ctx, a, "request"); ok {
		t.Fatal("configuration alone certified frame")
	}
	Operation(ctx, a, "managed.hydrated", "actual-source")
	ManagedPrepareEntered(ctx, a)
	if _, ok := ManagedSuspension(ctx, a, "request"); ok {
		t.Fatal("inflight Prepare certified")
	}
	ManagedPrepareReturned(ctx, a, "actual-request", nil)
	if _, ok := ManagedSuspension(ctx, a, "request"); !ok {
		t.Fatal("actual returned bounded preparation rejected")
	}
	otherCtx, other := Enter(ctx, "run", new(int), "opaque")
	if _, ok := ManagedSuspension(otherCtx, a, "request"); ok {
		t.Fatal("foreign frame adopted proof")
	}
	if _, ok := ManagedSuspension(ctx, a, "request"); ok {
		t.Fatal("outstanding sibling certified")
	}
	Returned(other, errors.New("unknown"))
	if _, ok := ManagedSuspension(ctx, a, "request"); ok {
		t.Fatal("unknown sibling certified")
	}
	Returned(ticket, nil)
	if _, ok := ManagedSuspension(ctx, a, "request"); ok {
		t.Fatal("returned frame reused as suspended")
	}
}
func TestManagedPreparationTracksSuccessiveActualRequests(t *testing.T) {
	a := new(int)
	s := New(Binding{RunID: "run"}, []any{a})
	ctx, _ := Enter(Bind(context.Background(), s), "run", a, "input")
	Operation(ctx, a, "managed.hydrated", "source")
	for _, request := range []string{"first", "second"} {
		ManagedPrepareEntered(ctx, a)
		if !ManagedPrepareReturned(ctx, a, request, nil) {
			t.Fatal("legitimate next preparation rejected")
		}
		if _, ok := ManagedSuspension(ctx, a, request); !ok {
			t.Fatal("missing returned prepare")
		}
	}
	ManagedPrepareEntered(ctx, a)
	ManagedPrepareReturned(ctx, a, "failed", errors.New("write uncertain"))
	if _, ok := ManagedSuspension(ctx, a, "failed"); ok {
		t.Fatal("failed Prepare certified")
	}
}
