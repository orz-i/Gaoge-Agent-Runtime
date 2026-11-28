package postgres

import (
	"context"
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
		return conformance.ManagedRecoveryStoreFixture{Store: &managedProjectionStore{KernelStore: first, t: t}, Peer: &managedProjectionStore{KernelStore: peer, t: t},
			AdvanceTo: func(target time.Time) {
				if delay := time.Until(target) + 50*time.Millisecond; delay > 0 {
					time.Sleep(delay)
				}
			},
			Reopen: func() kernel.Store {
				closeAll()
				fresh := open()
				if strings.HasSuffix(t.Name(), "/transaction-ack/cancel") {
					// Reconstruct the legacy index with a conflicting revision; rollback must preserve it.
					for _, q := range []string{
						"CREATE TABLE agent_kernel_managed_transfers (run_id text PRIMARY KEY, suspension_id text NOT NULL UNIQUE, recovery_id text NOT NULL UNIQUE, revision bigint NOT NULL, payload text NOT NULL)",
						"INSERT INTO agent_kernel_managed_transfers SELECT run_id,suspension_id,recovery_id,revision+1,payload FROM agent_kernel_managed_history",
					} {
						if e := fresh.db.Exec(q).Error; e != nil {
							t.Fatal(e)
						}
					}
					if e := Migrate(fresh.db); e == nil {
						t.Fatal("conflicting index accepted")
					}
					var mismatch int64
					if e := fresh.db.Raw("SELECT count(*) FROM agent_kernel_managed_transfers c JOIN agent_kernel_managed_history h USING (suspension_id) WHERE c.revision=h.revision+1 AND c.payload=h.payload").Scan(&mismatch).Error; e != nil || mismatch != 1 {
						t.Fatalf("failed migration changed source: %d %v", mismatch, e)
					}
					if e := fresh.db.Exec("UPDATE agent_kernel_managed_transfers SET revision=revision-1").Error; e != nil {
						t.Fatal(e)
					}
					if e := Migrate(fresh.db); e != nil {
						t.Fatal(e)
					}
				}
				if fresh.db.Migrator().HasTable("agent_kernel_managed_transfers") {
					t.Fatal("retired current table remains")
				}

				a, e := fresh.ManagedRecoveryAuthority(t.Context())
				if e != nil || a != authority {
					t.Fatalf("reopen changed authority: %v", e)
				}
				return &managedProjectionStore{KernelStore: fresh, t: t}
			},
		}
	})
}

// All directed ENABLE calls (including exact ACK replay and losing claimants)
// and entry calls must preserve the full sealed source, not just revision.
// The suite does not concurrently mutate feature state at these boundaries.
type managedProjectionStore struct {
	*KernelStore
	t *testing.T
}

func (s *managedProjectionStore) BeginPhysicalExecution(ctx context.Context, p kernel.PhysicalAdmission) (kernel.PhysicalAdmissionResult, error) {
	if !p.IsManagedRecovery() {
		return s.KernelStore.BeginPhysicalExecution(ctx, p)
	}
	id := p.Candidate().Grant.ChildRunID
	before, e := s.Load(ctx, id)
	if e != nil {
		return kernel.PhysicalAdmissionResult{}, e
	}
	out, callErr := s.KernelStore.BeginPhysicalExecution(ctx, p)
	after, e := s.Load(ctx, id)
	if e != nil || kernel.ManagedDigest(before) != kernel.ManagedDigest(after) {
		s.t.Errorf("native admission changed sealed aggregate: %v", e)
	}
	return out, callErr
}
func (s *managedProjectionStore) CheckOwnedPhysicalEntry(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, expected kernel.Snapshot) (kernel.OwnedPhysicalEntryWindow, error) {
	before, e := s.Load(ctx, g.ChildRunID)
	if e != nil {
		s.t.Errorf("load before entry: %v", e)
		return kernel.OwnedPhysicalEntryWindow{}, e
	}
	out, callErr := s.KernelStore.CheckOwnedPhysicalEntry(ctx, g, l, expected)
	after, e := s.Load(ctx, g.ChildRunID)
	if e != nil || kernel.ManagedDigest(before) != kernel.ManagedDigest(after) {
		s.t.Errorf("entry changed original aggregate: %v", e)
	}
	return out, callErr
}
