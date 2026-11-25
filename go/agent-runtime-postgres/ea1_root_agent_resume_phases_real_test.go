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

func TestEA1RealPostgresRootAgentResumePhases(t *testing.T) {
	conformance.RunEA1RootAgentResumePhases(t, realPostgresRootResumeFixture(t))
}

func TestRealPostgresPhysicalAdmissionACK(t *testing.T) {
	conformance.RunPhysicalAdmissionACKSuite(t, realPostgresRootResumeFixture(t))
}

func realPostgresRootResumeFixture(t *testing.T) func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	return func(t *testing.T) (kernel.Store, kernel.Store, func() kernel.Store) {
		db, isolated := openIsolatedRealPostgres(t, dsn)
		if e := Migrate(db); e != nil {
			t.Fatal(e)
		}
		open := func() kernel.Store {
			fresh, e := gorm.Open(pgdriver.Open(isolated), realPostgresTestConfig())
			if e != nil {
				t.Fatal(e)
			}
			sql, e := fresh.DB()
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() { _ = sql.Close() })
			return NewKernelStore(fresh)
		}
		return NewKernelStore(db), open(), open
	}
}
