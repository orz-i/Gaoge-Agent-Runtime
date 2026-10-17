package sqlite_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestIdentifiedApplyProofSurvivesStoreReopenAndLaterRevision(t *testing.T) {
	path := filepath.Join(privateDir(t), "apply-evidence.sqlite")
	store, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	created, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: "identified-reopen", Kind: "test.receipt",
		Actor:  kernel.ActorRef{TenantID: "t", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "committed history", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	mutation := kernel.Mutation{Status: kernel.RunStatusRunning, State: json.RawMessage(`{"one":true}`)}
	first, err := rt.ApplyIdentified(t.Context(), created.Run.ID, created.Run.Revision, "stable-reopen-operation", mutation)
	if err != nil {
		t.Fatal(err)
	}
	_, err = rt.Apply(t.Context(), created.Run.ID, first.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: json.RawMessage(`{"terminal":true}`),
		Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage(`true`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	restarted, err := kernel.New(kernel.Dependencies{Store: reopened})
	if err != nil {
		t.Fatal(err)
	}
	result, err := restarted.VerifyIdentifiedApply(t.Context(), created.Run.ID, created.Run.Revision,
		"stable-reopen-operation", mutation)
	if err != nil || result.Status != kernel.ApplyEvidenceCommitted ||
		result.CommittedRevision != first.Run.Revision || result.CurrentRevision != first.Run.Revision+1 {
		t.Fatalf("reopened terminal Run lost exact previous Apply: %+v err=%v", result, err)
	}
	if _, err = restarted.ApplyIdentified(t.Context(), created.Run.ID, created.Run.Revision,
		"stable-reopen-operation", mutation); !errors.Is(err, kernel.ErrApplyAlreadyCommitted) {
		t.Fatalf("reopen replayed a completed operation: %v", err)
	}
}
