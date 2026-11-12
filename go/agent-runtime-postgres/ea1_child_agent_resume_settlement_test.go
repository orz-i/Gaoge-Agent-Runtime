package postgres

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestEA1ChildAgentResumeSettlementGORM(t *testing.T) {
	conformance.RunEA1ChildAgentResumeSettlement(t, func(t *testing.T) (kernel.Store, func() (kernel.Store, kernel.Store)) {
		db := openKernelStoreTestDB(t)
		// This auxiliary GORM-SQLite dialect serializes SQL transactions;
		// both actual runners still capture the Grant before the Finish barrier.
		// Independent connection competition is certified by native SQLite/PG.
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		sqlDB.SetMaxOpenConns(1)
		return NewKernelStore(db), func() (kernel.Store, kernel.Store) { return NewKernelStore(db), NewKernelStore(db) }
	})
}
