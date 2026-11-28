package harnesspostgres_test

import (
	"os"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness/conformance"
	kernelpostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRealPostgresNativeAuthorityCutover(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	conformance.NativeAuthorityCutover(t, func(t *testing.T) conformance.NativeAuthorityFixture {
		db, isolated := openIsolatedHarnessPostgres(t, dsn)
		if e := kernelpostgres.Migrate(db); e != nil {
			t.Fatal(e)
		}
		if e := harnesspostgres.Migrate(db); e != nil {
			t.Fatal(e)
		}
		h, e := harnesspostgres.New(db)
		if e != nil {
			t.Fatal(e)
		}
		reopen := func() (kernel.Store, harness.Store, runrelation.Store) {
			next, e := gorm.Open(postgres.Open(isolated), realHarnessPostgresTestConfig())
			if e != nil {
				t.Fatal(e)
			}
			sql, e := next.DB()
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() { _ = sql.Close() })
			host, e := harnesspostgres.New(next)
			if e != nil {
				t.Fatal(e)
			}
			return kernelpostgres.NewKernelStore(next), host, kernelpostgres.NewRunRelationStore(next)
		}
		return conformance.NativeAuthorityFixture{Kernel: kernelpostgres.NewKernelStore(db), Host: h, Relations: kernelpostgres.NewRunRelationStore(db), Reopen: reopen, SharedTable: true}
	})
}
