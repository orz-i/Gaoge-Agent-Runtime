package harnesssqlite_test

import (
	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesssqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness/conformance"
	kernelsqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile/testprofile"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"path/filepath"
	"testing"
)

func TestNativeAuthorityCutoverSQLite(t *testing.T) {
	conformance.NativeAuthorityCutover(t, func(t *testing.T) conformance.NativeAuthorityFixture {
		ctx := t.Context()
		dir := testprofile.Dir(t)
		hp, kp := filepath.Join(dir, "host.sqlite"), filepath.Join(dir, "kernel.sqlite")
		h, e := harnesssqlite.CreateNew(ctx, hp)
		if e != nil {
			t.Fatal(e)
		}
		k, e := kernelsqlite.CreateNew(ctx, kp)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = h.Close(); _ = k.Close() })
		reopen := func() (kernel.Store, harness.Store, runrelation.Store) {
			if e := h.Close(); e != nil {
				t.Fatal(e)
			}
			if e := k.Close(); e != nil {
				t.Fatal(e)
			}
			h, e = harnesssqlite.OpenExisting(ctx, hp)
			if e != nil {
				t.Fatal(e)
			}
			k, e = kernelsqlite.OpenExisting(ctx, kp)
			if e != nil {
				t.Fatal(e)
			}
			return k, h, kernelsqlite.NewRunRelationStore(k)
		}
		return conformance.NativeAuthorityFixture{Kernel: k, Host: h, Relations: kernelsqlite.NewRunRelationStore(k), Reopen: reopen}
	})
}
