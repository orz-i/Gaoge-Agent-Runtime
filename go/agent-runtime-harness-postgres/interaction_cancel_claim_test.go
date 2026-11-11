package harnesspostgres_test

import (
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

// The existing Store revision CAS is the shared dispatch/cancel arbiter used
// by the business Interaction handler. Verify both orderings on Memory and
// the GORM SQL implementation, not only on the Harness in-memory fixture.
func TestInteractionHandlerClaimAndCancellationAreCASExclusive(t *testing.T) {
	for name, create := range rootInvocationStores() {
		t.Run(name, func(t *testing.T) {
			store := create(t)
			for _, dispatchFirst := range []bool{false, true} {
				suffix := "cancel-first"
				if dispatchFirst {
					suffix = "dispatch-first"
				}
				root := rootInvocationFixture(name+"-"+suffix, name+"-"+suffix+"-turn", "")
				root.Status = harness.InvocationWaitingInput
				start, fresh, err := store.CreateInvocation(t.Context(), root)
				if err != nil || !fresh {
					t.Fatalf("create fixture: fresh=%v err=%v", fresh, err)
				}
				dispatch := start
				dispatch.Status = harness.InvocationRunning
				cancel := start
				cancel.Status = harness.InvocationCancelled
				if dispatchFirst {
					if _, err = store.UpdateInvocation(t.Context(), dispatch, start.Revision); err != nil {
						t.Fatal(err)
					}
					if _, err = store.UpdateInvocation(t.Context(), cancel, start.Revision); !errors.Is(err, harness.ErrConflict) {
						t.Fatalf("stale cancellation accepted after business dispatch claim: %v", err)
					}
				} else {
					// A bare terminal status is not a cancellation operation:
					// only the elected Cancel path can carry the exact
					// physical control/owner proof. An unverified competing
					// Cancelled CAS must lose *before* the actual handler claims.
					if _, err = store.UpdateInvocation(t.Context(), cancel, start.Revision); !errors.Is(err, harness.ErrConflict) {
						t.Fatalf("unverified cancellation stole the handler's CAS: %v", err)
					}
					if _, err = store.UpdateInvocation(t.Context(), dispatch, start.Revision); err != nil {
						t.Fatalf("legitimate waiting handler did not claim after rejected cancel: %v", err)
					}
				}
				persisted, err := store.GetInvocation(t.Context(), root.ID)
				if err != nil {
					t.Fatal(err)
				}
				want := harness.InvocationRunning
				if persisted.Status != want || persisted.Revision != start.Revision+1 {
					t.Fatalf("CAS winner was not durable: status=%s revision=%d", persisted.Status, persisted.Revision)
				}
			}
		})
	}
}
