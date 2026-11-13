package postgres

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestEA1RootAgentAdmissionGORM(t *testing.T) {
	conformance.RunEA1RootAgentAdmission(t, func(t *testing.T) (kernel.Store, kernel.Store) {
		db := openKernelStoreTestDB(t)
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		sqlDB.SetMaxOpenConns(1)
		s := NewKernelStore(db)
		return s, s
	})
}

func TestEA1RootAgentDefaultAdmissionGORM(t *testing.T) {
	conformance.RunEA1RootAgentDefaultAdmission(t, func(t *testing.T) (kernel.Store, kernel.Store) {
		db := openKernelStoreTestDB(t)
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		sqlDB.SetMaxOpenConns(1)
		s := NewKernelStore(db)
		return s, s
	})
}
