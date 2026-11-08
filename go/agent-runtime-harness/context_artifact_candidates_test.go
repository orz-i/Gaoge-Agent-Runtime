package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Queryless offsets are permitted for backwards-compatible exact reads, but
// a Host opting into candidate navigation must give a model actionable search
// guidance when a large archive would otherwise encourage linear scanning.
// This advisory never selects an answer, changes evidence, or authorizes reads.
func TestCandidateLongOffsetPagingSuggestsQueryWithoutChangingEvidence(t *testing.T) {
	t.Parallel()
	store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
	doc := strings.Repeat("ordinary audit row; 核验日志;\n", 700) +
		"RELEASE R-17 DEPLOYMENT_CODE=FROM-SOURCE; WINDOW=UTC;\n"
	artifact, err := runtimecontext.NewArtifact(runtimecontext.ArtifactCompaction,
		turn.SessionID, checkpoint.Generation, checkpoint.CoveredThroughSourceID, doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutContextArtifact(t.Context(), artifact); err != nil {
		t.Fatal(err)
	}
	base := NewContextArtifactToolHandler(store, nil)
	candidate := ContextArtifactCandidateToolRegistration(base)
	execute := func(t *testing.T, handler tools.Handler, input contextArtifactReadInput) (struct {
		Content    string `json:"content"`
		SearchHint string `json:"searchHint"`
		NextOffset int    `json:"nextOffset"`
		Done       bool   `json:"done"`
		size       int
	}, error) {
		t.Helper()
		var parsed struct {
			Content    string `json:"content"`
			SearchHint string `json:"searchHint"`
			NextOffset int    `json:"nextOffset"`
			Done       bool   `json:"done"`
			size       int
		}
		body, err := json.Marshal(input)
		if err != nil {
			return parsed, err
		}
		response, err := handler.Execute(t.Context(), tools.ExecutionRequest{RunID: root.ExecutionRefID,
			Call: tools.Call{ID: "queryless-advice", ToolKey: ContextArtifactToolKey, Arguments: body}})
		if err == nil {
			parsed.size = len(response.Content)
			err = json.Unmarshal(response.Content, &parsed)
		}
		return parsed, err
	}
	plain, err := execute(t, base, contextArtifactReadInput{ArtifactID: artifact.ID, Limit: 128})
	if err != nil || plain.SearchHint != "" || plain.Content != string([]rune(doc)[:128]) {
		t.Fatalf("legacy offset reader changed: hint=%q contentLen=%d err=%v", plain.SearchHint, len(plain.Content), err)
	}
	// A large requested page must be bounded by the enhanced Tool policy.
	// Every returned rune remains the original prefix, and the published
	// cursor must advance by exactly that prefix length.
	enhanced, err := execute(t, candidate.Handler, contextArtifactReadInput{ArtifactID: artifact.ID, Limit: 8000})
	if err != nil || !strings.Contains(enhanced.SearchHint, "query") ||
		strings.Contains(enhanced.SearchHint, "R-17") || enhanced.Content == "" ||
		!strings.HasPrefix(doc, enhanced.Content) || enhanced.size > 512 ||
		enhanced.NextOffset != utf8.RuneCountInString(enhanced.Content) ||
		enhanced.NextOffset > 128 || enhanced.Done {
		t.Fatalf("candidate did not preserve bounded original prefix and query advice: hint=%q len=%d bytes=%d next=%d err=%v",
			enhanced.SearchHint, len(enhanced.Content), enhanced.size, enhanced.NextOffset, err)
	}
	withQuery, err := execute(t, candidate.Handler, contextArtifactReadInput{ArtifactID: artifact.ID,
		Query: "RELEASE R-17", Limit: 128})
	if err != nil || withQuery.SearchHint != "" || !strings.HasPrefix(withQuery.Content, "RELEASE R-17") {
		t.Fatalf("query result was obscured or search hint incorrectly inserted: %+v err=%v", withQuery, err)
	}
}

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
			if !strings.Contains(match.Preview, "DEPLOYMENT_CODE") {
				t.Fatalf("preview byte-budget truncation hid matching field: iteration=%d preview=%q", index, match.Preview)
			}
			if match.Offset < 0 || match.Offset >= len(source) || match.Preview == "" ||
				!strings.HasPrefix(string(source[match.Offset:]), match.Preview) ||
				!utf8.ValidString(match.Preview) {
				t.Fatalf("unsafe preview/offset: iteration=%d offset=%d preview=%q", index, match.Offset, match.Preview)
			}
		}
	}
}

// An ambiguous match is only useful when its preview visibly contains the
// matching term. Long indentation and JSON-wrapped transcript lines must not
// hide the literal match behind an unrelated 35-rune prefix.
func TestContextArtifactCandidatePreviewAlwaysIncludesTheMatchingTerm(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
	}{
		{"indented", "header\n" + strings.Repeat(" ", 57) + "RELEASE R-17 DEPLOYMENT_CODE=OK;\n" + strings.Repeat(" ", 61) + "RELEASE R-18 DEPLOYMENT_CODE=DECOY;\n"},
		{"long-field", "prefix:" + strings.Repeat("X", 70) + " RELEASE R-17 TARGET\n" + strings.Repeat("X", 85) + " RELEASE R-18 DECOY\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runes := []rune(tc.payload)
			positions := contextArtifactLiteralMatches(runes, 0, "RELEASE")
			matches, _ := contextArtifactMatchPreviews(runes, positions)
			if len(matches) != 2 {
				t.Fatalf("missing test matches: %+v", matches)
			}
			for _, match := range matches {
				if !strings.Contains(match.Preview, "RELEASE R-") {
					t.Fatalf("candidate preview hid the matched release at offset %d: %q", match.Offset, match.Preview)
				}
				if !strings.HasPrefix(string(runes[match.Offset:]), match.Preview) {
					t.Fatalf("preview offset no longer addresses its exact source: %+v", match)
				}
			}
		})
	}
}

func TestContextArtifactCandidateByteLimitMustNotEraseEveryMatchedLabel(t *testing.T) {
	store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
	var payload strings.Builder
	for _, id := range []string{"R-16", "R-18", "R-17", "R-19"} {
		payload.WriteString(strings.Repeat("🌱中", 28))
		payload.WriteString(" RELEASE " + id + " DEPLOYMENT_CODE=VALUE; WINDOW=UTC;\n")
	}
	artifact, err := runtimecontext.NewArtifact(runtimecontext.ArtifactCompaction, turn.SessionID,
		checkpoint.Generation, checkpoint.CoveredThroughSourceID, payload.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutContextArtifact(t.Context(), artifact); err != nil {
		t.Fatal(err)
	}
	registration := ContextArtifactCandidateToolRegistration(NewContextArtifactToolHandler(store, nil))
	args, _ := json.Marshal(contextArtifactReadInput{ArtifactID: artifact.ID, Query: "RELEASE", Limit: 128})
	result, err := registration.Handler.Execute(t.Context(), tools.ExecutionRequest{
		RunID: root.ExecutionRefID, Call: tools.Call{ID: "long-prefix", ToolKey: ContextArtifactToolKey, Arguments: args},
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Matches []contextArtifactMatch `json:"matches"`
	}
	if err = json.Unmarshal(result.Content, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Matches) != 3 {
		t.Fatalf("unexpected candidate count: %+v", decoded.Matches)
	}
	for i, candidate := range decoded.Matches {
		if !strings.Contains(candidate.Preview, "RELEASE") {
			t.Fatalf("byte-shrinking hid match: %d bytes %d preview %q", i, len(result.Content), candidate.Preview)
		}
	}
}

func TestContextArtifactNormalizedHintPaginationSkipsEntirePreviousMatch(t *testing.T) {
	for _, tc := range []struct {
		name, payload, query string
		expectedNext         int
	}{
		{"overlapping_lowercase", strings.Repeat("a", 28), "AAAA", 12},
		{"separator_spanning", strings.Repeat("a_a_a_a_", 5), "AAAA", 23},
	} {
		t.Run(tc.name, func(t *testing.T) {
			artifact, err := runtimecontext.NewArtifact(runtimecontext.ArtifactCompaction,
				"scope", 1, "source", tc.payload, nil)
			if err != nil {
				t.Fatal(err)
			}
			first, err := buildContextArtifactPage(artifact, contextArtifactReadInput{
				Query: tc.query, Candidates: true, Limit: 128})
			if err != nil {
				t.Fatal(err)
			}
			if len(first.matches) != 3 || !first.moreMatches || first.nextOffset != tc.expectedNext || first.found == nil || *first.found {
				t.Fatalf("normalized hints repeated earlier match or claimed literal authority: %+v", first)
			}
			second, err := buildContextArtifactPage(artifact, contextArtifactReadInput{
				Offset: first.nextOffset, Query: tc.query, Candidates: true, Limit: 128})
			if err != nil {
				t.Fatal(err)
			}
			if second.nextOffset <= first.nextOffset && second.moreMatches {
				t.Fatalf("second normalized hint page did not advance: %+v", second)
			}
		})
	}
}

// Fixed-seed property matrix: enumerate ambiguous source presentations with
// both real and JSON-escaped line boundaries, varied Unicode prelude sizes,
// and several matches. Every candidate must remain reachable exactly once,
// label-visible, source-addressable, and within the 512-byte Tool-result cap.
func TestContextArtifactCandidateSeededPagingInvariants(t *testing.T) {
	rng := rand.New(rand.NewSource(96007))
	for trial := 0; trial < 320; trial++ {
		var source strings.Builder
		count := rng.Intn(8) + 4
		separator := "\n"
		if rng.Intn(2) == 1 {
			separator = `\n`
		}
		for j := 0; j < count; j++ {
			source.WriteString(strings.Repeat("数🌱", rng.Intn(65)))
			fmt.Fprintf(&source, "RELEASE R-%02d DEPLOYMENT_CODE=C%d; WINDOW=UTC;", j+10, j)
			source.WriteString(separator)
		}
		artifact, err := runtimecontext.NewArtifact(runtimecontext.ArtifactCompaction, "scope", 1, "source", source.String(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, test := range []struct{ mode, query string }{{"literal", "RELEASE"}, {"normalized", "release"}} {
			seen := map[int]bool{}
			cursor := 0
			for step := 0; step < count+3; step++ {
				page, err := buildContextArtifactPage(artifact, contextArtifactReadInput{Offset: cursor, Query: test.query, Candidates: true, Limit: 128})
				if err != nil {
					t.Fatal(err)
				}
				serialized, err := contextArtifactExecutionResult("seed", artifact, page)
				if err != nil {
					t.Fatal(err)
				}
				if len(page.matches) != 0 && len(serialized.Content) > contextArtifactResponseMaxBytes {
					t.Fatalf("candidate result escaped 512 bytes: trial=%d mode=%s", trial, test.mode)
				}
				if test.mode == "normalized" && page.found != nil && *page.found {
					t.Fatalf("nonliteral hint reported exact match: trial=%d", trial)
				}
				if len(page.matches) == 0 {
					if test.mode == "literal" && page.found != nil && *page.found {
						// The last singleton is an ordinary exact response.
						if !strings.Contains(page.content, test.query) || seen[page.offset] {
							t.Fatalf("last literal candidate was duplicated or unreadable: trial=%d page=%+v", trial, page)
						}
						seen[page.offset] = true
					}
					break
				}
				for _, candidate := range page.matches {
					if seen[candidate.matchStart] || candidate.matchStart < cursor ||
						candidate.Offset > candidate.matchStart ||
						!strings.HasPrefix(string([]rune(source.String())[candidate.Offset:]), candidate.Preview) ||
						!strings.Contains(candidate.Preview, "RELEASE") {
						t.Fatalf("candidate repeated, offset wrong, or label hidden: trial=%d mode=%s step=%d match=%+v", trial, test.mode, step, candidate)
					}
					seen[candidate.matchStart] = true
				}
				if page.nextOffset <= cursor || page.nextOffset > len([]rune(source.String())) {
					t.Fatalf("pagination did not advance safely: trial=%d mode=%s page=%+v", trial, test.mode, page)
				}
				cursor = page.nextOffset
			}
			if len(seen) != count {
				t.Fatalf("missing or duplicated candidate: trial=%d mode=%s count=%d seen=%d", trial, test.mode, count, len(seen))
			}
		}
	}
}
