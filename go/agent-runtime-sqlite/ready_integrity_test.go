package sqlite

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Even a matching row fingerprint cannot turn a nonterminal Run transition
// without a wakeup into a legitimate continuation projection.
func TestInvalidProjectionIsIsolatedEvenWithRecomputedFingerprint(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := CreateNew(context.Background(), filepath.Join(dir, "run.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	now := time.Now().UTC()
	record := kernel.Record{Run: kernel.Run{
		ID: "r", Kind: "agent", Actor: kernel.ActorRef{TenantID: "p", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"},
		Goal:   "test", Status: kernel.RunStatusRunning, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}, State: json.RawMessage(`{}`)}
	if _, err := store.Create(context.Background(), record, []kernel.EventDraft{{Type: "started", Wakeup: true}}); err != nil {
		t.Fatal(err)
	}
	var workID, runID, kind, status string
	var revision uint64
	var committed int64
	err = store.db.QueryRow(`SELECT work_id,run_id,kind,status,revision,committed_ns FROM runtime_ready_work`).
		Scan(&workID, &runID, &kind, &status, &revision, &committed)
	if err != nil {
		t.Fatal(err)
	}
	changed := []byte("[]")
	matchingHash := readyPayloadFingerprint(workID, runID, kind, status, revision, committed, changed)
	if _, err = store.db.Exec(`UPDATE runtime_ready_work SET events_json=?,payload_hash=? WHERE work_id=?`, changed, matchingHash, workID); err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimTransitions(context.Background(), kernel.TransitionClaimRequest{
		WorkerID: "w", Limit: 4, Now: now.Add(time.Second), LeaseDuration: time.Minute,
	})
	if err != nil || len(claims) != 0 {
		t.Fatalf("invalid projection escaped quarantine %#v %v", claims, err)
	}
	quarantined, err := store.ListQuarantinedTransitions(context.Background(), 4)
	if err != nil || len(quarantined) != 1 || quarantined[0].FailureCode != "invalid_projection" {
		t.Fatalf("semantic check did not quarantine matching-hash nonwakeup: %#v %v", quarantined, err)
	}
}
