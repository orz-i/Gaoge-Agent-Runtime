package sqlite_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestSQLiteManagedRecoveryStore(t *testing.T) {
	conformance.RunManagedRecoveryStoreSuite(t, func(t *testing.T) conformance.ManagedRecoveryStoreFixture {
		path := filepath.Join(privateDir(t), "managed-recovery.sqlite")
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
		return conformance.ManagedRecoveryStoreFixture{Store: first, Peer: peer, RetryableContention: func(err error) bool { return errors.Is(err, sqlite.ErrBusy) },
			AdvanceTo: func(target time.Time) {
				if delay := time.Until(target) + 50*time.Millisecond; delay > 0 {
					time.Sleep(delay)
				}
			},
			// Every original connection is closed before reopening the durable file.
			Reopen: func() kernel.Store {
				closeAll()
				if strings.HasSuffix(t.Name(), "/transaction-ack/cancel") {
					// Reconstruct the exact v11 redundant index around the original committed history.
					raw, e := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
					if e != nil {
						t.Fatal(e)
					}
					_, e = raw.Exec(`DROP INDEX runtime_managed_history_run_revision;
                        CREATE TABLE runtime_managed_transfers (run_id TEXT PRIMARY KEY REFERENCES runtime_runs(run_id), suspension_id TEXT NOT NULL UNIQUE, recovery_id TEXT NOT NULL UNIQUE, revision INTEGER NOT NULL, payload_json BLOB NOT NULL);
                        INSERT INTO runtime_managed_transfers SELECT run_id,suspension_id,recovery_id,revision+1,payload_json FROM runtime_managed_history;
                        PRAGMA user_version=11`)
					raw.Close()
					if e != nil {
						t.Fatal(e)
					}
					if old, e := sqlite.OpenExisting(t.Context(), path); !errors.Is(e, sqlite.ErrUnsupportedSchema) {
						if old != nil {
							old.Close()
						}
						t.Fatalf("old profile opened without migration: %v", e)
					}
					if e := sqlite.MigrateV11ToV12(t.Context(), path); !errors.Is(e, sqlite.ErrUnsupportedSchema) {
						t.Fatalf("conflicting index accepted: %v", e)
					}
					raw, e = sql.Open("sqlite", "file:"+filepath.ToSlash(path))
					if e != nil {
						t.Fatal(e)
					}
					var version, mismatch int
					if e = raw.QueryRow("PRAGMA user_version").Scan(&version); e != nil {
						t.Fatal(e)
					}
					if e = raw.QueryRow("SELECT count(*) FROM runtime_managed_transfers c JOIN runtime_managed_history h USING (suspension_id) WHERE c.revision=h.revision+1 AND c.payload_json=h.payload_json").Scan(&mismatch); e != nil || version != 11 || mismatch != 1 {
						t.Fatalf("failed migration changed source: %d/%d %v", version, mismatch, e)
					}
					_, e = raw.Exec("UPDATE runtime_managed_transfers SET revision=revision-1")
					raw.Close()
					if e != nil {
						t.Fatal(e)
					}
					if e = sqlite.MigrateV11ToV12(t.Context(), path); e != nil {
						t.Fatal(e)
					}
				}
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
