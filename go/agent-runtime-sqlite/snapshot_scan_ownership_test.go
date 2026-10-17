package sqlite_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// statePayload is deliberately non-empty and large enough to expose repeated
// State-buffer copies during persisted Snapshot reads.
func statePayload(size int) json.RawMessage {
	return json.RawMessage(`{"payload":"` + strings.Repeat("x", size) + `"}`)
}

// database/sql.Scan into *[]byte promises an independent, caller-owned copy.
// That guarantee must remain true after the SQLite connection is reused,
// a new revision is committed, and the Store is closed/reopened.
func TestSQLiteSnapshotScanStateOwnershipAfterReopen(t *testing.T) {
	path := filepath.Join(privateDir(t), "snapshot-ownership.sqlite")
	store, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	req := sampleCreate("scan-ownership")
	req.State = statePayload(256 * 1024)
	expectedFirst := bytes.Clone(req.State)
	created, err := runtime.Create(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	// The caller owns both the input and the returned snapshot. Neither
	// mutable slice may alias persisted State or an independent later Load.
	req.State[12] = 'y'
	created.State[12] = 'z'
	first, err := store.Load(t.Context(), created.Run.ID)
	if err != nil || !bytes.Equal(first.State, expectedFirst) {
		t.Fatalf("first Load unexpectedly aliases input/Create state: %v", err)
	}
	second, err := store.Load(t.Context(), created.Run.ID)
	if err != nil || !bytes.Equal(second.State, expectedFirst) {
		t.Fatalf("second Load unexpectedly aliases first snapshot: %v", err)
	}
	first.State[12] = 'a'
	if !bytes.Equal(second.State, expectedFirst) {
		t.Fatal("two independently scanned snapshots share mutable State")
	}

	next := statePayload(320 * 1024)
	expectedNext := bytes.Clone(next)
	applied, err := runtime.Apply(t.Context(), created.Run.ID, created.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: next,
	})
	if err != nil || !bytes.Equal(applied.State, expectedNext) {
		t.Fatalf("Apply returned invalid new State: %v", err)
	}
	next[12] = 'b'
	applied.State[12] = 'c'
	// Old snapshots are independent of the next revision.
	if !bytes.Equal(second.State, expectedFirst) {
		t.Fatal("new commit modified already scanned old snapshot")
	}
	latest, err := store.Load(t.Context(), created.Run.ID)
	if err != nil || !bytes.Equal(latest.State, expectedNext) {
		t.Fatalf("Load after caller mutation lost committed State: %v", err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	// A scanned []byte must remain valid even after the Rows and DB close.
	if !bytes.Equal(second.State, expectedFirst) || !bytes.Equal(latest.State, expectedNext) {
		t.Fatal("closing connection invalidated caller-owned scan buffers")
	}
	reopened, err := sqlite.OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	again, err := reopened.Load(t.Context(), created.Run.ID)
	if err != nil || again.Run.Revision != applied.Run.Revision || !bytes.Equal(again.State, expectedNext) {
		t.Fatalf("reopen has different persisted State/revision: %v", err)
	}
}

func TestSQLiteSnapshotScanParallelReadsStayIsolated(t *testing.T) {
	store := openStore(t)
	runtime, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	req := sampleCreate("parallel-state-reads")
	req.State = statePayload(128 * 1024)
	if _, err = runtime.Create(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	expected := bytes.Clone(req.State)
	const workers = 12
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				snap, loadErr := store.Load(t.Context(), req.ID)
				if loadErr != nil {
					errs <- loadErr
					return
				}
				if !bytes.Equal(snap.State, expected) {
					errs <- fmt.Errorf("scanned snapshot is unexpectedly shared")
					return
				}
				snap.State[12] = 'z'
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	snap, err := store.Load(t.Context(), req.ID)
	if err != nil || !bytes.Equal(snap.State, expected) {
		t.Fatalf("parallel reader mutation escaped its snapshot: %v", err)
	}
}
