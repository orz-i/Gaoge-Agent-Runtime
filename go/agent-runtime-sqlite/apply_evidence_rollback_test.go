package sqlite_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestIdentifiedApplyEvidenceInsertFailureRollsBackRunCAS(t *testing.T) {
	path := filepath.Join(privateDir(t), "identified-rollback.sqlite")
	store, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	created, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: "identified-rollback", Kind: "test.evidence",
		Actor:  kernel.ActorRef{TenantID: "t", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "atomic proof", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	_, err = raw.Exec(`CREATE TRIGGER abort_apply_proof BEFORE INSERT ON runtime_events
		WHEN NEW.event_type='kernel.apply.evidence'
		BEGIN SELECT RAISE(ABORT,'proof failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	intent := kernel.Mutation{
		Status: kernel.RunStatusRunning, State: json.RawMessage(`{"mustRollback":true}`),
		Events: []kernel.EventDraft{{Type: "feature.rollback", Wakeup: true}},
	}
	_, err = rt.ApplyIdentified(t.Context(), created.Run.ID, created.Run.Revision, "atomic-operation", intent)
	if err == nil {
		t.Fatal("identified proof insert should abort entire Run CAS")
	}
	loaded, err := rt.Load(t.Context(), created.Run.ID)
	if err != nil || loaded.Run.Revision != created.Run.Revision ||
		loaded.EventHead != created.EventHead || string(loaded.State) != string(created.State) {
		t.Fatalf("failed event proof leaked a Run commit: %+v err=%v", loaded, err)
	}
	result, err := rt.VerifyIdentifiedApply(t.Context(), created.Run.ID, created.Run.Revision, "atomic-operation", intent)
	if err != nil || result.Status != kernel.ApplyEvidenceRetryableCAS {
		t.Fatalf("aborted journal proof misclassified as committed: %+v err=%v", result, err)
	}
	if _, err = raw.Exec("DROP TRIGGER abort_apply_proof"); err != nil {
		t.Fatal(err)
	}
	applied, err := rt.ApplyIdentified(t.Context(), created.Run.ID, created.Run.Revision, "atomic-operation", intent)
	if err != nil || applied.Run.Revision != created.Run.Revision+1 {
		t.Fatalf("same fixed-Revision recovery could not commit: %+v err=%v", applied, err)
	}
	if _, err = rt.ApplyIdentified(t.Context(), created.Run.ID, created.Run.Revision, "atomic-operation", intent); !errors.Is(err, kernel.ErrApplyAlreadyCommitted) {
		t.Fatalf("identical replay was not fenced: %v", err)
	}
}
