package sqlite_test

import (
	"errors"
	"path/filepath"
	"testing"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Every conformance case gets its own private database. Recovery must close
// the original handles and reopen that same on-disk database.
func sqliteEA1Store(t *testing.T) (*sqlite.Store, func() *sqlite.Store) {
	t.Helper()
	path := filepath.Join(privateDir(t), "ea1-native.sqlite")
	first, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	return first, func() *sqlite.Store {
		t.Helper()
		fresh, err := sqlite.OpenExisting(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = fresh.Close() })
		return fresh
	}
}
func sqliteEA1Close(t *testing.T, stores ...*sqlite.Store) {
	t.Helper()
	for _, s := range stores {
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
func sqliteEA1Pair(t *testing.T) (kernel.Store, kernel.Store) {
	first, open := sqliteEA1Store(t)
	return first, open()
}
func sqliteEA1SingleReopen(t *testing.T) (kernel.Store, func() kernel.Store) {
	first, open := sqliteEA1Store(t)
	return first, func() kernel.Store {
		sqliteEA1Close(t, first)
		return open()
	}
}
func sqliteEA1ReopenPair(t *testing.T) (kernel.Store, func() (kernel.Store, kernel.Store)) {
	first, open := sqliteEA1Store(t)
	return first, func() (kernel.Store, kernel.Store) {
		sqliteEA1Close(t, first)
		return open(), open()
	}
}
func sqliteEA1PeerReopen(t *testing.T) (kernel.Store, kernel.Store, func() kernel.Store) {
	first, open := sqliteEA1Store(t)
	peer := open()
	return first, peer, func() kernel.Store {
		sqliteEA1Close(t, first, peer)
		return open()
	}
}

func TestEA1ChildAgentApprovalSettlementNativeSQLite(t *testing.T) {
	conformance.RunEA1ChildAgentApprovalSettlement(t, sqliteEA1ReopenPair)
}

func TestEA1ChildAgentCancelPhasesNativeSQLite(t *testing.T) {
	conformance.RunEA1ChildAgentCancelPhases(t, sqliteEA1ReopenPair)
}

func TestEA1ChildAgentControlPhasesNativeSQLite(t *testing.T) {
	conformance.RunEA1ChildAgentControlPhases(t, sqliteEA1PeerReopen)
}

func TestEA1ChildAgentRecoveryPhasesNativeSQLite(t *testing.T) {
	conformance.RunEA1ChildAgentRecoveryPhases(t, sqliteEA1ReopenPair, func(err error) bool { return errors.Is(err, sqlite.ErrBusy) })
}

func TestEA1ChildAgentRecoveryUnknownNativeSQLite(t *testing.T) {
	conformance.RunEA1ChildAgentRecoveryUnknownSuite(t, sqliteEA1SingleReopen)
}

func TestEA1ChildAgentRecoveryAdmissionNativeSQLite(t *testing.T) {
	conformance.RunEA1ChildAgentRecoveryAdmissionSuite(t, sqliteEA1SingleReopen)
}

func TestEA1ChildAgentResumePhasesNativeSQLite(t *testing.T) {
	conformance.RunEA1ChildAgentResumePhases(t, sqliteEA1PeerReopen)
}

func TestEA1ChildAgentResumeSettlementNativeSQLite(t *testing.T) {
	conformance.RunEA1ChildAgentResumeSettlement(t, sqliteEA1ReopenPair)
}

func TestEA1ChildAgentStartPhasesNativeSQLite(t *testing.T) {
	conformance.RunEA1ChildAgentStartPhases(t, sqliteEA1PeerReopen)
}

func TestEA1MiddlewareNextLifetimeNativeSQLite(t *testing.T) {
	conformance.RunEA1MiddlewareNextLifetime(t, sqliteEA1PeerReopen)
}

func TestEA1OwnedCallbackReturnSQLite(t *testing.T) {
	conformance.RunEA1OwnedCallbackReturn(t, sqliteEA1Pair)
}

func TestEA1RootAgentAdmissionSQLite(t *testing.T) {
	conformance.RunEA1RootAgentAdmission(t, sqliteEA1Pair)
}

func TestEA1RootAgentDefaultAdmissionSQLite(t *testing.T) {
	conformance.RunEA1RootAgentDefaultAdmission(t, sqliteEA1Pair)
}

func TestEA1RootAgentCancelPhasesNativeSQLite(t *testing.T) {
	conformance.RunEA1RootAgentCancelPhases(t, sqliteEA1PeerReopen)
}

func TestEA1RootAgentControlPhasesNativeSQLite(t *testing.T) {
	conformance.RunEA1RootAgentControlPhases(t, sqliteEA1PeerReopen)
}

func TestEA1RootAgentControlSettlementNativeSQLite(t *testing.T) {
	conformance.RunEA1RootAgentControlSettlementSuite(t, sqliteEA1PeerReopen)
}

func TestEA1RootAgentElectionNativeSQLite(t *testing.T) {
	conformance.RunEA1RootAgentElectionSuite(t, sqliteEA1PeerReopen)
}

func TestEA1RootAgentReconcilePhasesNativeSQLite(t *testing.T) {
	conformance.RunEA1RootAgentReconcilePhases(t, sqliteEA1PeerReopen)
}

func TestEA1RootAgentResumePhasesNativeSQLite(t *testing.T) {
	conformance.RunEA1RootAgentResumePhases(t, sqliteEA1PeerReopen)
}

func TestPhysicalAdmissionACKNativeSQLite(t *testing.T) {
	conformance.RunPhysicalAdmissionACKSuite(t, sqliteEA1PeerReopen)
}

func TestEA1RootAgentResumeSettlementNativeSQLite(t *testing.T) {
	conformance.RunEA1RootAgentResumeSettlementSuite(t, sqliteEA1SingleReopen)
}

func TestEA1RootAgentStartSettlementNativeSQLite(t *testing.T) {
	conformance.RunEA1RootAgentStartSettlementSuite(t, sqliteEA1SingleReopen)
}

func TestEA1RootAgentUnknownNativeSQLite(t *testing.T) {
	conformance.RunEA1RootAgentUnknownSuite(t, sqliteEA1SingleReopen)
}
