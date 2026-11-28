package sqlite

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestOwnedRetirementFinishNativeIdentity(t *testing.T) {
	for _, mode := range []string{"owner", "lease", "epoch", "cleared", "expired", "unknown-return", "foreign-return", "child-expired", "child-missing-ancestor", "child-foreign-ancestor", "child-extra-ancestor"} {
		t.Run(mode, func(t *testing.T) {
			store, e := CreateNew(t.Context(), ownedPrivatePath(t, "identity.sqlite"))
			if e != nil {
				t.Fatal(e)
			}
			defer func() {
				if err := store.Close(); err != nil {
					t.Errorf("close store: %v", err)
				}
			}()
			rt, e := kernel.New(kernel.Dependencies{Store: store})
			if e != nil {
				t.Fatal(e)
			}
			request := kernel.CreateRequest{ID: "retire-root", Kind: "agent", Actor: kernel.ActorRef{TenantID: "t", ActorID: "a"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "th"}, Goal: "original", State: []byte("{}"), ExecutionRequired: true}
			if strings.HasPrefix(mode, "child-") {
				parent := request
				parent.ID = "parent"
				if _, e := rt.Create(t.Context(), parent); e != nil {
					t.Fatal(e)
				}
				request.ParentRunID = parent.ID
			}
			snap, e := rt.Create(t.Context(), request)
			if e != nil {
				t.Fatal(e)
			}
			lease, e := store.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: snap.Run.ID, OwnerID: "original", LeaseDuration: time.Minute})
			if e != nil {
				t.Fatal(e)
			}
			g := kernel.ChildExecutionGrant{ID: "retire-grant", ChildRunID: snap.Run.ID, ExpectedRevision: snap.Run.Revision, ExecutionOwnerID: lease.OwnerID, ExecutionLeaseID: lease.LeaseID, ExecutionEpoch: lease.Epoch, AdmissionSourceHash: strings.Repeat("a", 64), AdmissionOperation: "start"}
			// Persist an exact original callback/mark fixture. A legacy mark
			// without this return is separately required to remain fenced.
			c := kernel.PhysicalCallbackCompletion{GrantID: g.ID, ReturnRevision: snap.Run.Revision, ReturnSourceHash: strings.Repeat("b", 64), HostCompleted: true, LeaseOwned: true}
			if snap.Run.ParentRunID != "" {
				c.ReturnSourceHash = kernel.ManagedDigest(kernel.PhysicalCallbackReturnPayload(snap, kernel.PhysicalCallbackOutcome{HostCompleted: true, LeaseOwned: true}))
			}
			_, e = store.db.Exec("UPDATE runtime_runs SET execution_physical_epoch=? WHERE run_id=?", g.ExecutionEpoch, g.ChildRunID)
			if e != nil {
				t.Fatal(e)
			}
			seedPhysicalFixture(t, store, g)
			if snap.Run.ParentRunID != "" {
				if _, e := store.db.Exec("INSERT INTO runtime_effect_owners(grant_id,parent_run_id) VALUES(?,?)", g.ID, snap.Run.ParentRunID); e != nil {
					t.Fatal(e)
				}
				query := ""
				switch mode {
				case "child-missing-ancestor":
					query = "DELETE FROM runtime_effect_owners WHERE parent_run_id='parent'"
				case "child-foreign-ancestor":
					query = "UPDATE runtime_effect_owners SET parent_run_id='foreign' WHERE parent_run_id='parent'"
				case "child-extra-ancestor":
					query = "INSERT INTO runtime_effect_owners(grant_id,parent_run_id) VALUES('retire-grant','foreign')"
				}
				if query != "" {
					if _, e := store.db.Exec(query); e != nil {
						t.Fatal(e)
					}
				}
			}
			if mode != "unknown-return" {
				sourceHash := c.ReturnSourceHash
				if mode == "foreign-return" {
					sourceHash = "invalid-original-return"
				}
				if _, e = store.db.Exec("UPDATE runtime_effects SET callback_return_revision=?,callback_return_source_hash=?,callback_host_completed=1,callback_lease_owned=1 WHERE grant_id=?", c.ReturnRevision, sourceHash, g.ID); e != nil {
					t.Fatal(e)
				}
			}
			query := ""
			switch mode {
			case "owner":
				query = "execution_owner='other'"
			case "lease":
				query = "execution_lease_id='other'"
			case "epoch":
				query = "execution_epoch=execution_epoch+1"
			case "cleared":
				query = "execution_owner='',execution_lease_id='',execution_expiry_ms=0"
			case "expired":
				query = "execution_expiry_ms=1"
			case "unknown-return", "foreign-return", "child-expired", "child-missing-ancestor", "child-foreign-ancestor", "child-extra-ancestor":
				query = "execution_expiry_ms=1"
			}
			if _, e = store.db.Exec("UPDATE runtime_runs SET "+query+" WHERE run_id=?", g.ChildRunID); e != nil {
				t.Fatal(e)
			}
			readRows := func(table string) [][]any {
				t.Helper()
				rows, e := store.db.Query("SELECT * FROM " + table)
				if e != nil {
					t.Fatal(e)
				}
				defer func() {
					if err := rows.Close(); err != nil {
						t.Errorf("close rows: %v", err)
					}
				}()
				cols, e := rows.Columns()
				if e != nil {
					t.Fatal(e)
				}
				out := [][]any{}
				for rows.Next() {
					v := make([]any, len(cols))
					p := make([]any, len(cols))
					for i := range v {
						p[i] = &v[i]
					}
					if e = rows.Scan(p...); e != nil {
						t.Fatal(e)
					}
					for i, x := range v {
						if b, ok := x.([]byte); ok {
							v[i] = append([]byte(nil), b...)
						}
					}
					out = append(out, v)
				}
				if e = rows.Err(); e != nil {
					t.Fatal(e)
				}
				return out
			}
			before := readRows("runtime_runs")
			grants := readRows("runtime_effects")
			receipts := readRows("runtime_effect_owners")
			e = conformance.FinishOwnedFixture(t.Context(), rt, g, snap)
			if mode == "expired" || mode == "child-expired" {
				if e != nil {
					t.Fatalf("expiry changed identity: %v", e)
				}
				r, e := store.LoadOwnedRetirement(t.Context(), g.ID)
				if e != nil || r.Evidence.Completion.NotEntered() || r.Evidence.Completion != c {
					t.Fatalf("legacy retirement fabricated proof: %+v %v", r, e)
				}
				if _, e = store.LookupChildExecution(t.Context(), g.ChildRunID); !errors.Is(e, kernel.ErrNotFound) {
					t.Fatalf("grant not retired: %v", e)
				}
			} else {
				if !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("identity %s accepted: %v", mode, e)
				}
				if !reflect.DeepEqual(grants, readRows("runtime_effects")) || !reflect.DeepEqual(receipts, readRows("runtime_effect_owners")) {
					t.Fatal("rejected finish changed durable grants/receipts")
				}
			}
			if mode == "expired" || mode == "child-expired" {
				current, e := rt.Load(t.Context(), g.ChildRunID)
				native, _, le := sqliteManagedLease(t.Context(), store.db, g.ChildRunID)
				if e != nil || le != nil || kernel.ManagedDigest(current) != kernel.ManagedDigest(snap) || native.OwnerID != "" || native.LeaseID != "" || native.Epoch != lease.Epoch || !native.ExpiresAt.IsZero() {
					t.Fatalf("finish changed source or retained lease: %v %v", e, le)
				}
			} else if !reflect.DeepEqual(before, readRows("runtime_runs")) {
				t.Fatal("rejected finish changed aggregate/native authority")
			}
		})
	}
}
