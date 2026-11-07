package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const ContextArtifactToolKey = "harness.read_context_artifact"

const (
	contextArtifactDefaultLimit     = 2000
	contextArtifactMaxLimit         = 8000
	contextArtifactMaxSuggestions   = 3
	contextArtifactPreviewRunes     = 35
	contextArtifactResponseMaxBytes = 512
)

type contextArtifactReadInput struct {
	ArtifactID string `json:"artifactID"`
	Offset     int    `json:"offset,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	Query      string `json:"query,omitempty"`
	Candidates bool   `json:"candidates,omitempty"`
}

type contextArtifactPage struct {
	offset      int
	nextOffset  int
	done        bool
	content     string
	totalRunes  int
	found       *bool
	matches     []contextArtifactMatch
	searchMode  string
	moreMatches bool
}

// An authorized, bounded preview, not an assertion that the candidate
// is relevant to the user's task. Offsets always refer to the exact original
// artifact, even for nonliteral search hints.
type contextArtifactMatch struct {
	Offset  int    `json:"offset"`
	Preview string `json:"preview"`
}

// ContextArtifactToolHandler reads exact payloads removed from the inline Context
// Window. Access is constrained to the Harness Turn resolved from the caller's
// durable Runtime ownership lineage and to artifacts in the same Context scope.
type ContextArtifactToolHandler struct {
	store               Store
	relations           ContextRunRelationSource
	allowCandidateHints bool
}

func NewContextArtifactToolHandler(store Store, relations ContextRunRelationSource) *ContextArtifactToolHandler {
	return &ContextArtifactToolHandler{store: store, relations: relations}
}

func (handler *ContextArtifactToolHandler) Execute(
	ctx context.Context,
	request tools.ExecutionRequest,
) (tools.ExecutionResult, error) {
	input, err := handler.parseReadInput(request)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	if input.Candidates && !handler.allowCandidateHints {
		return tools.ExecutionResult{}, tools.NewRecoverableCallError("context_artifact.candidates_not_enabled",
			"Candidate previews were not enabled by the Host for this Tool.", tools.ErrInvalidCall)
	}
	if handler.allowCandidateHints && input.Query != "" {
		// Candidate navigation is the Host-selected contract, not an
		// optional Model compliance trick. Broad/incorrect queries still
		// receive bounded navigation hints even if the Model omits the flag.
		input.Candidates = true
	}
	artifact, err := handler.resolveReadableArtifact(ctx, request.RunID, input.ArtifactID)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	page, err := buildContextArtifactPage(artifact, input)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	return contextArtifactExecutionResult(request.Call.ID, artifact, page)
}

func (handler *ContextArtifactToolHandler) parseReadInput(request tools.ExecutionRequest) (contextArtifactReadInput, error) {
	if !validContextArtifactExecutionRequest(handler, request) {
		return contextArtifactReadInput{}, tools.ErrInvalidCall
	}
	var input contextArtifactReadInput
	if err := json.Unmarshal(request.Call.Arguments, &input); err != nil {
		return contextArtifactReadInput{}, tools.NewRecoverableCallError("context_artifact.invalid_input", "invalid Context artifact read input", err)
	}
	input.ArtifactID = strings.TrimSpace(input.ArtifactID)
	if input.ArtifactID == "" || input.Offset < 0 || input.Limit < 0 || utf8.RuneCountInString(input.Query) > 256 ||
		input.Candidates && strings.TrimSpace(input.Query) == "" {
		return contextArtifactReadInput{}, tools.NewRecoverableCallError("context_artifact.invalid_input", "invalid Context artifact read range", tools.ErrInvalidCall)
	}
	return input, nil
}

func validContextArtifactExecutionRequest(
	handler *ContextArtifactToolHandler,
	request tools.ExecutionRequest,
) bool {
	return handler != nil && handler.store != nil && strings.TrimSpace(request.RunID) != "" &&
		strings.TrimSpace(request.Call.ID) != "" && request.Call.ToolKey == ContextArtifactToolKey &&
		json.Valid(request.Call.Arguments)
}

func (handler *ContextArtifactToolHandler) resolveReadableArtifact(
	ctx context.Context,
	runID string,
	artifactID string,
) (runtimecontext.Artifact, error) {
	// The exact archive is part of the same Context isolation boundary as
	// the active window. An isolated Specialist or its descendants may not
	// borrow a Supervisor Turn merely by knowing an Artifact ID, even if
	// the Host explicitly exposes this read-only Tool to that Run.
	turn, _, isolated, err := resolveContextRunAuthority(ctx, handler.store, handler.relations, runID)
	if err != nil {
		return runtimecontext.Artifact{}, err
	}
	if isolated {
		return runtimecontext.Artifact{}, ErrConflict
	}
	artifact, err := handler.store.GetContextArtifact(ctx, artifactID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			message, checkpointErr := handler.artifactNotFoundGuidance(ctx, turn)
			if checkpointErr != nil {
				return runtimecontext.Artifact{}, checkpointErr
			}
			return runtimecontext.Artifact{}, tools.NewRecoverableCallError(
				"context_artifact.not_found",
				message,
				err,
			)
		}
		return runtimecontext.Artifact{}, err
	}
	// The Store read is untrusted until the returned object's exact identity
	// and content hash match this request. Scope-only checks permit a valid
	// different same-session artifact (or corrupted bytes) to escape.
	if artifact.ID != artifactID || !runtimecontext.ValidArtifact(artifact) || artifact.ScopeID != turn.SessionID {
		return runtimecontext.Artifact{}, ErrConflict
	}
	return artifact, nil
}

func (handler *ContextArtifactToolHandler) artifactNotFoundGuidance(ctx context.Context, turn Turn) (string, error) {
	const guidance = "Context artifact was not found. Copy the exact artifact_id from the context checkpoint or compacted tool result, then retry. Do not guess IDs."
	if strings.TrimSpace(turn.ContextCheckpointID) == "" {
		return guidance, nil
	}
	checkpoint, err := handler.store.GetContextCheckpoint(ctx, turn.ContextCheckpointID)
	if err != nil {
		return "", err
	}
	if checkpoint.ScopeID != turn.SessionID || checkpoint.ID != turn.ContextCheckpointID ||
		!runtimecontext.ValidCheckpoint(checkpoint) || !sameContextCheckpointRef(checkpoint, turn.ContextRef) {
		return "", ErrConflict
	}
	ids := checkpoint.ArtifactIDs
	if len(ids) > 16 {
		ids = ids[:16]
	}
	if len(ids) != 0 {
		return guidance + " Exact IDs from the current checkpoint: " + strings.Join(ids, ", ") + ".", nil
	}
	return guidance, nil
}

func buildContextArtifactPage(
	artifact runtimecontext.Artifact,
	input contextArtifactReadInput,
) (contextArtifactPage, error) {
	limit := input.Limit
	if limit <= 0 {
		limit = contextArtifactDefaultLimit
	}
	if limit > contextArtifactMaxLimit {
		limit = contextArtifactMaxLimit
	}
	runes := []rune(contextArtifactPayload(artifact))
	if input.Offset > len(runes) {
		return contextArtifactPage{}, tools.NewRecoverableCallError("context_artifact.invalid_range", "Context artifact offset is past the end", tools.ErrInvalidCall)
	}
	var found *bool
	var matches []contextArtifactMatch
	var searchMode string
	var moreMatches bool
	if input.Query != "" {
		var positions []int
		if input.Candidates {
			positions = contextArtifactLiteralMatches(runes, input.Offset, input.Query)
		} else {
			// Preserve the original single-literal-search cost and semantics
			// for existing Hosts that did not opt into candidate navigation.
			suffix := string(runes[input.Offset:])
			if at := strings.Index(suffix, input.Query); at >= 0 {
				positions = []int{input.Offset + utf8.RuneCountInString(suffix[:at])}
			}
		}
		matched := len(positions) > 0
		found = &matched
		if !matched {
			if !input.Candidates {
				return contextArtifactPage{offset: input.Offset, nextOffset: len(runes),
					totalRunes: len(runes), done: true, found: found}, nil
			}
			// Literal search remains exact and case-sensitive. Nonliteral
			// hints are explicitly marked so they cannot masquerade as
			// proven exact matches or alter returned source bytes.
			positions = contextArtifactNormalizedHints(runes, input.Offset, input.Query)
			if len(positions) > 0 {
				matches, moreMatches = contextArtifactMatchPreviews(runes, positions)
				searchMode = "normalized_hints"
			}
			next := len(runes)
			if moreMatches {
				// A hint is not an exact result, but later hints still
				// need a usable forward-only cursor rather than done=true.
				next = positions[len(matches)-1] + 1
			}
			return contextArtifactPage{offset: input.Offset, nextOffset: next, totalRunes: len(runes),
				done: !moreMatches, found: found, matches: matches, searchMode: searchMode, moreMatches: moreMatches}, nil
		}
		input.Offset = positions[0]
		if input.Candidates && len(positions) > 1 {
			matches, moreMatches = contextArtifactMatchPreviews(runes, positions)
			searchMode = "literal_candidates"
			// Candidate discovery is navigation, not a redundant first-page
			// read. The bounded list must fit a small Context Tool result.
			// Callers explicitly re-read the chosen exact original offset.
			next := positions[len(matches)-1] + utf8.RuneCountInString(input.Query)
			return contextArtifactPage{offset: input.Offset, nextOffset: next,
				done: next == len(runes), totalRunes: len(runes), found: found,
				matches: matches, searchMode: searchMode, moreMatches: moreMatches}, nil
		}
	}
	end := input.Offset + limit
	if end > len(runes) {
		end = len(runes)
	}
	return contextArtifactPage{
		offset: input.Offset, nextOffset: end, done: end == len(runes), content: string(runes[input.Offset:end]),
		totalRunes: len(runes), found: found, matches: matches, searchMode: searchMode, moreMatches: moreMatches,
	}, nil
}

// Only a few earliest positions are needed for actionable navigation. A
// fourth occurrence proves there are more without an unbounded result list.
func contextArtifactLiteralMatches(runes []rune, offset int, query string) []int {
	var positions []int
	cursor := offset
	for cursor < len(runes) && len(positions) <= contextArtifactMaxSuggestions {
		suffix := string(runes[cursor:])
		at := strings.Index(suffix, query)
		if at < 0 {
			break
		}
		position := cursor + utf8.RuneCountInString(suffix[:at])
		positions = append(positions, position)
		cursor = position + utf8.RuneCountInString(query)
	}
	return positions
}

// Search suggestions tolerate ordinary case and separator mistakes without
// changing the exact literal-match flag. This is navigation only: the caller
// must explicitly read the original offset to obtain authoritative text.
func contextArtifactNormalizedHints(runes []rune, offset int, query string) []int {
	normalize := func(r rune) (rune, bool) {
		if r == '_' || r == '-' || unicode.IsSpace(r) {
			return 0, false
		}
		return unicode.ToLower(r), true
	}
	var needle []rune
	for _, r := range query {
		if lowered, ok := normalize(r); ok {
			needle = append(needle, lowered)
		}
	}
	// One-letter fuzzy queries on long archives produce unhelpful noise.
	// This is a hint-only feature, never an automatic fuzzy read.
	if len(needle) < 3 {
		return nil
	}
	var normalized []rune
	var originalOffsets []int
	for i := offset; i < len(runes); i++ {
		if lowered, ok := normalize(runes[i]); ok {
			normalized, originalOffsets = append(normalized, lowered), append(originalOffsets, i)
		}
	}
	var positions []int
	cursor := 0
	for cursor < len(normalized) && len(positions) <= contextArtifactMaxSuggestions {
		at := strings.Index(string(normalized[cursor:]), string(needle))
		if at < 0 {
			break
		}
		position := cursor + utf8.RuneCountInString(string(normalized[cursor:])[:at])
		positions = append(positions, originalOffsets[position])
		cursor = position + len(needle)
	}
	return positions
}

func contextArtifactMatchPreviews(runes []rune, positions []int) ([]contextArtifactMatch, bool) {
	more := len(positions) > contextArtifactMaxSuggestions
	if more {
		positions = positions[:contextArtifactMaxSuggestions]
	}
	result := make([]contextArtifactMatch, 0, len(positions))
	for _, position := range positions {
		start := position
		for start > 0 && position-start < 96 {
			if runes[start-1] == '\n' || start > 1 && runes[start-2] == '\\' && runes[start-1] == 'n' {
				break
			}
			start--
		}
		// Do not let an extremely long line hide the actual match.
		if position-start >= 96 {
			start = position
			if start > 20 {
				start -= 20
			}
		}
		end := start
		for end < len(runes) && end-start < contextArtifactPreviewRunes && runes[end] != '\n' {
			if end+1 < len(runes) && runes[end] == '\\' && runes[end+1] == 'n' {
				break
			}
			end++
		}
		// Offset points to the start of the bounded preview, not
		// necessarily the query itself. This lets a caller re-read the
		// exact record prefix when the match is inside a field name.
		result = append(result, contextArtifactMatch{Offset: start, Preview: string(runes[start:end])})
	}
	return result, more
}

func contextArtifactExecutionResult(
	executionID string,
	artifact runtimecontext.Artifact,
	page contextArtifactPage,
) (tools.ExecutionResult, error) {
	encode := func() ([]byte, error) {
		return json.Marshal(struct {
			ArtifactID  string                      `json:"artifactID"`
			Kind        runtimecontext.ArtifactKind `json:"kind"`
			ContentHash string                      `json:"contentHash"`
			Offset      int                         `json:"offset"`
			NextOffset  int                         `json:"nextOffset"`
			Done        bool                        `json:"done"`
			Content     string                      `json:"content"`
			TotalRunes  int                         `json:"totalRunes"`
			Found       *bool                       `json:"found,omitempty"`
			Matches     []contextArtifactMatch      `json:"matches,omitempty"`
			SearchMode  string                      `json:"searchMode,omitempty"`
			MoreMatches bool                        `json:"moreMatches,omitempty"`
		}{
			ArtifactID: artifact.ID, Kind: artifact.Kind, ContentHash: artifact.ContentHash,
			Offset: page.offset, NextOffset: page.nextOffset, Done: page.done, Content: page.content,
			TotalRunes: page.totalRunes, Found: page.found, Matches: page.matches,
			SearchMode: page.searchMode, MoreMatches: page.moreMatches,
		})
	}
	content, err := encode()
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	// Candidate responses must fit the example's 512-byte Context Tool
	// result ceiling, including metadata and multibyte Unicode. Otherwise
	// candidates would be compacted immediately and become unusable.
	for len(page.matches) != 0 && len(content) > contextArtifactResponseMaxBytes {
		progress := false
		for index := range page.matches {
			runes := []rune(page.matches[index].Preview)
			if len(runes) != 0 {
				page.matches[index].Preview = string(runes[:len(runes)-1])
				progress = true
			}
		}
		if !progress {
			return tools.ExecutionResult{}, tools.ErrInvalidCall
		}
		content, err = encode()
		if err != nil {
			return tools.ExecutionResult{}, err
		}
	}
	return tools.ExecutionResult{
		Content: content,
		Receipt: tools.Receipt{ExecutionID: executionID, Disposition: "read"},
	}, nil
}

func contextArtifactPayload(artifact runtimecontext.Artifact) string {
	if len(artifact.ContentJSON) != 0 {
		return string(artifact.ContentJSON)
	}
	return artifact.Content
}

func ContextArtifactToolRegistration(handler *ContextArtifactToolHandler) tools.Registration {
	return tools.Registration{
		Definition: tools.Definition{
			Key:         ContextArtifactToolKey,
			Name:        "read_context_artifact",
			Description: "Read exact bounded text from a Context artifact. To recover a known key or phrase, set query for literal search instead of scanning every page. Search starts at offset and returns found plus an excerpt starting at the match. Offsets and totalRunes count Unicode characters, not bytes. Copy an exact referenced artifact ID. Continue with nextOffset until done; a missing match applies only to that artifact.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["artifactID"],"properties":{"artifactID":{"type":"string","minLength":1,"maxLength":128},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":8000},"query":{"type":"string","minLength":1,"maxLength":256}}}`),
		},
		Handler: handler,
	}
}

func ContextArtifactToolPolicySnapshot() ToolPolicySnapshot {
	return ToolPolicySnapshot{
		Key: ContextArtifactToolKey, DefinitionVersion: harnessToolDefinitionVersion,
		ApprovalCapability: approvalCapabilityPerCall, ApprovalMode: approvalModeNever,
		RiskLevel: toolRiskLevelLow, SideEffectLevel: "read", IdempotencyMode: toolIdempotencyRequestKey,
	}
}

// ContextArtifactCandidateToolRegistration is an explicit, Host-selected
// enhancement of the existing read-only artifact Tool. The ordinary reader
// and its task/Tool manifest stay unchanged. The handler still enforces the
// same Run -> Turn -> artifact authority, exact byte/hash identity and scope.
// Only this separate registration accepts candidates=true; no implicit fuzzy
// lookup is performed when the caller requests an exact read.
func ContextArtifactCandidateToolRegistration(handler *ContextArtifactToolHandler) tools.Registration {
	registration := ContextArtifactToolRegistration(handler)
	registration.Definition.Description = "Read bounded exact Context artifact text. The Host enables candidate navigation on every query in this enhanced registration (candidates=true is optional): ambiguous queries yield at most 3 short candidate previews and original offsets. 'literal_candidates' are actual case-sensitive matches; 'normalized_hints' are case/separator-insensitive navigation suggestions, NOT confirmed literal matches (found remains false). Re-read an exact original offset before using any facts. Use nextOffset to continue; no answer ranking, guessing or scope bypass is performed."
	registration.Definition.InputSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["artifactID"],"properties":{"artifactID":{"type":"string","minLength":1,"maxLength":128},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":8000},"query":{"type":"string","minLength":1,"maxLength":256},"candidates":{"type":"boolean"}}}`)
	if handler != nil {
		isolated := *handler
		isolated.allowCandidateHints = true
		registration.Handler = &isolated
	}
	return registration
}

func ContextArtifactCandidateToolPolicySnapshot() ToolPolicySnapshot {
	policy := ContextArtifactToolPolicySnapshot()
	policy.DefinitionVersion = "harness-candidate-search-v1"
	return policy
}
