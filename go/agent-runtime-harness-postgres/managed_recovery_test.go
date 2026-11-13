package harnesspostgres_test

import (
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestManagedHostSQLRejectsUnissuedProofs(t *testing.T) {
	s := newStore(t)
	a, err := s.ManagedRecoveryAuthority(t.Context())
	if err != nil || !a.Valid() {
		t.Fatal(a, err)
	}
	if _, err = s.ReserveManagedRecovery(t.Context(), kernel.ManagedParkedProof{}); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("unissued parked proof: %v", err)
	}
	if err = s.CloseManagedRecovery(t.Context(), kernel.ManagedClosureProof{}); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("unissued closure proof: %v", err)
	}
	after, err := s.ManagedRecoveryAuthority(t.Context())
	if err != nil || a != after {
		t.Fatal("rejected proof mutated authority")
	}
}
