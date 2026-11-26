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
)

func TestNoEntryCancellationGORM(t *testing.T) {
	conformance.NoEntryCancellation(t, func(t *testing.T) (kernel.Store, harness.Store, func() (kernel.Store, harness.Store)) {
		host, db := newStoreWithDB(t)
		if e := kernelpostgres.Migrate(db); e != nil {
			t.Fatal(e)
		}
		return kernelpostgres.NewKernelStore(db), host, func() (kernel.Store, harness.Store) {
			fresh, e := harnesspostgres.New(db)
			if e != nil {
				t.Fatal(e)
			}
			return kernelpostgres.NewKernelStore(db), fresh
		}
	})
}

func TestRealPostgresNoEntryCancellation(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	conformance.NoEntryCancellation(t, func(t *testing.T) (kernel.Store, harness.Store, func() (kernel.Store, harness.Store)) {
		db, isolated := openIsolatedHarnessPostgres(t, dsn)
		if e := kernelpostgres.Migrate(db); e != nil {
			t.Fatal(e)
		}
		if e := harnesspostgres.Migrate(db); e != nil {
			t.Fatal(e)
		}
		host, e := harnesspostgres.New(db)
		if e != nil {
			t.Fatal(e)
		}
		return kernelpostgres.NewKernelStore(db), host, func() (kernel.Store, harness.Store) {
			closeToolRecoveryDB(t, db)
			fresh := reopenToolRecoveryDB(t, isolated)
			h, e := harnesspostgres.New(fresh)
			if e != nil {
				t.Fatal(e)
			}
			return kernelpostgres.NewKernelStore(fresh), h
		}
	})
}
