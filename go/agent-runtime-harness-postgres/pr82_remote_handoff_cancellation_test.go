package harnesspostgres_test

import (
	"os"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness/conformance"
)

func assertPR82RemoteCancellationOnStore(t *testing.T, store harness.Store, role, rootCancel, shadow, matchingLocalCollision bool) {
	t.Helper()
	conformance.RemoteHandoffCancellation(t, store, conformance.RemoteCancellationCase{
		MemberRevision: role, CancelRoot: rootCancel, Shadow: shadow, LocalCollision: matchingLocalCollision,
	}, nil)
}

func TestPR82RemoteHandoffCancellationUsesDurableProofAcrossStores(t *testing.T) {
	for name, create := range rootInvocationStores() {
		t.Run(name, func(t *testing.T) {
			for _, role := range []bool{false, true} {
				for _, rootCancel := range []bool{false, true} {
					for _, shadow := range []bool{false, true} {
						mode := "member"
						if role {
							mode = "role"
						}
						if rootCancel {
							mode += "-turn"
						} else {
							mode += "-child"
						}
						if shadow {
							mode += "-a2a-shadow"
						}
						t.Run(mode, func(t *testing.T) {
							assertPR82RemoteCancellationOnStore(t, create(t), role, rootCancel, shadow, false)
						})
					}
				}
			}
		})
	}
}

func TestRealPostgresPR82RemoteHandoffCancellationRequiresReceipt(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	for _, role := range []bool{false, true} {
		for _, rootCancel := range []bool{false, true} {
			for _, shadow := range []bool{false, true} {
				db, _ := openIsolatedHarnessPostgres(t, dsn)
				if err := harnesspostgres.Migrate(db); err != nil {
					t.Fatal(err)
				}
				store, err := harnesspostgres.New(db)
				if err != nil {
					t.Fatal(err)
				}
				assertPR82RemoteCancellationOnStore(t, store, role, rootCancel, shadow, false)
			}
		}
	}
}

// The exact same run/task identity in the Harness-local Kernel must not be
// treated as a route-owned cancel receipt. Exercise the persisted grant and
// cancellation intent on both Memory and SQL Store compositions.
func TestPR82MatchingLocalRunCollisionFencedAcrossStores(t *testing.T) {
	for name, create := range rootInvocationStores() {
		t.Run(name, func(t *testing.T) {
			for _, role := range []bool{false, true} {
				for _, rootCancel := range []bool{false, true} {
					mode := "member-child"
					if role {
						mode = "role-child"
					}
					if rootCancel {
						mode += "-root"
					}
					t.Run(mode, func(t *testing.T) {
						assertPR82RemoteCancellationOnStore(t, create(t), role, rootCancel, false, true)
					})
				}
			}
		})
	}
}

func TestRealPostgresPR82MatchingLocalRunCollisionRemainsUnverified(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	for _, role := range []bool{false, true} {
		for _, rootCancel := range []bool{false, true} {
			db, _ := openIsolatedHarnessPostgres(t, dsn)
			if err := harnesspostgres.Migrate(db); err != nil {
				t.Fatal(err)
			}
			store, err := harnesspostgres.New(db)
			if err != nil {
				t.Fatal(err)
			}
			assertPR82RemoteCancellationOnStore(t, store, role, rootCancel, false, true)
		}
	}
}
