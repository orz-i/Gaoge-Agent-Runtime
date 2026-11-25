package postgres

import (
	"os"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRelationalReconciliationGORM(t *testing.T) {
	db := openKernelStoreTestDB(t)
	conformance.RunRelationalReconciliationSuite(t, NewKernelStore(db), NewRunRelationStore(db), func() runrelation.Store {
		return NewRunRelationStore(db)
	})
}

func TestRealPostgresRelationalReconciliation(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	db, isolated := openIsolatedRealPostgres(t, dsn)
	if e := Migrate(db); e != nil {
		t.Fatal(e)
	}
	conformance.RunRelationalReconciliationSuite(t, NewKernelStore(db), NewRunRelationStore(db), func() runrelation.Store {
		peer, e := gorm.Open(pgdriver.Open(isolated), realPostgresTestConfig())
		if e != nil {
			t.Fatal(e)
		}
		pool, e := peer.DB()
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = pool.Close() })
		return NewRunRelationStore(peer)
	})
}
