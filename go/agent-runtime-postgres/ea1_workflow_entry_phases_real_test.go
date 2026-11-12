package postgres

import (
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
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
	t.Run("RootWaitP0", func(t *testing.T) {
		db, isolatedDSN := openIsolatedRealPostgres(t, dsn)
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
		peer, err := gorm.Open(pgdriver.Open(isolatedDSN), realPostgresTestConfig())
		if err != nil {
			t.Fatal(err)
		}
		peerSQL, err := peer.DB()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = peerSQL.Close() })
		conformance.RunEA1RootWaitP0AcrossStores(t, NewKernelStore(db), NewKernelStore(peer))
	})
	t.Run("ResumeCompensationP3", func(t *testing.T) { conformance.RunEA1WorkflowResumeCompensationP3(t, factory) })
}
