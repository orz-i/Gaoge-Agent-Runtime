package postgres

import (
	"os"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// The GORM conformance adapter also runs against SQLite; exercise the exact
// Workflow Effect/Wait physical grant and terminal-parent CAS on native
// PostgreSQL 16, using a new isolated schema for each independent scenario.
func TestIssue107RealPostgresParentedWorkflowPhysicalGrantSuite(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	conformance.RunParentedWorkflowSuite(t, func(child *testing.T) kernel.Store {
		db, _ := openIsolatedRealPostgres(child, dsn)
		if err := Migrate(db); err != nil {
			child.Fatal(err)
		}
		return NewKernelStore(db)
	})
}

func TestIssue107RealPostgresRootWorkflowPhysicalGrantConformance(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	conformance.RunRootWorkflowSuite(t, func(child *testing.T) kernel.Store {
		db, _ := openIsolatedRealPostgres(child, dsn)
		if err := Migrate(db); err != nil {
			child.Fatal(err)
		}
		return NewKernelStore(db)
	})
}
