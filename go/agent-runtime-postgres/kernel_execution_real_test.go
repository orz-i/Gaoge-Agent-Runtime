package postgres

import (
	"os"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Requires TEST_POSTGRES_DSN: unlike the regular adapter Conformance (which
// uses GORM SQLite), this exercises row locks and DB clock_timestamp() on an
// actual PostgreSQL engine in an isolated test schema.
func TestRealPostgresKernelExecutionStoreConformance(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured: real PostgreSQL Stage 2 test skipped")
	}
	conformance.RunKernelExecutionSuite(t, func(child *testing.T) kernel.Store {
		db, _ := openIsolatedRealPostgres(child, dsn)
		if err := Migrate(db); err != nil {
			child.Fatal(err)
		}
		return NewKernelStore(db)
	})
}
