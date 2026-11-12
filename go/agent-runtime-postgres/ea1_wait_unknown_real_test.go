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

func TestEA1RealPostgresWaitUnknown(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	conformance.RunEA1WaitUnknownSuite(t, func(t *testing.T) (kernel.Store, func() kernel.Store) {
		db, isolatedDSN := openIsolatedRealPostgres(t, dsn)
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
		return NewKernelStore(db), func() kernel.Store {
			fresh, err := gorm.Open(pgdriver.Open(isolatedDSN), realPostgresTestConfig())
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
	})
}
