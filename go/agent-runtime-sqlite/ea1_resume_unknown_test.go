package sqlite_test

import (
	"path/filepath"
	"testing"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestEA1ResumeUnknownNativeSQLite(t *testing.T) {
	conformance.RunEA1ResumeUnknownSuite(t, func(t *testing.T) (kernel.Store, func() kernel.Store) {
		path := filepath.Join(privateDir(t), "resume-unknown.sqlite")
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
