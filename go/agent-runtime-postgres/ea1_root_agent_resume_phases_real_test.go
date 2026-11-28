package postgres

import (
	"os"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type postgresEA1Fixture string

func realEA1(t *testing.T) postgresEA1Fixture {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	return postgresEA1Fixture(dsn)
}

// Migrate a separate database schema per conformance case. Each peer or
// recovery store uses a new pool bound to the same isolated schema.
func (f postgresEA1Fixture) store(t *testing.T) (kernel.Store, func() kernel.Store) {
	t.Helper()
	db, isolated := openIsolatedRealPostgres(t, string(f))
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	return NewKernelStore(db), func() kernel.Store {
		t.Helper()
		fresh, err := gorm.Open(pgdriver.Open(isolated), realPostgresTestConfig())
		if err != nil {
			t.Fatal(err)
		}
		sqlDB, err := fresh.DB()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sqlDB.Close() })
		return NewKernelStore(fresh)
	}
}
func (f postgresEA1Fixture) pair(t *testing.T) (kernel.Store, kernel.Store) {
	first, open := f.store(t)
	return first, open()
}
func (f postgresEA1Fixture) singleReopen(t *testing.T) (kernel.Store, func() kernel.Store) {
	return f.store(t)
}
func (f postgresEA1Fixture) reopenPair(t *testing.T) (kernel.Store, func() (kernel.Store, kernel.Store)) {
	first, open := f.store(t)
	return first, func() (kernel.Store, kernel.Store) { return open(), open() }
}
func (f postgresEA1Fixture) peerReopen(t *testing.T) (kernel.Store, kernel.Store, func() kernel.Store) {
	first, open := f.store(t)
	return first, open(), open
}

func TestEA1RealPostgresChildAgentApprovalSettlement(t *testing.T) {
	conformance.RunEA1ChildAgentApprovalSettlement(t, realEA1(t).reopenPair)
}

func TestEA1RealPostgresChildAgentCancelPhases(t *testing.T) {
	conformance.RunEA1ChildAgentCancelPhases(t, realEA1(t).reopenPair)
}

func TestEA1RealPostgresChildAgentControlPhases(t *testing.T) {
	conformance.RunEA1ChildAgentControlPhases(t, realEA1(t).peerReopen)
}

func TestEA1RealPostgresChildAgentRecoveryPhases(t *testing.T) {
	conformance.RunEA1ChildAgentRecoveryPhases(t, realEA1(t).reopenPair)
}

func TestEA1RealPostgresChildAgentRecoveryUnknown(t *testing.T) {
	conformance.RunEA1ChildAgentRecoveryUnknownSuite(t, realEA1(t).singleReopen)
}

func TestEA1RealPostgresChildAgentRecoveryAdmission(t *testing.T) {
	conformance.RunEA1ChildAgentRecoveryAdmissionSuite(t, realEA1(t).singleReopen)
}

func TestEA1RealPostgresChildAgentResumePhases(t *testing.T) {
	conformance.RunEA1ChildAgentResumePhases(t, realEA1(t).peerReopen)
}

func TestEA1RealPostgresChildAgentResumeSettlement(t *testing.T) {
	conformance.RunEA1ChildAgentResumeSettlement(t, realEA1(t).reopenPair)
}

func TestEA1RealPostgresChildAgentStartPhases(t *testing.T) {
	conformance.RunEA1ChildAgentStartPhases(t, realEA1(t).peerReopen)
}

func TestEA1RealPostgresOwnedCallbackReturn(t *testing.T) {
	conformance.RunEA1OwnedCallbackReturn(t, realEA1(t).pair)
}

func TestEA1RealPostgresRootAgentAdmission(t *testing.T) {
	conformance.RunEA1RootAgentAdmission(t, realEA1(t).pair)
}

func TestEA1RealPostgresRootAgentDefaultAdmission(t *testing.T) {
	conformance.RunEA1RootAgentDefaultAdmission(t, realEA1(t).pair)
}

func TestEA1RealPostgresRootAgentCancelPhases(t *testing.T) {
	conformance.RunEA1RootAgentCancelPhases(t, realEA1(t).peerReopen)
}

func TestEA1RealPostgresRootAgentControlPhases(t *testing.T) {
	conformance.RunEA1RootAgentControlPhases(t, realEA1(t).peerReopen)
}

func TestEA1RealPostgresRootAgentControlSettlement(t *testing.T) {
	conformance.RunEA1RootAgentControlSettlementSuite(t, realEA1(t).peerReopen)
}

func TestEA1RealPostgresRootAgentElection(t *testing.T) {
	conformance.RunEA1RootAgentElectionSuite(t, realEA1(t).peerReopen)
}

func TestEA1RealPostgresRootAgentReconcilePhases(t *testing.T) {
	conformance.RunEA1RootAgentReconcilePhases(t, realEA1(t).peerReopen)
}

func TestEA1RealPostgresRootAgentResumePhases(t *testing.T) {
	conformance.RunEA1RootAgentResumePhases(t, realEA1(t).peerReopen)
}

func TestRealPostgresPhysicalAdmissionACK(t *testing.T) {
	conformance.RunPhysicalAdmissionACKSuite(t, realEA1(t).peerReopen)
}

func TestEA1RealPostgresRootAgentResumeSettlement(t *testing.T) {
	conformance.RunEA1RootAgentResumeSettlementSuite(t, realEA1(t).singleReopen)
}

func TestEA1RealPostgresRootAgentStartSettlement(t *testing.T) {
	conformance.RunEA1RootAgentStartSettlementSuite(t, realEA1(t).singleReopen)
}

func TestEA1RealPostgresRootAgentUnknown(t *testing.T) {
	conformance.RunEA1RootAgentUnknownSuite(t, realEA1(t).singleReopen)
}
