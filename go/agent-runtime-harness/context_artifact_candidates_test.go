package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestContextArtifactCandidatesExposeDistinctMatchesWithoutChangingLiteralReader(t *testing.T) {
	t.Parallel()
	store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
	prefix := strings.Repeat("中文🌱 ordinary log entry\n", 300)
	record := "RELEASE R-16 DEPLOYMENT_CODE=DECOY; WINDOW=ignore;\n" +
		"RELEASE R-18 DEPLOYMENT_CODE=DECOY; WINDOW=ignore;\n" +
		"RELEASE R-17 DEPLOYMENT_CODE=RIGHT; WINDOW=Tuesday;\n" +
		"RELEASE R-19 DEPLOYMENT_CODE=STALE; WINDOW=ignore;\n"
	payload := prefix + record + prefix
	artifact, err := runtimecontext.NewArtifact(runtimecontext.ArtifactCompaction, turn.SessionID,
		checkpoint.Generation, checkpoint.CoveredThroughSourceID, payload, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutContextArtifact(t.Context(), artifact); err != nil {
		t.Fatal(err)
	}
	handler := NewContextArtifactToolHandler(store, nil)
	legacy := executeContextArtifactReadInput(t, handler, root.ExecutionRefID, contextArtifactReadInput{
		ArtifactID: artifact.ID, Query: "RELEASE", Limit: 128,
	})
	if legacy.Found == nil || !*legacy.Found ||
		!strings.HasPrefix(legacy.Content, "RELEASE R-16") {
		t.Fatalf("old exact reader changed %#v", legacy)
	}
	// Direct calls cannot sneak candidate hints through the legacy registration.
	rejected := tools.ExecutionRequest{RunID: root.ExecutionRefID,
		Call: tools.Call{ID: "not-enabled", ToolKey: ContextArtifactToolKey,
			Arguments: mustContextArtifactArguments(t, artifact.ID, 0, 128)}}
	rejected.Call.Arguments, _ = json.Marshal(contextArtifactReadInput{ArtifactID: artifact.ID,
		Query: "RELEASE", Limit: 128, Candidates: true})
	_, err = handler.Execute(t.Context(), rejected)
	if code, _, ok := tools.RecoverableCallErrorInfo(err); !ok || code != "context_artifact.candidates_not_enabled" {
		t.Fatalf("legacy registration allowed search hints: %v", err)
	}

	enhanced := ContextArtifactCandidateToolRegistration(handler)
	// Candidate discovery must never silently run with an empty query.
	badArguments, _ := json.Marshal(contextArtifactReadInput{ArtifactID: artifact.ID, Candidates: true})
	_, badErr := enhanced.Handler.Execute(t.Context(), tools.ExecutionRequest{
		RunID: root.ExecutionRefID,
		Call:  tools.Call{ID: "empty-query", ToolKey: ContextArtifactToolKey, Arguments: badArguments},
	})
	if code, _, ok := tools.RecoverableCallErrorInfo(badErr); !ok || code != "context_artifact.invalid_input" {
		t.Fatalf("empty query triggered unbounded candidate lookup: %v", badErr)
	}
	if enhanced.Definition.Key != ContextArtifactToolKey ||
		!strings.Contains(enhanced.Definition.Description, "candidates=true") ||
		!bytes.Contains(enhanced.Definition.InputSchema, []byte("\"candidates\"")) {
		t.Fatal("candidate reader did not declare its separate input capability")
	}
	if ContextArtifactToolPolicySnapshot().DefinitionVersion ==
		ContextArtifactCandidateToolPolicySnapshot().DefinitionVersion {
		t.Fatal("alternate Tool schema reused the ordinary definition version")
	}
	req := tools.ExecutionRequest{RunID: root.ExecutionRefID,
		Call: tools.Call{ID: "candidate-search", ToolKey: ContextArtifactToolKey}}
	req.Call.Arguments, _ = json.Marshal(contextArtifactReadInput{
		ArtifactID: artifact.ID, Query: "RELEASE", Limit: 128,
	})
	response, err := enhanced.Handler.Execute(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Found       bool                   `json:"found"`
		Content     string                 `json:"content"`
		SearchMode  string                 `json:"searchMode"`
		MoreMatches bool                   `json:"moreMatches"`
		Matches     []contextArtifactMatch `json:"matches"`
	}
	if err = json.Unmarshal(response.Content, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Found || out.Content != "" || out.SearchMode != "literal_candidates" || len(out.Matches) != 3 ||
		!out.MoreMatches || len(response.Content) > 512 {
		t.Fatalf("unbounded or missing disambiguation: len=%d values=%+v", len(response.Content), out)
	}
	for i, label := range []string{"R-16", "R-18", "R-17"} {
		m := out.Matches[i]
		if !strings.Contains(m.Preview, label) || len([]rune(m.Preview)) > contextArtifactPreviewRunes {
			t.Fatalf("candidate %d did not identify distinct record: %+v", i, m)
		}
		literal := executeContextArtifactReadInput(t, handler, root.ExecutionRefID, contextArtifactReadInput{
			ArtifactID: artifact.ID, Offset: m.Offset, Limit: 110,
		})
		if !strings.HasPrefix(literal.Content, "RELEASE "+label) {
			t.Fatalf("candidate offset no longer maps to original evidence: %+v", literal)
		}
	}
	// The continuation offset skips the displayed matches. It can find
	// later occurrences without silently repeating the first decoy.
	req.Call.ID = "candidate-continue"
	// The cursor comes from the actual Tool response, never a guessed offset.
	var raw struct {
		NextOffset int `json:"nextOffset"`
	}
	if err = json.Unmarshal(response.Content, &raw); err != nil {
		t.Fatal(err)
	}
	req.Call.Arguments, _ = json.Marshal(contextArtifactReadInput{
		ArtifactID: artifact.ID, Query: "RELEASE", Offset: raw.NextOffset,
		Limit: 128, Candidates: true,
	})
	continued, continuationErr := enhanced.Handler.Execute(t.Context(), req)
	if continuationErr != nil {
		t.Fatal(continuationErr)
	}
	var continuation struct {
		Content string `json:"content"`
		Found   bool   `json:"found"`
	}
	if err = json.Unmarshal(continued.Content, &continuation); err != nil {
		t.Fatal(err)
	}
	if !continuation.Found || !strings.HasPrefix(continuation.Content, "RELEASE R-19") {
		t.Fatalf("candidate pagination repeated an earlier hit: %+v", continuation)
	}
	// Normalized hint covers common casing/underscores but NEVER changes found.
	req.Call.ID = "candidate-hints"
	req.Call.Arguments, _ = json.Marshal(contextArtifactReadInput{
		ArtifactID: artifact.ID, Query: "deploymentCode", Limit: 128,
	})
	response, err = enhanced.Handler.Execute(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	out = struct {
		Found       bool                   `json:"found"`
		Content     string                 `json:"content"`
		SearchMode  string                 `json:"searchMode"`
		MoreMatches bool                   `json:"moreMatches"`
		Matches     []contextArtifactMatch `json:"matches"`
	}{}
	if err = json.Unmarshal(response.Content, &out); err != nil {
		t.Fatal(err)
	}
	if out.Found || out.Content != "" || out.SearchMode != "normalized_hints" ||
		len(out.Matches) != 3 || !out.MoreMatches || !strings.Contains(out.Matches[2].Preview, "R-17") || len(response.Content) > 512 {
		t.Fatalf("hint incorrectly became authoritative literal evidence: %+v", out)
	}
	var hintCursor struct {
		NextOffset int  `json:"nextOffset"`
		Done       bool `json:"done"`
	}
	if err = json.Unmarshal(response.Content, &hintCursor); err != nil {
		t.Fatal(err)
	}
	if hintCursor.Done || hintCursor.NextOffset <= out.Matches[2].Offset {
		t.Fatalf("ambiguous normalized hint did not supply a usable cursor: %+v", hintCursor)
	}
	req.Call.ID = "more-normalized-hints"
	req.Call.Arguments, _ = json.Marshal(contextArtifactReadInput{
		ArtifactID: artifact.ID, Query: "deploymentCode", Offset: hintCursor.NextOffset,
		Limit: 128,
	})
	moreHints, hintErr := enhanced.Handler.Execute(t.Context(), req)
	if hintErr != nil {
		t.Fatal(hintErr)
	}
	var nextHint struct {
		Found   bool                   `json:"found"`
		Matches []contextArtifactMatch `json:"matches"`
	}
	if err = json.Unmarshal(moreHints.Content, &nextHint); err != nil {
		t.Fatal(err)
	}
	if nextHint.Found || len(nextHint.Matches) != 1 || !strings.Contains(nextHint.Matches[0].Preview, "R-19") {
		t.Fatalf("normalized hints did not resume after earlier candidates: %+v", nextHint)
	}
}

func TestContextArtifactCandidateSearchDoesNotWeakenRunAuthority(t *testing.T) {
	t.Parallel()
	store, _, _, root := contextRunMiddlewareFixture(t)
	handler := NewContextArtifactToolHandler(store, nil)
	enhanced := ContextArtifactCandidateToolRegistration(handler)
	foreign, err := runtimecontext.NewArtifact(runtimecontext.ArtifactCompaction,
		"foreign-session", 1, "source", "SECRET-FOREIGN RELEASE R-17", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutContextArtifact(t.Context(), foreign); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(contextArtifactReadInput{
		ArtifactID: foreign.ID, Query: "RELEASE", Limit: 128, Candidates: true,
	})
	_, err = enhanced.Handler.Execute(context.Background(), tools.ExecutionRequest{
		RunID: root.ExecutionRefID, Call: tools.Call{ID: "foreign-test", ToolKey: ContextArtifactToolKey, Arguments: args},
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("unauthorized candidate access: %v", err)
	}
}

// Candidate previews are advisory even with Unicode-heavy documents and long
// headers. They must remain valid source slices, rune-offset-addressable and
// physically small enough to survive the real 512-byte Tool Context ceiling.
func TestContextArtifactCandidatesKeepUTF8AndOutputWithinBudget(t *testing.T) {
	t.Parallel()
	store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
	handler := NewContextArtifactToolHandler(store, nil)
	enhanced := ContextArtifactCandidateToolRegistration(handler)
	for index := 0; index < 80; index++ {
		prefix := strings.Repeat("记录🌱普通检查; 翻訳済み;\n", index%9+1)
		doc := prefix + strings.Repeat("字", index%27) +
			"RELEASE R-16 DEPLOYMENT_CODE=诱饵甲; WINDOW=周一;\n" +
			"RELEASE R-18 DEPLOYMENT_CODE=诱饵乙; WINDOW=周二;\n" +
			"RELEASE R-17 DEPLOYMENT_CODE=目标丙; WINDOW=周三;\n" +
			"RELEASE R-19 DEPLOYMENT_CODE=过期丁; WINDOW=周四;\n"
		artifact, err := runtimecontext.NewArtifact(runtimecontext.ArtifactCompaction, turn.SessionID,
			checkpoint.Generation, checkpoint.CoveredThroughSourceID, doc, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = store.PutContextArtifact(t.Context(), artifact); err != nil {
			t.Fatal(err)
		}
		args, _ := json.Marshal(contextArtifactReadInput{
			ArtifactID: artifact.ID, Query: "DEPLOYMENT_CODE", Limit: 128, Candidates: true,
		})
		result, err := enhanced.Handler.Execute(t.Context(), tools.ExecutionRequest{
			RunID: root.ExecutionRefID, Call: tools.Call{
				ID: "unicode-bounded", ToolKey: ContextArtifactToolKey, Arguments: args,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		var parsed struct {
			Content string                 `json:"content"`
			Found   bool                   `json:"found"`
			Matches []contextArtifactMatch `json:"matches"`
		}
		if err = json.Unmarshal(result.Content, &parsed); err != nil {
			t.Fatal(err)
		}
		if len(result.Content) > contextArtifactResponseMaxBytes ||
			len(parsed.Matches) != contextArtifactMaxSuggestions || !parsed.Found ||
			parsed.Content != "" {
			t.Fatalf("candidate output exceeded budget or lost ambiguity: iteration=%d bytes=%d", index, len(result.Content))
		}
		source := []rune(doc)
		for _, match := range parsed.Matches {
			if match.Offset < 0 || match.Offset >= len(source) || match.Preview == "" ||
				!strings.HasPrefix(string(source[match.Offset:]), match.Preview) ||
				!utf8.ValidString(match.Preview) {
				t.Fatalf("unsafe preview/offset: iteration=%d offset=%d preview=%q", index, match.Offset, match.Preview)
			}
		}
	}
}
