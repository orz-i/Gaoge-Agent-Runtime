package sqlite

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestOwnedRetirementFinishNativeIdentity(t *testing.T) {
	for _, mode := range []string{"owner", "lease", "epoch", "cleared", "expired"} {
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
			snap, e := rt.Create(t.Context(), kernel.CreateRequest{ID: "retire-root", Kind: "agent", Actor: kernel.ActorRef{TenantID: "t", ActorID: "a"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "th"}, Goal: "original", State: []byte("{}"), ExecutionRequired: true})
			if e != nil {
				t.Fatal(e)
			}
			lease, e := store.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: snap.Run.ID, OwnerID: "original", LeaseDuration: time.Minute})
			if e != nil {
				t.Fatal(e)
			}
			g := kernel.ChildExecutionGrant{ID: "retire-grant", ChildRunID: snap.Run.ID, ExpectedRevision: snap.Run.Revision, ExecutionOwnerID: lease.OwnerID, ExecutionLeaseID: lease.LeaseID, ExecutionEpoch: lease.Epoch, AdmissionSourceHash: strings.Repeat("a", 64), AdmissionOperation: "start"}
			// Original legacy marker row: no fabricated first-party callback evidence.
			_, e = store.db.Exec("INSERT INTO runtime_child_effect_grants(grant_id,parent_run_id,child_run_id,start_revision,settled_revision,execution_owner_id,execution_lease_id,execution_epoch,admission_source_hash,admission_operation) VALUES(?,?,?,?,?,?,?,?,?,?)", g.ID, g.ChildRunID, g.ChildRunID, g.ExpectedRevision, snap.Run.Revision, g.ExecutionOwnerID, g.ExecutionLeaseID, g.ExecutionEpoch, g.AdmissionSourceHash, g.AdmissionOperation)
			if e != nil {
				t.Fatal(e)
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
			grants := readRows("runtime_child_effect_grants")
			receipts := readRows("runtime_owned_retirements")
			e = store.FinishChildExecution(t.Context(), g)
			if mode == "expired" {
				if e != nil {
					t.Fatalf("expiry changed identity: %v", e)
				}
				r, e := store.LoadOwnedRetirement(t.Context(), g.ID)
				if e != nil || r.Evidence.MarkOrigin != "LEGACY_UNKNOWN" || r.Evidence.Completion.GrantID != "" {
					t.Fatalf("legacy retirement fabricated proof: %+v %v", r, e)
				}
				if _, e = store.LookupChildExecution(t.Context(), g.ChildRunID); !errors.Is(e, kernel.ErrNotFound) {
					t.Fatalf("grant not retired: %v", e)
				}
			} else {
				if !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("identity %s accepted: %v", mode, e)
				}
				if !reflect.DeepEqual(grants, readRows("runtime_child_effect_grants")) || !reflect.DeepEqual(receipts, readRows("runtime_owned_retirements")) {
					t.Fatal("rejected finish changed durable grants/receipts")
				}
			}
			if !reflect.DeepEqual(before, readRows("runtime_runs")) {
				t.Fatal("Finish changed aggregate/native authority")
			}
		})
	}
}
