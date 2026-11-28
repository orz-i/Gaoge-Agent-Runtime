package postgres

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
)

func gormEA1Serial(t *testing.T) *gorm.DB {
	t.Helper()
	db := openKernelStoreTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	// Auxiliary GORM SQLite uses one connection for these transaction
	// races. Native PostgreSQL tests exercise independent pools.
	sqlDB.SetMaxOpenConns(1)
	return db
}
func gormEA1Pair(t *testing.T) (kernel.Store, kernel.Store) {
	s := NewKernelStore(openKernelStoreTestDB(t))
	return s, s
}
func gormEA1SerialPair(t *testing.T) (kernel.Store, kernel.Store) {
	s := NewKernelStore(gormEA1Serial(t))
	return s, s
}
func gormEA1SingleReopen(t *testing.T) (kernel.Store, func() kernel.Store) {
	db := openKernelStoreTestDB(t)
	return NewKernelStore(db), func() kernel.Store { return NewKernelStore(db) }
}
func gormEA1ReopenPair(t *testing.T) (kernel.Store, func() (kernel.Store, kernel.Store)) {
	db := gormEA1Serial(t)
	return NewKernelStore(db), func() (kernel.Store, kernel.Store) {
		return NewKernelStore(db), NewKernelStore(db)
	}
}
func gormEA1PeerReopen(t *testing.T) (kernel.Store, kernel.Store, func() kernel.Store) {
	db := openKernelStoreTestDB(t)
	return NewKernelStore(db), NewKernelStore(db), func() kernel.Store { return NewKernelStore(db) }
}

func TestEA1ChildAgentApprovalSettlementGORM(t *testing.T) {
	conformance.RunEA1ChildAgentApprovalSettlement(t, gormEA1ReopenPair)
}

func TestEA1ChildAgentCancelPhasesGORM(t *testing.T) {
	conformance.RunEA1ChildAgentCancelPhases(t, gormEA1ReopenPair)
}

func TestEA1ChildAgentControlPhasesGORM(t *testing.T) {
	conformance.RunEA1ChildAgentControlPhases(t, gormEA1PeerReopen)
}

func TestEA1ChildAgentRecoveryPhasesGORM(t *testing.T) {
	conformance.RunEA1ChildAgentRecoveryPhases(t, gormEA1ReopenPair)
}

func TestEA1ChildAgentRecoveryUnknownGORM(t *testing.T) {
	conformance.RunEA1ChildAgentRecoveryUnknownSuite(t, gormEA1SingleReopen)
}

func TestEA1ChildAgentRecoveryAdmissionGORM(t *testing.T) {
	conformance.RunEA1ChildAgentRecoveryAdmissionSuite(t, gormEA1SingleReopen)
}

func TestEA1ChildAgentResumePhasesGORM(t *testing.T) {
	conformance.RunEA1ChildAgentResumePhases(t, gormEA1PeerReopen)
}

func TestEA1ChildAgentResumeSettlementGORM(t *testing.T) {
	conformance.RunEA1ChildAgentResumeSettlement(t, gormEA1ReopenPair)
}

func TestEA1ChildAgentStartPhasesGORM(t *testing.T) {
	conformance.RunEA1ChildAgentStartPhases(t, gormEA1PeerReopen)
}

func TestEA1MiddlewareNextLifetimeGORM(t *testing.T) {
	conformance.RunEA1MiddlewareNextLifetime(t, gormEA1PeerReopen)
}

func TestEA1OwnedCallbackReturnGORM(t *testing.T) {
	conformance.RunEA1OwnedCallbackReturn(t, gormEA1Pair)
}

func TestEA1RootAgentAdmissionGORM(t *testing.T) {
	conformance.RunEA1RootAgentAdmission(t, gormEA1SerialPair)
}

func TestEA1RootAgentDefaultAdmissionGORM(t *testing.T) {
	conformance.RunEA1RootAgentDefaultAdmission(t, gormEA1SerialPair)
}

func TestEA1RootAgentCancelPhasesGORM(t *testing.T) {
	conformance.RunEA1RootAgentCancelPhases(t, gormEA1PeerReopen)
}

func TestEA1RootAgentControlPhasesGORM(t *testing.T) {
	conformance.RunEA1RootAgentControlPhases(t, gormEA1PeerReopen)
}

func TestEA1RootAgentControlSettlementGORM(t *testing.T) {
	conformance.RunEA1RootAgentControlSettlementSuite(t, gormEA1PeerReopen)
}

func TestEA1RootAgentElectionGORM(t *testing.T) {
	conformance.RunEA1RootAgentElectionSuite(t, gormEA1PeerReopen)
}

func TestEA1RootAgentReconcilePhasesGORM(t *testing.T) {
	conformance.RunEA1RootAgentReconcilePhases(t, gormEA1PeerReopen)
}

func TestEA1RootAgentResumePhasesGORM(t *testing.T) {
	conformance.RunEA1RootAgentResumePhases(t, gormEA1PeerReopen)
}

func TestPhysicalAdmissionACKGORM(t *testing.T) {
	conformance.RunPhysicalAdmissionACKSuite(t, gormEA1PeerReopen)
}

func TestEA1RootAgentResumeSettlementGORM(t *testing.T) {
	conformance.RunEA1RootAgentResumeSettlementSuite(t, gormEA1SingleReopen)
}

func TestEA1RootAgentStartSettlementGORM(t *testing.T) {
	conformance.RunEA1RootAgentStartSettlementSuite(t, gormEA1SingleReopen)
}

func TestEA1RootAgentUnknownGORM(t *testing.T) {
	conformance.RunEA1RootAgentUnknownSuite(t, gormEA1SingleReopen)
}
