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

func TestEA1RealPostgresRootAgentAdmission(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	conformance.RunEA1RootAgentAdmission(t, func(t *testing.T) (kernel.Store, kernel.Store) {
		db, isolated := openIsolatedRealPostgres(t, dsn)
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
		peer, err := gorm.Open(pgdriver.Open(isolated), realPostgresTestConfig())
		if err != nil {
			t.Fatal(err)
		}
		sqlDB, err := peer.DB()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sqlDB.Close() })
		return NewKernelStore(db), NewKernelStore(peer)
	})
}
