package harnesspostgres_test

import (
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestManagedHostSQLRejectsUnissuedProofs(t *testing.T) {
	s := newStore(t)
	a, err := s.ManagedRecoveryAuthority(t.Context())
	if err != nil || !a.Valid() {
		t.Fatal(a, err)
	}
	if err = s.VerifyManagedOriginalDispatch(t.Context(), kernel.ManagedHostBinding{}, true); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("unissued parked proof: %v", err)
	}
	after, err := s.ManagedRecoveryAuthority(t.Context())
	if err != nil || a != after {
		t.Fatal("rejected proof mutated authority")
	}
}

func TestManagedLegacyReservationCutoverRejectsOpenPreservesHistory(t *testing.T) {
	_, db := newStoreWithDB(t)
	const table = "agent_harness_managed_recovery"
	record := map[string]any{"dispatch_id": "legacy-dispatch", "recovery_id": "legacy-recovery", "turn_id": "legacy-turn", "record_json": "{}", "closure_hash": ""}
	if err := db.Table(table).Create(record).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := harnesspostgres.New(db); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("open reservation reopened: %v", err)
	}
	if err := harnesspostgres.Migrate(db); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("open reservation migrated: %v", err)
	}
	var got string
	if err := db.Table(table).Select("record_json").Where("dispatch_id = ?", "legacy-dispatch").Scan(&got).Error; err != nil || got != "{}" {
		t.Fatalf("migration mutated old record: %q %v", got, err)
	}
	// Fixture represents a fully drained old writer, not new-writer repair.
	if err := db.Table(table).Where("dispatch_id = ?", "legacy-dispatch").Update("closure_hash", "historical-closure").Error; err != nil {
		t.Fatal(err)
	}
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := harnesspostgres.New(db); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := db.Table(table).Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("historical row lost: %d %v", n, err)
	}
}
