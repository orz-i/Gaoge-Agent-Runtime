package postgres

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
)

// Query barriers enforce the problematic interleaving: another worker commits
// after this worker commits, but before its post-commit Load could return.
type ownSnapshotQueryKey struct{}

type snapshotResult struct {
	snapshot kernel.Snapshot
	err      error
}

func testKernelStoreApplyOwnSnapshot(t *testing.T, db *gorm.DB) {
	t.Helper()
	store := NewKernelStore(db)
	record := snapshotTestRecord("apply-own-snapshot")
	created, err := store.Create(t.Context(), record, []kernel.EventDraft{{Type: "run.created"}})
	if err != nil {
		t.Fatal(err)
	}

	aboutToRead := make(chan struct{})
	releaseRead := make(chan struct{})
	var queries atomic.Int32
	if err := db.Callback().Query().Before("gorm:query").Register("test:apply-own-snapshot-read", func(tx *gorm.DB) {
		if tx.Statement.Context.Value(ownSnapshotQueryKey{}) != true {
			return
		}
		// The first query reads the row inside the committing transaction.
		// A second query is the post-commit Load that must not exist.
		if queries.Add(1) == 2 {
			close(aboutToRead)
			<-releaseRead
		}
	}); err != nil {
		t.Fatal(err)
	}

	applyA := make(chan snapshotResult, 1)
	go func() {
		result, err := store.Apply(context.WithValue(context.Background(), ownSnapshotQueryKey{}, true),
			record.Run.ID, 1, snapshotMutation(record, 2, `{"worker":"A"}`))
		applyA <- snapshotResult{result, err}
	}()
	var own snapshotResult
	select {
	case <-aboutToRead:
		// A has already committed r2. B commits r3 before the old post-commit Load.
		if _, err := store.Apply(t.Context(), record.Run.ID, 2,
			snapshotMutation(record, 3, `{"worker":"B"}`)); err != nil {
			close(releaseRead)
			t.Fatalf("concurrent worker B: %v", err)
		}
		close(releaseRead)
		own = <-applyA
	case own = <-applyA:
		// A returned its own snapshot before B committed.
		if _, err := store.Apply(t.Context(), record.Run.ID, 2,
			snapshotMutation(record, 3, `{"worker":"B"}`)); err != nil {
			t.Fatalf("subsequent worker B: %v", err)
		}
	case <-time.After(10 * time.Second):
		close(releaseRead)
		t.Fatal("timed out waiting for worker A")
	}
	if own.err != nil || own.snapshot.Run.Revision != 2 ||
		string(own.snapshot.State) != `{"worker":"A"}` || own.snapshot.EventHead != created.EventHead+1 {
		t.Fatalf("Apply returned another worker's snapshot: %+v err=%v", own.snapshot, own.err)
	}
	current, err := store.Load(t.Context(), record.Run.ID)
	if err != nil || current.Run.Revision != 3 || string(current.State) != `{"worker":"B"}` {
		t.Fatalf("durable successor: %+v err=%v", current, err)
	}
}

func testKernelStoreCreateOwnSnapshot(t *testing.T, db *gorm.DB) {
	t.Helper()
	store := NewKernelStore(db)
	record := snapshotTestRecord("create-own-snapshot")
	aboutToRead := make(chan struct{})
	releaseRead := make(chan struct{})
	if err := db.Callback().Query().Before("gorm:query").Register("test:create-own-snapshot-read", func(tx *gorm.DB) {
		if tx.Statement.Context.Value(ownSnapshotQueryKey{}) == true {
			close(aboutToRead)
			<-releaseRead
		}
	}); err != nil {
		t.Fatal(err)
	}
	createdCh := make(chan snapshotResult, 1)
	go func() {
		snapshot, err := store.Create(context.WithValue(context.Background(), ownSnapshotQueryKey{}, true),
			record, []kernel.EventDraft{{Type: "run.created"}})
		createdCh <- snapshotResult{snapshot, err}
	}()
	var own snapshotResult
	select {
	case <-aboutToRead:
		if _, err := store.Apply(t.Context(), record.Run.ID, 1,
			snapshotMutation(record, 2, `{"worker":"B"}`)); err != nil {
			close(releaseRead)
			t.Fatal(err)
		}
		close(releaseRead)
		own = <-createdCh
	case own = <-createdCh:
		if _, err := store.Apply(t.Context(), record.Run.ID, 1,
			snapshotMutation(record, 2, `{"worker":"B"}`)); err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		close(releaseRead)
		t.Fatal("timed out waiting for Create")
	}
	if own.err != nil || own.snapshot.Run.Revision != 1 ||
		string(own.snapshot.State) != `{"worker":"initial"}` || own.snapshot.EventHead != 1 {
		t.Fatalf("Create returned another worker's snapshot: %+v err=%v", own.snapshot, own.err)
	}
}

func snapshotTestRecord(id string) kernel.Record {
	now := time.Now().UTC()
	return kernel.Record{
		Run: kernel.Run{ID: id, Kind: "agent", Status: kernel.RunStatusRunning,
			Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
			Goal:   "snapshot isolation", Revision: 1, CreatedAt: now, UpdatedAt: now},
		State: json.RawMessage(`{"worker":"initial"}`),
	}
}

func snapshotMutation(original kernel.Record, revision uint64, state string) kernel.StoreMutation {
	original.Run.Revision = revision
	original.Run.UpdatedAt = original.Run.UpdatedAt.Add(time.Duration(revision) * time.Second)
	original.State = json.RawMessage(state)
	return kernel.StoreMutation{Record: original, Events: []kernel.EventDraft{{Type: "run.advanced"}}}
}

func TestKernelStoreApplyReturnsOwnCommittedSnapshot(t *testing.T) {
	testKernelStoreApplyOwnSnapshot(t, openKernelStoreTestDB(t))
}

func TestKernelStoreCreateReturnsOwnCommittedSnapshot(t *testing.T) {
	testKernelStoreCreateOwnSnapshot(t, openKernelStoreTestDB(t))
}
