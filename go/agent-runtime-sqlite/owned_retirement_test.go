package sqlite_test

import (
	"path/filepath"
	"testing"
	"time"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestSQLiteOwnedRetirementStore(t *testing.T) {
	conformance.RunOwnedRetirementStoreSuite(t, func(t *testing.T) conformance.ManagedRecoveryStoreFixture {
		path := filepath.Join(privateDir(t), "owned-retirement.sqlite")
		first, e := sqlite.CreateNew(t.Context(), path)
		if e != nil {
			t.Fatal(e)
		}
		active := []*sqlite.Store{first}
		open := func() *sqlite.Store {
			s, e := sqlite.OpenExisting(t.Context(), path)
			if e != nil {
				t.Fatal(e)
			}
			active = append(active, s)
			return s
		}
		closeAll := func() {
			for _, s := range active {
				if e := s.Close(); e != nil {
					t.Fatal(e)
				}
			}
			active = nil
		}
		t.Cleanup(closeAll)
		peer := open()
		authority, e := first.ManagedRecoveryAuthority(t.Context())
		if e != nil {
			t.Fatal(e)
		}
		pa, e := peer.ManagedRecoveryAuthority(t.Context())
		if e != nil || pa != authority {
			t.Fatalf("independent connection changed authority: %v", e)
		}
		return conformance.ManagedRecoveryStoreFixture{Store: first, Peer: peer,
			AdvanceTo: func(target time.Time) {
				if delay := time.Until(target) + 50*time.Millisecond; delay > 0 {
					time.Sleep(delay)
				}
			},
			// Every original connection is closed before reopening the durable file.
			Reopen: func() kernel.Store {
				closeAll()
				fresh := open()
				a, e := fresh.ManagedRecoveryAuthority(t.Context())
				if e != nil || a != authority {
					t.Fatalf("reopen changed authority: %v", e)
				}
				return fresh
			},
		}
	})
}
