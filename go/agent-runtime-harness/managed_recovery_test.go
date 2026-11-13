package harness

import (
	"errors"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"testing"
)

func TestManagedHostMemoryRejectsUnissuedProofs(t *testing.T) {
	s := NewMemoryStore()
	a, err := s.ManagedRecoveryAuthority(t.Context())
	if err != nil || !a.Valid() {
		t.Fatal(a, err)
	}
	if _, err = s.ReserveManagedRecovery(t.Context(), kernel.ManagedParkedProof{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("unissued parked proof: %v", err)
	}
	if err = s.CloseManagedRecovery(t.Context(), kernel.ManagedClosureProof{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("unissued closure proof: %v", err)
	}
	after, err := s.ManagedRecoveryAuthority(t.Context())
	if err != nil || a != after || len(s.managedReservations) != 0 || len(s.managedClosures) != 0 || len(s.items) != 0 {
		t.Fatal("denied proof mutated Host authority or history")
	}
	other, err := NewMemoryStore().ManagedRecoveryAuthority(t.Context())
	if err != nil || other == a {
		t.Fatal("independent Memory stores share authority")
	}
}
