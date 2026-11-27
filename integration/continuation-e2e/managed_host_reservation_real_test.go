package continuatione2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	postgresadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type managedHostReadFault struct {
	*harnesspostgres.Store
	fault error
}

func (s *managedHostReadFault) VerifyManagedOriginalDispatch(ctx context.Context, b kernel.ManagedHostBinding) error {
	if e := s.Store.VerifyManagedOriginalDispatch(ctx, b); e != nil {
		return e
	}
	return s.fault
}

// A real interrupted process leaves one native source. Neither authorization
// read can elect a mode. Opposing commands contend at the native commit only.
func TestRealPostgresManagedNativeElectionAfterProcessCrash(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	for _, mode := range []kernel.ManagedRecoveryMode{kernel.ManagedResume, kernel.ManagedCancel} {
		t.Run(string(mode), func(t *testing.T) {
			db, isolated := managedRecoveryDatabase(t, dsn)
			if e := postgresadapter.Migrate(db); e != nil {
				t.Fatal(e)
			}
			if e := harnesspostgres.Migrate(db); e != nil {
				t.Fatal(e)
			}
			files := t.TempDir()
			runManagedRecoveryProcess(t, isolated, files, "crash", managedRecoveryCrashExit)
			host, e := harnesspostgres.New(db)
			if e != nil {
				t.Fatal(e)
			}
			turnID, root := managedRecoveryRoot(t, host)
			fault := errors.New("Host authorization read interrupted")
			rt, e := kernel.New(kernel.Dependencies{Store: postgresadapter.NewKernelStore(db), ManagedRecoveryHost: &managedHostReadFault{Store: host, fault: fault}})
			if e != nil {
				t.Fatal(e)
			}
			original, e := rt.Load(t.Context(), root.ExecutionRefID)
			if e != nil {
				t.Fatal(e)
			}
			safe, e := rt.LoadManagedRecovery(t.Context(), root.ExecutionRefID)
			if e != nil || !safe.Valid() {
				t.Fatal("missing original safe point", e)
			}
			assertNoSend := func() {
				t.Helper()
				if got := readManagedRecoveryRecords[model.Request](t, filepath.Join(files, "model.jsonl")); len(got) != 1 {
					t.Fatalf("premature Model: %d", len(got))
				}
				if got := readManagedRecoveryRecords[managedRecoveryEffect](t, filepath.Join(files, "ledger.jsonl")); len(got) != 1 {
					t.Fatalf("replayed committed Tool: %d", len(got))
				}
				if db.Migrator().HasTable("agent_harness_managed_recovery") {
					t.Fatal("independent Host reservation table exists")
				}
			}
			assertOriginal := func() {
				t.Helper()
				if got, e := rt.Load(t.Context(), root.ExecutionRefID); e != nil || !reflect.DeepEqual(got, original) {
					t.Fatalf("read mutated Run: %v", e)
				}
				if got, e := rt.LoadManagedRecovery(t.Context(), root.ExecutionRefID); e != nil || !reflect.DeepEqual(got, safe) {
					t.Fatalf("read elected native mode: %+v %v", got, e)
				}
				assertNoSend()
			}
			waitCandidate := func(r *kernel.Runtime) kernel.ManagedTransfer {
				t.Helper()
				deadline := time.Now().Add(8 * time.Second)
				for {
					lease := readManagedRecoveryLease(t, t.Context(), db, root.ExecutionRefID)
					if lease.ExecutionExpiresAt != nil && !lease.StoreNow.Before(*lease.ExecutionExpiresAt) {
						p, e := r.LoadManagedRecovery(t.Context(), root.ExecutionRefID)
						if e != nil {
							t.Fatal(e)
						}
						return p
					}
					if time.Now().After(deadline) {
						t.Fatal("native lease did not expire")
					}
					time.Sleep(20 * time.Millisecond)
				}
			}
			p := waitCandidate(rt)
			if _, e = rt.AuthorizeManagedContinuation(t.Context(), p); !errors.Is(e, fault) {
				t.Fatalf("read cut not hit: %v", e)
			}
			assertOriginal()
			secondDB := openManagedRecoveryDB(t, isolated)
			peer, e := harnesspostgres.New(secondDB)
			if e != nil {
				t.Fatal(e)
			}
			peerRT, e := kernel.New(kernel.Dependencies{Store: postgresadapter.NewKernelStore(secondDB), ManagedRecoveryHost: peer})
			if e != nil {
				t.Fatal(e)
			}
			resume, e := peerRT.AuthorizeManagedContinuation(t.Context(), p)
			if e != nil {
				t.Fatal(e)
			}
			cancel, e := peerRT.AuthorizeManagedContinuation(t.Context(), p, "original stop")
			if e != nil {
				t.Fatal(e)
			}
			if resume.Authorization().Mode != kernel.ManagedResume || cancel.Authorization().Mode != kernel.ManagedCancel || !cancel.Authorization().Valid() || cancel.Authorization().CancellationReason != "original stop" {
				t.Fatal("commands are not independent read-only candidates")
			}
			assertOriginal()
			r := resume.Authorization()
			now := time.Now().UTC()
			payload, _ := json.Marshal(map[string]string{"boundary": "cancel_scope_pending", "runID": root.ExecutionRefID, "protocol": "effects_v1", "canceller": "native"})
			if _, _, e = peer.AppendItem(t.Context(), harness.Item{ID: "forged-host-cancel", TurnID: turnID, RunID: root.ExecutionRefID, Kind: harness.ItemDiagnostic, Status: harness.ItemStarted, ParentItemID: harness.CancellationFenceParentItemID, Payload: payload, CreatedAt: now, UpdatedAt: now}); !errors.Is(e, harness.ErrConflict) {
				t.Fatalf("independent Host cancellation accepted: %v", e)
			}
			payload, _ = json.Marshal(map[string]string{"boundary": "managed_dispatch_transferred", "recoveryID": safe.SafePoint.RecoveryID, "mode": string(kernel.ManagedResume), "selectionHash": kernel.ManagedDigest(r), "terminalHash": strings.Repeat("a", 64)})
			if _, _, e = peer.AppendItem(t.Context(), harness.Item{ID: "forged-managed-closure", TurnID: turnID, InvocationID: root.ID, RunID: root.ExecutionRefID, Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted, ParentItemID: safe.SafePoint.Host.DispatchID, Payload: payload, CreatedAt: now, UpdatedAt: now}); !errors.Is(e, harness.ErrConflict) {
				t.Fatalf("forged closure: %v", e)
			}
			// A canonical ordinary-return receipt must also be rejected when
			// it lacks the original native physical-return authority.
			hash := sha256.Sum256([]byte(safe.SafePoint.Host.DispatchID + "\x00"))
			forgedID := "hdispatchdone_" + hex.EncodeToString(hash[:])[:24]
			if _, _, e = peer.AppendItem(t.Context(), harness.Item{ID: forgedID, TurnID: turnID, InvocationID: root.ID, RunID: root.ExecutionRefID, Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted, ParentItemID: safe.SafePoint.Host.DispatchID, Payload: json.RawMessage(`{"boundary":"physical_dispatch_returned"}`), CreatedAt: now, UpdatedAt: now}); !errors.Is(e, harness.ErrConflict) {
				t.Fatalf("forged ordinary native return: %v", e)
			}
			foreign := safe.SafePoint.Host
			foreign.Source = json.RawMessage(`{"foreign":"source"}`)
			if e = peer.VerifyManagedOriginalDispatch(t.Context(), foreign); !errors.Is(e, harness.ErrConflict) {
				t.Fatalf("foreign source: %v", e)
			}
			if mode == kernel.ManagedResume {
				_, admitted, e := peerRT.AdmitManagedContinuation(t.Context(), resume, "fixture-owner", 20*time.Millisecond)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = peerRT.CommitManagedCancellation(t.Context(), cancel); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("losing cancellation: %v", e)
				}
				enabled, e := peerRT.LoadManagedRecovery(t.Context(), root.ExecutionRefID)
				if e != nil || enabled.SuccessorGrant != admitted.Grant {
					t.Fatalf("missing native admission: %v", e)
				}
				p = waitCandidate(peerRT)
				if p.SuccessorGrant.ExecutionEpoch != enabled.SuccessorGrant.ExecutionEpoch || p.Resolved() {
					t.Fatal("recovery read advanced native generation")
				}
				if got, e := peerRT.LoadManagedRecovery(t.Context(), root.ExecutionRefID); e != nil || !reflect.DeepEqual(got, enabled) {
					t.Fatalf("recovery read wrote state: %v", e)
				}
				again, e := peerRT.AuthorizeManagedContinuation(t.Context(), p)
				if e != nil || !reflect.DeepEqual(again.Authorization(), r) {
					t.Fatalf("native selection changed: %v", e)
				}
			} else {
				r = cancel.Authorization()
				committed, e := peerRT.CommitManagedCancellation(t.Context(), cancel)
				if e != nil || !committed.Resolved() || committed.Selection.Mode != kernel.ManagedCancel {
					t.Fatalf("native cancel: %v", e)
				}
				if _, _, e = peerRT.AdmitManagedContinuation(t.Context(), resume, "loser", time.Second); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("losing resume: %v", e)
				}
				if committed.Terminal == nil || committed.Terminal.Run.ErrorDetail != "original stop" {
					t.Fatal("native cancellation reason lost")
				}
			}
			assertNoSend()
			session := newManagedRecoverySession(t, secondDB, files, false)
			finished, e := session.Resume(t.Context(), managedRecoveryTurn)
			if e != nil {
				t.Fatal(e)
			}
			want, wantCalls := harness.TurnCompleted, 3
			if mode == kernel.ManagedCancel {
				want, wantCalls = harness.TurnCancelled, 1
			}
			if finished.Turn.Status != want {
				t.Fatalf("mode result=%s want=%s", finished.Turn.Status, want)
			}
			final, e := peerRT.LoadManagedRecovery(t.Context(), root.ExecutionRefID)
			if e != nil || !final.Resolved() || final.Selection.Mode != mode || !reflect.DeepEqual(final.Selection, r) {
				t.Fatalf("native closure lost original authorization: %+v %v", final, e)
			}
			if db.Migrator().HasTable("agent_harness_managed_recovery") {
				t.Fatal("Host reservation table exists")
			}
			effects := readManagedRecoveryRecords[managedRecoveryEffect](t, filepath.Join(files, "ledger.jsonl"))
			calls := readManagedRecoveryRecords[model.Request](t, filepath.Join(files, "model.jsonl"))
			if len(effects) != 1 || len(calls) != wantCalls {
				t.Fatalf("mode effects: calls=%d effects=%d", len(calls), len(effects))
			}
		})
	}
}

// The same durable unsent intent is insufficient without the explicit audited
// managed lifecycle. Time passage never manufactures that missing authority.
func TestRealPostgresOpaqueManagedProcessCrashRemainsUnknown(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, isolated := managedRecoveryDatabase(t, dsn)
	if e := postgresadapter.Migrate(db); e != nil {
		t.Fatal(e)
	}
	if e := harnesspostgres.Migrate(db); e != nil {
		t.Fatal(e)
	}
	files := t.TempDir()
	runManagedRecoveryProcess(t, isolated, files, "opaque-crash", managedRecoveryCrashExit)
	host, e := harnesspostgres.New(db)
	if e != nil {
		t.Fatal(e)
	}
	turnID, root := managedRecoveryRoot(t, host)
	originalItems, e := host.ListItems(t.Context(), turnID, 0, 1000)
	if e != nil || !harness.PendingExecutionDispatch(originalItems, root.ID) {
		t.Fatalf("original Host dispatch missing: %v", e)
	}
	store := postgresadapter.NewKernelStore(db)
	before, e := store.Load(t.Context(), root.ExecutionRefID)
	if e != nil {
		t.Fatal(e)
	}
	grant, e := store.LookupChildExecution(t.Context(), root.ExecutionRefID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.LoadManagedRecovery(t.Context(), root.ExecutionRefID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("opaque callback created safe descriptor: %v", e)
	}
	lease := readManagedRecoveryLease(t, t.Context(), db, root.ExecutionRefID)
	if lease.ExecutionExpiresAt == nil {
		t.Fatal("original lease absent")
	}
	deadline := time.Now().Add(8 * time.Second)
	for lease.StoreNow.Before(*lease.ExecutionExpiresAt) {
		if time.Now().After(deadline) {
			t.Fatal("original lease did not expire")
		}
		time.Sleep(20 * time.Millisecond)
		lease = readManagedRecoveryLease(t, t.Context(), db, root.ExecutionRefID)
	}
	// The original owned physical fence precedes the legacy Epoch fallback.
	if e = store.CheckExecution(t.Context(), root.ExecutionRefID, nil); !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatalf("opaque original physical authority: %v", e)
	}
	originalLease := readManagedRecoveryLease(t, t.Context(), db, root.ExecutionRefID)
	session := newManagedRecoverySession(t, db, files, false, false)
	resumed, e := session.Resume(t.Context(), managedRecoveryTurn)
	projection := func(i harness.Item) bool { return i.ParentItemID != harness.ExecutionDispatchParentItemID }
	if !errors.Is(e, harness.ErrExecutionOutcomeUnknown) || !harness.PendingExecutionDispatch(resumed.Items, root.ID) ||
		!reflect.DeepEqual(slices.DeleteFunc(originalItems, projection), slices.DeleteFunc(resumed.Items, projection)) {
		t.Fatalf("opaque pending-intent resume changed Host dispatch: %v", e)
	}
	after, e := store.Load(t.Context(), root.ExecutionRefID)
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("opaque recovery changed Kernel source: %v", e)
	}
	afterLease := readManagedRecoveryLease(t, t.Context(), db, root.ExecutionRefID)
	if !sameManagedRecoveryLease(originalLease, afterLease) {
		t.Fatal("opaque denied recovery changed original native authority")
	}
	retained, e := store.LookupChildExecution(t.Context(), root.ExecutionRefID)
	if e != nil || retained != grant {
		t.Fatalf("opaque recovery retired original scope: %v", e)
	}
	if len(readManagedRecoveryRecords[model.Request](t, filepath.Join(files, "model.jsonl"))) != 1 ||
		len(readManagedRecoveryRecords[managedRecoveryEffect](t, filepath.Join(files, "ledger.jsonl"))) != 1 {
		t.Fatal("opaque recovery replayed physical work")
	}
}
