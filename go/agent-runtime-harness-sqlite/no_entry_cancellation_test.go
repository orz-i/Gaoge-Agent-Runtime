package harnesssqlite_test

import (
	"path/filepath"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness/conformance"
	kernelsqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile/testprofile"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestNoEntryCancellationSQLiteReopen(t *testing.T) {
	conformance.NoEntryCancellation(t, func(t *testing.T) (kernel.Store, harness.Store, func() (kernel.Store, harness.Store)) {
		path, host := newHost(t)
		kpath := filepath.Join(testprofile.Dir(t), "kernel.sqlite")
		k, e := kernelsqlite.CreateNew(t.Context(), kpath)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = k.Close() })
		return k, host, func() (kernel.Store, harness.Store) {
			if e := host.Close(); e != nil {
				t.Fatal(e)
			}
			if e := k.Close(); e != nil {
				t.Fatal(e)
			}
			fresh, e := kernelsqlite.OpenExisting(t.Context(), kpath)
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() { _ = fresh.Close() })
			return fresh, reopenHost(t, path)
		}
	})
}
func TestOriginalDispatchReturnSQLiteReopen(t *testing.T) {
	conformance.OriginalDispatchReturn(t, func(t *testing.T) (kernel.Store, harness.Store, func() (kernel.Store, harness.Store)) {
		path, host := newHost(t)
		kpath := filepath.Join(testprofile.Dir(t), "kernel.sqlite")
		k, e := kernelsqlite.CreateNew(t.Context(), kpath)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = k.Close() })
		return k, host, func() (kernel.Store, harness.Store) {
			if e := host.Close(); e != nil {
				t.Fatal(e)
			}
			if e := k.Close(); e != nil {
				t.Fatal(e)
			}
			fresh, e := kernelsqlite.OpenExisting(t.Context(), kpath)
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() { _ = fresh.Close() })
			return fresh, reopenHost(t, path)
		}
	})
}

func TestManagedPreparationSummarySQLiteReopen(t *testing.T) {
	conformance.ManagedPreparationSummary(t, func(t *testing.T) (kernel.Store, harness.Store, func() (kernel.Store, harness.Store)) {
		path, host := newHost(t)
		kpath := filepath.Join(testprofile.Dir(t), "kernel.sqlite")
		k, e := kernelsqlite.CreateNew(t.Context(), kpath)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = k.Close() })
		return k, host, func() (kernel.Store, harness.Store) {
			if e := host.Close(); e != nil {
				t.Fatal(e)
			}
			if e := k.Close(); e != nil {
				t.Fatal(e)
			}
			fresh, e := kernelsqlite.OpenExisting(t.Context(), kpath)
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() { _ = fresh.Close() })
			return fresh, reopenHost(t, path)
		}
	})
}

func TestNativeTransitionCommitSQLiteReopen(t *testing.T) {
	conformance.NativeTransitionCommit(t, func(t *testing.T) (kernel.Store, harness.Store, func() (kernel.Store, harness.Store)) {
		path, host := newHost(t)
		kpath := filepath.Join(testprofile.Dir(t), "kernel.sqlite")
		k, e := kernelsqlite.CreateNew(t.Context(), kpath)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = k.Close() })
		return k, host, func() (kernel.Store, harness.Store) {
			if e := host.Close(); e != nil {
				t.Fatal(e)
			}
			if e := k.Close(); e != nil {
				t.Fatal(e)
			}
			fresh, e := kernelsqlite.OpenExisting(t.Context(), kpath)
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() { _ = fresh.Close() })
			return fresh, reopenHost(t, path)
		}
	})
}
