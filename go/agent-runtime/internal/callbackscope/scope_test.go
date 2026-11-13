package callbackscope

import (
	"context"
	"errors"
	"testing"
)

type adapter struct{ id int }

func testScope(a *adapter) *Scope {
	return New(Binding{RunID: "run", GrantID: "grant", OwnerID: "owner", LeaseID: "lease", SourceHash: "source", Operation: "resume", Epoch: 7}, []any{a})
}
func TestActualLayerCompletionNeedsReturnedNextAndReceipt(t *testing.T) {
	a := &adapter{1}
	s := testScope(a)
	ctx, ticket := Enter(Bind(t.Context(), s), "run", a, "original-input")
	if !Operation(ctx, a, "checkpoint:original", struct{ Revision int }{3}) || !Record(ctx, a, "hydrate", "actual-source") {
		t.Fatal("actual operation rejected")
	}
	if s.Complete() {
		t.Fatal("receipt retired live callback/Next")
	}
	Returned(ticket, errors.New("downstream denied"))
	if !s.Complete() {
		t.Fatal("completed own layer lost downstream distinction")
	}
	s.Close()
	if Record(ctx, a, "late", "source") || s.Complete() {
		t.Fatal("closed receipt reused")
	}
}
func TestReceiptCannotBeClaimedByShapeOrAnotherLayer(t *testing.T) {
	for _, mode := range []string{"public-shape", "foreign-adapter", "duplicate", "contradictory-operation", "unreturned", "post-receipt-panic"} {
		t.Run(mode, func(t *testing.T) {
			a := &adapter{1}
			s := testScope(a)
			ctx, ticket := Enter(Bind(t.Context(), s), "run", a, "input")
			switch mode {
			case "public-shape":
				// Neither a public state hash nor a nil result is the private layer receipt.
				_ = struct {
					Completed bool
					Hash      string
				}{true, "source"}
			case "foreign-adapter":
				Record(ctx, &adapter{2}, "same-operation", "same-source")
			case "duplicate":
				Record(ctx, a, "same-operation", "source")
				Record(ctx, a, "same-operation", "source")
			case "contradictory-operation":
				Operation(ctx, a, "checkpoint:id", "one")
				Operation(ctx, a, "checkpoint:id", "two")
				Record(ctx, a, "return", "source")
			case "unreturned":
				Record(ctx, a, "return", "source")
			case "post-receipt-panic":
				Record(ctx, a, "return", "source")
				Aborted(ticket)
			}
			if mode != "unreturned" {
				Returned(ticket, errors.New("callback failure"))
			}
			if s.Complete() {
				t.Fatal("unproved layer completed")
			}
		})
	}
}
func TestScopeAndNonceAreNotTransferable(t *testing.T) {
	a := &adapter{1}
	s := testScope(a)
	ctx, first := Enter(Bind(t.Context(), s), "run", a, "one")
	Record(ctx, a, "return", "source")
	Returned(first, nil)
	secondCtx, second := Enter(Bind(t.Context(), s), "run", a, "two")
	if first.nonce == second.nonce || first.input == second.input {
		t.Fatal("layer identity reused")
	}
	// A retained first-layer context cannot mint the second layer's receipt.
	if Record(ctx, a, "return", "source") {
		t.Fatal("old nonce accepted")
	}
	Returned(second, errors.New("second callback unknown"))
	if s.Complete() {
		t.Fatal("prior receipt cleared new callback")
	}
	foreign, ft := Enter(secondCtx, "foreign-run", a, "two")
	if ft != nil || Record(foreign, a, "return", "source") {
		t.Fatal("foreign run inherited authority")
	}
	other := testScope(a)
	otherCtx, ot := Enter(Bind(context.Background(), other), "run", a, "one")
	if !Record(otherCtx, a, "return", "source") {
		t.Fatal("independent scope failed")
	}
	Returned(ot, nil)
	if other.tickets[0] == first {
		t.Fatal("scope shared ticket")
	}
}
func TestOpaqueLayerErrorAndPostNextErrorStayUnknown(t *testing.T) {
	trusted := &adapter{1}
	opaque := &adapter{2}
	s := testScope(trusted)
	outer, ot := Enter(Bind(t.Context(), s), "run", opaque, "run")
	inner, it := Enter(outer, "run", trusted, "model")
	Record(inner, trusted, "settled", "receipt")
	Returned(it, nil)
	Returned(ot, errors.New("post Next failed"))
	if s.Complete() {
		t.Fatal("inner evidence erased unknown outer callback")
	}
	if Record(outer, opaque, "settled", "same-shape") {
		t.Fatal("opaque callback self-certified")
	}
}
func TestCompletedScopeProofBindsFullOriginalSource(t *testing.T) {
	a := &adapter{1}
	base := testScope(a)
	finish := func(s *Scope) [32]byte {
		ctx, ticket := Enter(Bind(t.Context(), s), "run", a, "input")
		Record(ctx, a, "return", "actualsource")
		Returned(ticket, nil)
		return ticket.proof
	}
	original := finish(base)
	for _, field := range []string{"grant", "owner", "lease", "epoch", "source", "operation"} {
		s := testScope(a)
		switch field {
		case "grant":
			s.binding.GrantID = "other"
		case "owner":
			s.binding.OwnerID = "other"
		case "lease":
			s.binding.LeaseID = "other"
		case "epoch":
			s.binding.Epoch++
		case "source":
			s.binding.SourceHash = "other"
		case "operation":
			s.binding.Operation = "other"
		}
		if finish(s) == original {
			t.Fatalf("%s not bound", field)
		}
	}
}
