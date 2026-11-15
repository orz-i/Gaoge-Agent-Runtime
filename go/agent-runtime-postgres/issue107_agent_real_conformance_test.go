package postgres

import (
	"os"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestAuditIssue107NativePostgresChildSelfPhysicalGrantFence(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	conformance.RunAuditPhysicalChildSelfFence(t, func(child *testing.T) kernel.Store {
		db, _ := openIsolatedRealPostgres(child, dsn)
		if err := Migrate(db); err != nil {
			child.Fatal(err)
		}
		return NewKernelStore(db)
	})
}
func TestEA1RealPostgresP0SourceAdmission(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	conformance.RunEA1P0SourceAdmissionSuite(t, func(sub *testing.T) kernel.Store {
		db, _ := openIsolatedRealPostgres(sub, dsn)
		if err := Migrate(db); err != nil {
			sub.Fatal(err)
		}
		return NewKernelStore(db)
	})
}
