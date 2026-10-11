package harness

import (
	"context"
	"errors"
	"strings"
	"testing"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestContextArtifactQueryFindsDeepUnicodeFieldsAndContinues(t *testing.T) {
	t.Parallel()
	store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
	prefix := strings.Repeat("中文🌱", 4000)
	payload := prefix + "DELIVERY_CODE=第一🌱;" + strings.Repeat("尾🌱", 6000) + "DELIVERY_CODE=第二🌱;"
	artifact, err := runtimecontext.NewArtifact(runtimecontext.ArtifactCompaction, turn.SessionID,
		checkpoint.Generation, checkpoint.CoveredThroughSourceID, payload, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutContextArtifact(t.Context(), artifact); err != nil {
		t.Fatal(err)
	}
	handler := NewContextArtifactToolHandler(store, nil)
	first := executeContextArtifactReadInput(t, handler, root.ExecutionRefID, contextArtifactReadInput{
		ArtifactID: artifact.ID, Query: "DELIVERY_CODE=", Limit: 24,
	})
	if first.Found == nil || !*first.Found || first.Offset != len([]rune(prefix)) ||
		first.TotalRunes != len([]rune(payload)) || first.Done || !strings.HasPrefix(first.Content, "DELIVERY_CODE=第一🌱;") {
		t.Fatalf("deep Unicode search result = %#v", first)
	}
	continued := executeContextArtifactRead(t, handler, root.ExecutionRefID, artifact.ID, first.NextOffset, 24)
	if continued.Found != nil || continued.Content != string([]rune(payload)[first.NextOffset:continued.NextOffset]) {
		t.Fatalf("search pagination changed exact payload: %#v", continued)
	}
	second := executeContextArtifactReadInput(t, handler, root.ExecutionRefID, contextArtifactReadInput{
		ArtifactID: artifact.ID, Query: "DELIVERY_CODE=", Offset: first.NextOffset, Limit: 100,
	})
	if second.Found == nil || !*second.Found || !second.Done || second.Content != "DELIVERY_CODE=第二🌱;" {
		t.Fatalf("search continuation did not find second literal: %#v", second)
	}
	missing := executeContextArtifactReadInput(t, handler, root.ExecutionRefID, contextArtifactReadInput{
		ArtifactID: artifact.ID, Query: "missing-key",
	})
	if missing.Found == nil || *missing.Found || missing.Content != "" || !missing.Done || missing.NextOffset != len([]rune(payload)) {
		t.Fatalf("missing literal must return a bounded no-match result: %#v", missing)
	}
}

func TestContextArtifactMissingIDOffersOnlyOwnedTurnCheckpointReferences(t *testing.T) {
	t.Parallel()
	store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
	artifact, err := runtimecontext.NewArtifact(runtimecontext.ArtifactCompaction, turn.SessionID,
		checkpoint.Generation+1, checkpoint.CoveredThroughSourceID, "owned archived context", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutContextArtifact(t.Context(), artifact); err != nil {
		t.Fatal(err)
	}
	next, err := runtimecontext.NewManager(runtimecontext.Dependencies{}).Rollover(t.Context(), runtimecontext.RolloverRequest{
		Previous: checkpoint, Window: checkpoint.Window, Artifacts: []runtimecontext.Artifact{artifact}, Reason: "soft_limit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CommitContextCheckpoint(t.Context(), ContextCheckpointCommit{
		TurnID: turn.ID, ExpectedTurnRevision: turn.Revision, ExpectedTurnCheckpointID: checkpoint.ID,
		ExpectedHeadCheckpointID: checkpoint.ID, Checkpoint: next, UpdatedAt: turn.UpdatedAt.Add(1),
	}); err != nil {
		t.Fatal(err)
	}
	foreign, err := runtimecontext.NewArtifact(runtimecontext.ArtifactCompaction, "another-session", 1, "source", "secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutContextArtifact(t.Context(), foreign); err != nil {
		t.Fatal(err)
	}
	request := tools.ExecutionRequest{RunID: root.ExecutionRefID,
		Call: tools.Call{ID: "typo", ToolKey: ContextArtifactToolKey, Arguments: mustContextArtifactArguments(t, "ctxa_typo", 0, 100)}}
	_, err = NewContextArtifactToolHandler(store, nil).Execute(t.Context(), request)
	code, message, recoverable := tools.RecoverableCallErrorInfo(err)
	if !recoverable || code != "context_artifact.not_found" || !strings.Contains(message, artifact.ID) || strings.Contains(message, foreign.ID) {
		t.Fatalf("missing ID guidance must carry only exact owned references: %q, %v", message, err)
	}
	request.Call.Arguments = mustContextArtifactArguments(t, foreign.ID, 0, 100)
	_, err = NewContextArtifactToolHandler(store, nil).Execute(t.Context(), request)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("known foreign artifact must remain inaccessible: %v", err)
	}
}

func TestContextArtifactMissingIDDoesNotMaskCheckpointFailure(t *testing.T) {
	t.Parallel()
	for _, failure := range []error{ErrNotFound, context.Canceled, errors.New("store unavailable")} {
		t.Run(failure.Error(), func(t *testing.T) {
			t.Parallel()
			store, _, _, root := contextRunMiddlewareFixture(t)
			handler := NewContextArtifactToolHandler(contextArtifactCheckpointFailureStore{Store: store, err: failure}, nil)
			_, err := handler.Execute(t.Context(), tools.ExecutionRequest{RunID: root.ExecutionRefID,
				Call: tools.Call{ID: "typo", ToolKey: ContextArtifactToolKey, Arguments: mustContextArtifactArguments(t, "ctxa_typo", 0, 100)}})
			if _, _, recoverable := tools.RecoverableCallErrorInfo(err); recoverable || !errors.Is(err, failure) {
				t.Fatalf("durable checkpoint failure must remain fatal: %v", err)
			}
		})
	}
}

type contextArtifactCheckpointFailureStore struct {
	Store
	err error
}

func (store contextArtifactCheckpointFailureStore) GetContextCheckpoint(context.Context, string) (runtimecontext.Checkpoint, error) {
	return runtimecontext.Checkpoint{}, store.err
}
