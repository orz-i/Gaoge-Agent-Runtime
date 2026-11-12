package sqlite_test

import (
	"path/filepath"
	"testing"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestEA1ChildAgentRecoveryUnknownNativeSQLite(t *testing.T) {
	conformance.RunEA1ChildAgentRecoveryUnknownSuite(t, func(t *testing.T) (kernel.Store, func() kernel.Store) {
		path := filepath.Join(privateDir(t), "root-agent-unknown.sqlite")
		first, err := sqlite.CreateNew(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = first.Close() })
		return first, func() kernel.Store {
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := sqlite.OpenExisting(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			return reopened
		}
	})
}

func TestEA1ChildAgentRecoveryAdmissionNativeSQLite(t *testing.T) {
	conformance.RunEA1ChildAgentRecoveryAdmissionSuite(t, func(t *testing.T) (kernel.Store, func() kernel.Store) {
		path := filepath.Join(privateDir(t), "root-agent-unknown.sqlite")
		first, err := sqlite.CreateNew(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = first.Close() })
		return first, func() kernel.Store {
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := sqlite.OpenExisting(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			return reopened
		}
	})
}
