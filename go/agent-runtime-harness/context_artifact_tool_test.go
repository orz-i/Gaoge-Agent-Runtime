package harness

import (
	"encoding/json"
	"errors"
	"testing"

	runtimecontext "github.com/orz-i/Gaoge/sdk/go/agent-runtime/context"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

func TestContextArtifactToolReadsExactPayloadByDurableRunOwnership(t *testing.T) {
	t.Parallel()
	store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
	payload := json.RawMessage(`{"removed":[{"role":"user","content":"alpha beta gamma"}]}`)
	artifact, err := runtimecontext.NewArtifact(
		runtimecontext.ArtifactCompaction, turn.SessionID, checkpoint.Generation,
		checkpoint.CoveredThroughSourceID, "", payload,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutContextArtifact(t.Context(), artifact); err != nil {
		t.Fatal(err)
	}
	exactPayload := string(artifact.ContentJSON)
	handler := NewContextArtifactToolHandler(store, nil)
	first := executeContextArtifactRead(t, handler, root.ExecutionRefID, artifact.ID, 0, 12)
	assertFirstContextArtifactPage(t, first, artifact, exactPayload)
	second := executeContextArtifactRead(t, handler, root.ExecutionRefID, artifact.ID, first.NextOffset, contextArtifactMaxLimit)
	assertCompleteContextArtifactRead(t, first, second, exactPayload)
}

func assertFirstContextArtifactPage(
	t *testing.T,
	page contextArtifactReadResult,
	artifact runtimecontext.Artifact,
	exactPayload string,
) {
	t.Helper()
	if page.ArtifactID != artifact.ID || page.Kind != runtimecontext.ArtifactCompaction || page.Offset != 0 {
		t.Fatalf("first artifact page identity = %#v", page)
	}
	if page.NextOffset != 12 || page.Done || page.Content != string([]rune(exactPayload)[:12]) {
		t.Fatalf("first artifact page range = %#v", page)
	}
}

func assertCompleteContextArtifactRead(
	t *testing.T,
	first contextArtifactReadResult,
	second contextArtifactReadResult,
	exactPayload string,
) {
	t.Helper()
	if !second.Done || second.NextOffset != len([]rune(exactPayload)) {
		t.Fatalf("final artifact page = %#v", second)
	}
	if first.Content+second.Content != exactPayload {
		t.Fatalf("artifact pagination lost exact payload: first=%#v second=%#v", first, second)
	}
}

func TestContextArtifactToolRejectsCrossScopeArtifact(t *testing.T) {
	t.Parallel()
	store, _, checkpoint, root := contextRunMiddlewareFixture(t)
	artifact, err := runtimecontext.NewArtifact(
		runtimecontext.ArtifactCompaction, "another-session", checkpoint.Generation,
		"other-source", "secret", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutContextArtifact(t.Context(), artifact); err != nil {
		t.Fatal(err)
	}
	handler := NewContextArtifactToolHandler(store, nil)
	_, err = handler.Execute(t.Context(), tools.ExecutionRequest{
		RunID: root.ExecutionRefID,
		Call: tools.Call{
			ID: "artifact-cross-scope", ToolKey: ContextArtifactToolKey,
			Arguments: mustContextArtifactArguments(t, artifact.ID, 0, 100),
		},
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-scope artifact read error = %v, want ErrConflict", err)
	}
}

type contextArtifactReadResult struct {
	ArtifactID string                      `json:"artifactID"`
	Kind       runtimecontext.ArtifactKind `json:"kind"`
	Offset     int                         `json:"offset"`
	NextOffset int                         `json:"nextOffset"`
	Done       bool                        `json:"done"`
	Content    string                      `json:"content"`
}

func executeContextArtifactRead(
	t *testing.T,
	handler *ContextArtifactToolHandler,
	runID string,
	artifactID string,
	offset int,
	limit int,
) contextArtifactReadResult {
	t.Helper()
	result, err := handler.Execute(t.Context(), tools.ExecutionRequest{
		RunID: runID,
		Call: tools.Call{
			ID: "artifact-read", ToolKey: ContextArtifactToolKey,
			Arguments: mustContextArtifactArguments(t, artifactID, offset, limit),
		},
	})
	if err != nil {
		t.Fatalf("read Context artifact: %v", err)
	}
	var decoded contextArtifactReadResult
	if err = json.Unmarshal(result.Content, &decoded); err != nil {
		t.Fatalf("decode Context artifact result: %v", err)
	}
	return decoded
}

func mustContextArtifactArguments(t *testing.T, artifactID string, offset int, limit int) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(contextArtifactReadInput{ArtifactID: artifactID, Offset: offset, Limit: limit})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
