package harnesspostgres_test

import (
	"os"
	"strings"
	"testing"

	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	kernelpostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func reopenToolRecoveryDB(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.Open(dsn), realHarnessPostgresTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return db
}
func closeToolRecoveryDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err = pool.Close(); err != nil {
		t.Fatal(err)
	}
}

// Exercise the shared Call/approval matrix with independent real Kernel and
// Harness ledger pools, not a Memory ledger paired with a PostgreSQL Kernel.
func TestRealPostgresToolApprovalAndLedgerReopen(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	conformance.RunSettledToolRecovery(t, func(t *testing.T) (kernel.Store, kernel.Store, func() kernel.Store) {
		db, isolated := openIsolatedHarnessPostgres(t, dsn)
		if err := kernelpostgres.Migrate(db); err != nil {
			t.Fatal(err)
		}
		peer := reopenToolRecoveryDB(t, isolated)
		return kernelpostgres.NewKernelStore(db), kernelpostgres.NewKernelStore(peer), func() kernel.Store {
			closeToolRecoveryDB(t, db)
			closeToolRecoveryDB(t, peer)
			return kernelpostgres.NewKernelStore(reopenToolRecoveryDB(t, isolated))
		}
	}, func(t *testing.T) (budget.LedgerStore, func() budget.LedgerStore) {
		db, isolated := openIsolatedHarnessPostgres(t, dsn)
		if err := harnesspostgres.Migrate(db); err != nil {
			t.Fatal(err)
		}
		original, err := harnesspostgres.New(db)
		if err != nil {
			t.Fatal(err)
		}
		return original, func() budget.LedgerStore {
			closeToolRecoveryDB(t, db)
			fresh, err := harnesspostgres.New(reopenToolRecoveryDB(t, isolated))
			if err != nil {
				t.Fatal(err)
			}
			return fresh
		}
	})
}
