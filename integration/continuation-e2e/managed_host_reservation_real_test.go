package continuatione2e

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	postgresadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type managedHostLostACK struct {
	*harnesspostgres.Store
	fault     error
	committed kernel.ManagedHostReservation
}

func (s *managedHostLostACK) ReserveManagedRecovery(ctx context.Context, p kernel.ManagedParkedProof) (kernel.ManagedHostReservation, error) {
	r, e := s.Store.ReserveManagedRecovery(ctx, p)
	if e != nil {
		return r, e
	}
	s.committed = r
	return r, s.fault
}

// Every subcase begins with the SAME real second-pending-commit process exit.
// The CANCEL contender is an actual admitted Host storage cancellation intent,
// not a fabricated cancellation receipt or a claimed callback return.
func TestRealPostgresManagedHostReservationModesAfterProcessCrash(t *testing.T) {
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
			fault := errors.New("Host reservation committed ACK lost")
			lossy := &managedHostLostACK{Store: host, fault: fault}
			rt, e := kernel.New(kernel.Dependencies{Store: postgresadapter.NewKernelStore(db), ManagedRecoveryHost: lossy})
			if e != nil {
				t.Fatal(e)
			}
			original, e := rt.Load(t.Context(), root.ExecutionRefID)
			if e != nil {
				t.Fatal(e)
			}
			safe, e := rt.LoadManagedRecovery(t.Context(), root.ExecutionRefID)
			if e != nil || safe.State != kernel.ManagedAvailable {
				t.Fatal("missing original actual safe point", e)
			}
			assertNoSend := func() {
				t.Helper()
				if got, e := rt.Load(t.Context(), root.ExecutionRefID); e != nil || !reflect.DeepEqual(got, original) {
					t.Fatalf("reservation changed original Kernel snapshot: %v", e)
				}
				if got := readManagedRecoveryRecords[model.Request](t, filepath.Join(files, "model.jsonl")); len(got) != 1 {
					t.Fatalf("reservation sent Model: %d", len(got))
				}
				if got := readManagedRecoveryRecords[managedRecoveryEffect](t, filepath.Join(files, "ledger.jsonl")); len(got) != 1 {
					t.Fatalf("reservation replayed committed Tool: %d", len(got))
				}
			}
			var parked kernel.ManagedParkedProof
			deadline := time.Now().Add(8 * time.Second)
			for {
				parked, e = rt.ParkManagedContinuation(t.Context(), root.ExecutionRefID)
				if e == nil {
					break
				}
				if !errors.Is(e, kernel.ErrExecutionConflict) || time.Now().After(deadline) {
					t.Fatal(e)
				}
				assertNoSend()
				time.Sleep(20 * time.Millisecond)
			}
			var intent harness.Item
			if mode == kernel.ManagedCancel {
				now := time.Now().UTC()
				payload, _ := json.Marshal(map[string]string{"boundary": "cancel_scope_pending", "runID": root.ExecutionRefID})
				intent = harness.Item{ID: "actual-managed-cancel", TurnID: turnID, RunID: root.ExecutionRefID, Kind: harness.ItemDiagnostic, Status: harness.ItemStarted, ParentItemID: harness.CancellationFenceParentItemID, Payload: payload, CreatedAt: now, UpdatedAt: now}
				var fresh bool
				intent, fresh, e = host.AppendItem(t.Context(), intent)
				if e != nil || !fresh {
					t.Fatalf("actual cancellation contender: %v", e)
				}
			}
			if _, e = rt.SelectManagedContinuation(t.Context(), parked); !errors.Is(e, fault) {
				t.Fatalf("actual reserve ACK cut not hit: %v", e)
			}
			assertNoSend()
			secondDB := openManagedRecoveryDB(t, isolated)
			peer, e := harnesspostgres.New(secondDB)
			if e != nil {
				t.Fatal(e)
			}
			peerRT, e := kernel.New(kernel.Dependencies{Store: postgresadapter.NewKernelStore(secondDB), ManagedRecoveryHost: peer})
			if e != nil {
				t.Fatal(e)
			}
			selected, e := peerRT.SelectManagedContinuation(t.Context(), parked)
			if e != nil {
				t.Fatal(e)
			}
			r := selected.Reservation()
			if !reflect.DeepEqual(r, lossy.committed) || r.Mode != mode || r.RecoveryID != safe.SafePoint.RecoveryID {
				t.Fatal("ACK retry changed immutable selection")
			}
			if mode == kernel.ManagedCancel && (r.Cancellation.IntentID != intent.ID || r.Cancellation.Seq != intent.Seq || string(r.Cancellation.Payload) != string(intent.Payload)) {
				t.Fatal("cancellation did not retain full actual intent")
			}
			replay, e := peer.ReserveManagedRecovery(t.Context(), parked)
			if e != nil || !reflect.DeepEqual(replay, r) {
				t.Fatalf("same reservation replay: %v", e)
			}
			// An ordinary caller cannot manufacture the dedicated managed completion.
			payload, _ := json.Marshal(map[string]string{"boundary": "managed_dispatch_transferred", "recoveryID": r.RecoveryID, "mode": string(kernel.ManagedResume), "selectionHash": kernel.ManagedDigest(r), "terminalHash": strings.Repeat("a", 64)})
			now := time.Now().UTC()
			_, _, e = peer.AppendItem(t.Context(), harness.Item{ID: "forged-managed-closure", TurnID: turnID, InvocationID: root.ID, RunID: root.ExecutionRefID, Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted, ParentItemID: r.Binding.DispatchID, Payload: payload, CreatedAt: now, UpdatedAt: now})
			if !errors.Is(e, harness.ErrConflict) {
				t.Fatalf("ordinary Append forged closure: %v", e)
			}
			foreign := r.Binding
			foreign.Source = append([]byte(nil), foreign.Source...)
			foreign.Source = json.RawMessage(`{"foreign":"source"}`)
			if _, e = peer.ReadManagedRecovery(t.Context(), foreign, r.RecoveryID); !errors.Is(e, harness.ErrConflict) {
				t.Fatalf("foreign frozen source accepted: %v", e)
			}
			originalJSON, _ := json.Marshal(r)
			// Isolated corruption negatives: a persisted mode/intent discrepancy must
			// fail read-back, even if the payload's public digest is recomputed.
			changes := []func(*kernel.ManagedHostReservation){
				func(v *kernel.ManagedHostReservation) {
					if v.Mode == kernel.ManagedResume {
						v.Mode = kernel.ManagedCancel
						v.Cancellation = kernel.ManagedCancellation{IntentID: "foreign", Seq: 1, Payload: json.RawMessage(`{}`), PayloadDigest: kernel.ManagedDigest(json.RawMessage(`{}`))}
					} else {
						v.Mode = kernel.ManagedResume
						v.Cancellation = kernel.ManagedCancellation{}
					}
				},
			}
			if mode == kernel.ManagedCancel {
				changes = append(changes,
					func(v *kernel.ManagedHostReservation) { v.Cancellation.IntentID = "foreign-intent" },
					func(v *kernel.ManagedHostReservation) { v.Cancellation.Seq++ },
					func(v *kernel.ManagedHostReservation) {
						v.Cancellation.Payload = json.RawMessage(`{"boundary":"cancel_scope_pending","runID":"foreign"}`)
						v.Cancellation.PayloadDigest = kernel.ManagedDigest(v.Cancellation.Payload)
					})
			}
			for _, change := range changes {
				altered := r
				altered.Cancellation.Payload = append([]byte(nil), r.Cancellation.Payload...)
				change(&altered)
				raw, _ := json.Marshal(altered)
				if e = db.Table("agent_harness_managed_recovery").Where("dispatch_id = ?", r.Binding.DispatchID).Update("record_json", string(raw)).Error; e != nil {
					t.Fatal(e)
				}
				_, readErr := peer.ReadManagedRecovery(t.Context(), r.Binding, r.RecoveryID)
				restoreErr := db.Table("agent_harness_managed_recovery").Where("dispatch_id = ?", r.Binding.DispatchID).Update("record_json", string(originalJSON)).Error
				if restoreErr != nil {
					t.Fatal(restoreErr)
				}
				if !errors.Is(readErr, harness.ErrConflict) {
					t.Fatalf("corrupted selection accepted: %v", readErr)
				}
				assertNoSend()
			}
			if mode == kernel.ManagedResume {
				// A successor dies before Leave: re-PARK one activation generation later
				// and retain exactly the same original Host reservation and RESUME mode.
				enabled, e := rt.EnableManagedContinuation(t.Context(), selected, "fixture-claimant", "fixture-owner", 20*time.Millisecond)
				if e != nil {
					t.Fatal(e)
				}
				deadline = time.Now().Add(time.Second)
				for {
					parked, e = rt.ParkManagedContinuation(t.Context(), root.ExecutionRefID)
					if e == nil {
						break
					}
					if !errors.Is(e, kernel.ErrExecutionConflict) || time.Now().After(deadline) {
						t.Fatal(e)
					}
					time.Sleep(5 * time.Millisecond)
				}
				if parked.Transfer().Generation <= enabled.Generation {
					t.Fatal("activation generation did not advance")
				}
				selected, e = peerRT.SelectManagedContinuation(t.Context(), parked)
				if e != nil || !reflect.DeepEqual(selected.Reservation(), r) {
					t.Fatalf("new generation changed Host mode/identity: %v", e)
				}
				assertNoSend()
				// Selection is nonrevocable: a later cancellation cannot replace it.
				payload, _ = json.Marshal(map[string]string{"boundary": "cancel_scope_pending", "runID": root.ExecutionRefID})
				_, _, e = peer.AppendItem(t.Context(), harness.Item{ID: "late-managed-cancel", TurnID: turnID, RunID: root.ExecutionRefID, Kind: harness.ItemDiagnostic, Status: harness.ItemStarted, ParentItemID: harness.CancellationFenceParentItemID, Payload: payload, CreatedAt: now, UpdatedAt: now})
				if !errors.Is(e, harness.ErrConflict) {
					t.Fatalf("late cancel replaced selected RESUME: %v", e)
				}
			} else {
				if _, e = rt.EnableManagedContinuation(t.Context(), selected, "wrong-mode", "wrong-owner", time.Second); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("CANCEL opened dispatch permission: %v", e)
				}
				assertNoSend()
			}
			session := newManagedRecoverySession(t, secondDB, files, false)
			finished, e := session.Resume(t.Context(), managedRecoveryTurn)
			if e != nil {
				t.Fatal(e)
			}
			want := harness.TurnCompleted
			if mode == kernel.ManagedCancel {
				want = harness.TurnCancelled
			}
			if finished.Turn.Status != want {
				t.Fatalf("selected mode result=%s want=%s", finished.Turn.Status, want)
			}
			final, e := peer.ReadManagedRecovery(t.Context(), r.Binding, r.RecoveryID)
			if e != nil || !reflect.DeepEqual(final, r) {
				t.Fatalf("closure changed immutable reservation: %v", e)
			}
			effects := readManagedRecoveryRecords[managedRecoveryEffect](t, filepath.Join(files, "ledger.jsonl"))
			calls := readManagedRecoveryRecords[model.Request](t, filepath.Join(files, "model.jsonl"))
			wantCalls := 3
			if mode == kernel.ManagedCancel {
				wantCalls = 1
			}
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
	_, root := managedRecoveryRoot(t, host)
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
	_, e = session.Resume(t.Context(), managedRecoveryTurn)
	if !errors.Is(e, harness.ErrConflict) {
		t.Fatalf("opaque pending-intent resume: %v", e)
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
