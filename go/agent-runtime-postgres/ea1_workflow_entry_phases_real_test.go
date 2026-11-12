package postgres

import (
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"os"
	"strings"
	"testing"
)

func TestEA1RealPostgresWorkflowEntryPhases(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	factory := func(sub *testing.T) kernel.Store {
		db, _ := openIsolatedRealPostgres(sub, dsn)
		if err := Migrate(db); err != nil {
			sub.Fatal(err)
		}
		return NewKernelStore(db)
	}
	t.Run("RootWaitP0", func(t *testing.T) { conformance.RunEA1RootWaitP0(t, factory) })
	t.Run("ResumeCompensationP3", func(t *testing.T) { conformance.RunEA1WorkflowResumeCompensationP3(t, factory) })
}
