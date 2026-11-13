package postgres

import (
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Real server clock, separate pools, isolated schema. No expiry-row rewrites.
func TestRealPostgresManagedRecoveryStore(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	conformance.RunManagedRecoveryStoreSuite(t, func(t *testing.T) conformance.ManagedRecoveryStoreFixture {
		db, isolated := openIsolatedRealPostgres(t, dsn)
		if e := Migrate(db); e != nil {
			t.Fatal(e)
		}
		var pools []*sql.DB
		track := func(db *gorm.DB) *KernelStore {
			pool, e := db.DB()
			if e != nil {
				t.Fatal(e)
			}
			pools = append(pools, pool)
			return NewKernelStore(db)
		}
		open := func() *KernelStore {
			fresh, e := gorm.Open(pgdriver.Open(isolated), realPostgresTestConfig())
			if e != nil {
				t.Fatal(e)
			}
			return track(fresh)
		}
		closeAll := func() {
			for _, p := range pools {
				if e := p.Close(); e != nil {
					t.Fatal(e)
				}
			}
			pools = nil
		}
		t.Cleanup(closeAll)
		first, peer := track(db), open()
		authority, e := first.ManagedRecoveryAuthority(t.Context())
		if e != nil {
			t.Fatal(e)
		}
		pa, e := peer.ManagedRecoveryAuthority(t.Context())
		if e != nil || pa != authority {
			t.Fatalf("independent pool changed authority: %v", e)
		}
		return conformance.ManagedRecoveryStoreFixture{Store: first, Peer: peer,
			AdvanceTo: func(target time.Time) {
				if delay := time.Until(target) + 50*time.Millisecond; delay > 0 {
					time.Sleep(delay)
				}
			},
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
