package harness

import (
	"context"
	"encoding/json"
	"strings"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const ContextArtifactToolKey = "harness.read_context_artifact"

const (
	contextArtifactDefaultLimit = 2000
	contextArtifactMaxLimit     = 8000
)

type contextArtifactReadInput struct {
	ArtifactID string `json:"artifactID"`
	Offset     int    `json:"offset,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type contextArtifactPage struct {
	offset     int
	nextOffset int
	done       bool
	content    string
}

// ContextArtifactToolHandler reads exact payloads removed from the inline Context
// Window. Access is constrained to the Harness Turn resolved from the caller's
// durable Runtime ownership lineage and to artifacts in the same Context scope.
type ContextArtifactToolHandler struct {
	store     Store
	relations ContextRunRelationSource
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
	if input.ArtifactID == "" || input.Offset < 0 || input.Limit < 0 {
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
	turn, _, err := resolveContextTurnForRun(ctx, handler.store, handler.relations, runID)
	if err != nil {
		return runtimecontext.Artifact{}, err
	}
	artifact, err := handler.store.GetContextArtifact(ctx, artifactID)
	if err != nil {
		return runtimecontext.Artifact{}, err
	}
	if strings.TrimSpace(artifact.ScopeID) == "" || artifact.ScopeID != turn.SessionID {
		return runtimecontext.Artifact{}, ErrConflict
	}
	return artifact, nil
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
	end := input.Offset + limit
	if end > len(runes) {
		end = len(runes)
	}
	return contextArtifactPage{
		offset: input.Offset, nextOffset: end, done: end == len(runes), content: string(runes[input.Offset:end]),
	}, nil
}

func contextArtifactExecutionResult(
	executionID string,
	artifact runtimecontext.Artifact,
	page contextArtifactPage,
) (tools.ExecutionResult, error) {
	content, err := json.Marshal(struct {
		ArtifactID  string                      `json:"artifactID"`
		Kind        runtimecontext.ArtifactKind `json:"kind"`
		ContentHash string                      `json:"contentHash"`
		Offset      int                         `json:"offset"`
		NextOffset  int                         `json:"nextOffset"`
		Done        bool                        `json:"done"`
		Content     string                      `json:"content"`
	}{
		ArtifactID: artifact.ID, Kind: artifact.Kind, ContentHash: artifact.ContentHash,
		Offset: page.offset, NextOffset: page.nextOffset, Done: page.done, Content: page.content,
	})
	if err != nil {
		return tools.ExecutionResult{}, err
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
			Description: "Read an exact bounded chunk from a Context artifact referenced by a compacted conversation or Tool result. Continue with nextOffset until done.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["artifactID"],"properties":{"artifactID":{"type":"string","minLength":1,"maxLength":128},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":8000}}}`),
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
