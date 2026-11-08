package harness

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Search hints are strictly informational; a large offset-only request is
// still scoped to the original exact artifact. Seeded data exercises Unicode,
// source offsets, quoted JSON and end-of-file behavior without an external LLM.
func TestCandidateQuerylessHintPagingSeededInvariants(t *testing.T) {
	t.Parallel()
	store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
	handler := ContextArtifactCandidateToolRegistration(NewContextArtifactToolHandler(store, nil)).Handler
	for seed := 0; seed < 80; seed++ {
		prefix := strings.Repeat(fmt.Sprintf("记录🌱 index=%03d; unrelated «%d»\n", seed, seed), 440+seed%20)
		document := prefix + "SOURCE KEY=truth-from-artifact; marker=验收\n"
		var body json.RawMessage
		if seed%2 == 1 {
			encoded, err := json.Marshal(map[string]string{"source": document})
			if err != nil {
				t.Fatal(err)
			}
			body = encoded
			document = string(encoded)
		}
		artifact, err := runtimecontext.NewArtifact(runtimecontext.ArtifactCompaction,
			turn.SessionID, checkpoint.Generation, checkpoint.CoveredThroughSourceID,
			document, body)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = store.PutContextArtifact(t.Context(), artifact); err != nil {
			t.Fatal(err)
		}
		runes := []rune(document)
		if len(runes) <= contextArtifactMaxLimit {
			t.Fatalf("invalid seeded source length")
		}
		offsets := []int{0, 100 + seed%97, len(runes) - 70, len(runes)}
		for _, offset := range offsets {
			input, err := json.Marshal(contextArtifactReadInput{ArtifactID: artifact.ID, Offset: offset, Limit: 8000})
			if err != nil {
				t.Fatal(err)
			}
			result, err := handler.Execute(t.Context(), tools.ExecutionRequest{RunID: root.ExecutionRefID,
				Call: tools.Call{ID: fmt.Sprintf("hint-%d-%d", seed, offset), ToolKey: ContextArtifactToolKey, Arguments: input}})
			if err != nil {
				t.Fatalf("seed=%d offset=%d: %v", seed, offset, err)
			}
			var page struct {
				Offset     int    `json:"offset"`
				NextOffset int    `json:"nextOffset"`
				TotalRunes int    `json:"totalRunes"`
				Done       bool   `json:"done"`
				Content    string `json:"content"`
				SearchHint string `json:"searchHint"`
				Found      *bool  `json:"found"`
				SearchMode string `json:"searchMode"`
			}
			if err = json.Unmarshal(result.Content, &page); err != nil {
				t.Fatal(err)
			}
			l := utf8.RuneCountInString(page.Content)
			if len(result.Content) > contextArtifactResponseMaxBytes || page.SearchHint != contextArtifactLongPageSearchHint ||
				page.Offset != offset || page.TotalRunes != len(runes) || page.Found != nil || page.SearchMode != "" ||
				page.NextOffset != offset+l || page.NextOffset > len(runes) ||
				page.Done != (page.NextOffset == len(runes)) || l > 128 ||
				string(runes[offset:page.NextOffset]) != page.Content ||
				(offset < len(runes) && l == 0) {
				t.Fatalf("invalid seeded long hint: seed=%d offset=%d next=%d contentRunes=%d outputBytes=%d done=%t",
					seed, offset, page.NextOffset, l, len(result.Content), page.Done)
			}
		}
	}
}

func TestCandidateQuerylessSmallArchivePreservesOriginalPageWithoutHint(t *testing.T) {
	t.Parallel()
	store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
	small := strings.Repeat("原始记录🌱", 12)
	artifact, err := runtimecontext.NewArtifact(runtimecontext.ArtifactCompaction,
		turn.SessionID, checkpoint.Generation, checkpoint.CoveredThroughSourceID, small, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutContextArtifact(t.Context(), artifact); err != nil {
		t.Fatal(err)
	}
	reader := ContextArtifactCandidateToolRegistration(NewContextArtifactToolHandler(store, nil))
	args, err := json.Marshal(contextArtifactReadInput{ArtifactID: artifact.ID, Limit: 8000})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.Handler.Execute(t.Context(), tools.ExecutionRequest{RunID: root.ExecutionRefID,
		Call: tools.Call{ID: "small", ToolKey: ContextArtifactToolKey, Arguments: args}})
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Content    string `json:"content"`
		SearchHint string `json:"searchHint"`
		Done       bool   `json:"done"`
	}
	if err = json.Unmarshal(result.Content, &page); err != nil {
		t.Fatal(err)
	}
	if page.Content != small || page.SearchHint != "" || !page.Done {
		t.Fatalf("small candidate page was modified: %+v", page)
	}
}
