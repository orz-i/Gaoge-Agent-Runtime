package harness

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"

	runtimecontext "github.com/orz-i/Gaoge/sdk/go/agent-runtime/context"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/model"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

// ContextSeed is the complete host transcript ancestry for one Harness Turn.
// SourcePath has no semantic message-count limit; repository paging is an I/O concern only.
type ContextSeed struct {
	SourcePath         []string               `json:"sourcePath"`
	Instructions       string                 `json:"instructions,omitempty"`
	Entries            []runtimecontext.Entry `json:"entries"`
	ResetCacheIdentity bool                   `json:"resetCacheIdentity,omitempty"`
}

// ContextCheckpointRef is the content-free durable reference stored on a Harness Turn.
type ContextCheckpointRef struct {
	ID                     string `json:"id"`
	Generation             int    `json:"generation"`
	Revision               int    `json:"revision"`
	LineageHash            string `json:"lineageHash"`
	CoveredThroughSourceID string `json:"coveredThroughSourceID"`
	ContentHash            string `json:"contentHash"`
}

// ContextWindowAccess controls whether one execution may advance the durable Context Window.
// Advanced capability sampling is read-only: it can consume the Conversation prefix without
// projecting planner/child prompts back into the host transcript lineage.
type ContextWindowAccess string

const (
	ContextWindowOwner    ContextWindowAccess = "owner"
	ContextWindowReadOnly ContextWindowAccess = "read_only"
)

// ContextWindowBinding identifies the Harness Turn that owns the execution-scoped Context head.
type ContextWindowBinding struct {
	TurnID string
	Access ContextWindowAccess
}

type contextCheckpointKey struct{}
type contextWindowBindingKey struct{}

type contextWindowState struct {
	mu         sync.RWMutex
	checkpoint runtimecontext.Checkpoint
}

// NewContextWindowMiddleware injects the exact active Context Window carried by Harness execution.
func NewContextWindowMiddleware() plugin.ModelMiddleware { return contextWindowMiddleware{} }

type contextWindowMiddleware struct{}

func (contextWindowMiddleware) Name() string { return "harness.context_window" }

func (contextWindowMiddleware) Model(
	ctx context.Context,
	request model.Request,
	emit model.StreamSink,
	next plugin.ModelNext,
) (model.Response, error) {
	materialized, err := MaterializeContextWindowRequest(ctx, request)
	if err != nil {
		return model.Response{}, err
	}
	return next(ctx, materialized, emit)
}

// MaterializeContextWindowRequest prepends the active Harness Context prefix to any provider-neutral
// model request. Production callers invoke this at the universal sampling boundary; the middleware
// remains a reusable Runtime composition primitive for hosts that do not own that boundary.
func MaterializeContextWindowRequest(ctx context.Context, request model.Request) (model.Request, error) {
	request = model.CloneRequest(request)
	checkpoint, ok := CurrentContextCheckpoint(ctx)
	if !ok || strings.TrimSpace(checkpoint.ID) == "" {
		return request, nil
	}
	messages := runtimecontext.Materialize(checkpoint.Window)
	merged, err := mergeContextRuntimeMessagesForCheckpoint(checkpoint, messages, request.Messages)
	if err != nil {
		return model.Request{}, err
	}
	request.Messages = merged
	return request, nil
}

func mergeContextRuntimeMessages(contextMessages, runtimeMessages []model.Message) ([]model.Message, error) {
	return mergeContextRuntimeMessagesForCheckpoint(runtimecontext.Checkpoint{}, contextMessages, runtimeMessages)
}

func mergeContextRuntimeMessagesForCheckpoint(
	checkpoint runtimecontext.Checkpoint,
	contextMessages, runtimeMessages []model.Message,
) ([]model.Message, error) {
	if len(contextMessages) == 0 || len(runtimeMessages) == 0 {
		return nil, ErrInvalidRequest
	}
	goalIndex := runtimeGoalIndex(runtimeMessages)
	if goalIndex >= len(runtimeMessages) || runtimeMessages[goalIndex].Role != model.RoleUser {
		return nil, ErrInvalidRequest
	}
	merged := append(model.CloneMessages(contextMessages), runtimeGuidanceMessages(runtimeMessages[:goalIndex])...)
	body := model.CloneMessages(runtimeMessages[goalIndex:])
	overlap := contextRuntimeOverlap(checkpoint, contextMessages, body)
	return append(merged, body[overlap:]...), nil
}

func runtimeGoalIndex(messages []model.Message) int {
	index := 0
	for index < len(messages) && messages[index].Role == model.RoleSystem {
		index++
	}
	return index
}

func contextRuntimeOverlap(
	checkpoint runtimecontext.Checkpoint,
	contextMessages, runtimeMessages []model.Message,
) int {
	limit := len(contextMessages)
	if len(runtimeMessages) < limit {
		limit = len(runtimeMessages)
	}
	for size := limit; size > 0; size-- {
		start := len(contextMessages) - size
		matched := true
		for index := 0; index < size; index++ {
			if !sameCheckpointModelMessage(checkpoint, contextMessages[start+index], runtimeMessages[index]) {
				matched = false
				break
			}
		}
		if matched {
			return size
		}
	}
	return 0
}

func sameCheckpointModelMessage(checkpoint runtimecontext.Checkpoint, left, right model.Message) bool {
	if sameContextModelMessage(left, right) {
		return true
	}
	if strings.TrimSpace(checkpoint.ScopeID) == "" || checkpoint.Generation <= 0 ||
		left.Role != model.RoleTool || right.Role != model.RoleTool ||
		strings.TrimSpace(left.ToolCallID) != strings.TrimSpace(right.ToolCallID) ||
		!strings.HasPrefix(strings.TrimSpace(left.Content), "[tool_result_compacted ") {
		return false
	}
	artifact, err := runtimecontext.NewArtifact(
		runtimecontext.ArtifactToolResult,
		checkpoint.ScopeID,
		checkpoint.Generation,
		strings.TrimSpace(right.ToolCallID),
		right.Content,
		nil,
	)
	return err == nil && strings.Contains(left.Content, "sha256="+artifact.ContentHash)
}

func sameContextModelMessage(left, right model.Message) bool {
	left.ToolCalls = normalizeContextToolCalls(left.ToolCalls)
	right.ToolCalls = normalizeContextToolCalls(right.ToolCalls)
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func normalizeContextToolCalls(values []tools.Call) []tools.Call {
	if len(values) == 0 {
		return nil
	}
	return values
}

// Runtime guidance is appended after the frozen Context prefix instead of mutating the first
// system message. This preserves prompt-cache prefix identity across sampling boundaries.
func runtimeGuidanceMessages(messages []model.Message) []model.Message {
	result := make([]model.Message, 0, len(messages))
	for _, message := range messages {
		if content := strings.TrimSpace(message.Content); content != "" {
			result = append(result, model.Message{Role: model.RoleSystem, Content: content})
		}
	}
	return result
}

func withContextCheckpoint(ctx context.Context, checkpoint runtimecontext.Checkpoint) context.Context {
	return context.WithValue(ctx, contextCheckpointKey{}, &contextWindowState{checkpoint: runtimecontext.CloneCheckpoint(checkpoint)})
}

func withContextWindowBinding(ctx context.Context, turnID string, access ContextWindowAccess) context.Context {
	binding := ContextWindowBinding{TurnID: strings.TrimSpace(turnID), Access: access}
	return context.WithValue(ctx, contextWindowBindingKey{}, binding)
}

func withoutContextCheckpoint(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, contextCheckpointKey{}, &contextWindowState{})
	return context.WithValue(ctx, contextWindowBindingKey{}, ContextWindowBinding{})
}

// CurrentContextWindowBinding returns the execution access contract for the active Context Window.
func CurrentContextWindowBinding(ctx context.Context) (ContextWindowBinding, bool) {
	binding, ok := ctx.Value(contextWindowBindingKey{}).(ContextWindowBinding)
	if !ok || strings.TrimSpace(binding.TurnID) == "" ||
		(binding.Access != ContextWindowOwner && binding.Access != ContextWindowReadOnly) {
		return ContextWindowBinding{}, false
	}
	binding.TurnID = strings.TrimSpace(binding.TurnID)
	return binding, true
}

// CurrentContextCheckpoint returns the execution-scoped active Context Window checkpoint.
func CurrentContextCheckpoint(ctx context.Context) (runtimecontext.Checkpoint, bool) {
	state, ok := ctx.Value(contextCheckpointKey{}).(*contextWindowState)
	if !ok || state == nil {
		return runtimecontext.Checkpoint{}, false
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	if strings.TrimSpace(state.checkpoint.ID) == "" {
		return runtimecontext.Checkpoint{}, false
	}
	return runtimecontext.CloneCheckpoint(state.checkpoint), true
}

// ReplaceContextCheckpoint advances the execution-scoped active window after a durable rollover.
// The expected checkpoint identity provides an in-memory CAS for parallel/continuation safety.
func ReplaceContextCheckpoint(ctx context.Context, expectedCheckpointID string, next runtimecontext.Checkpoint) error {
	state, ok := ctx.Value(contextCheckpointKey{}).(*contextWindowState)
	if !ok || state == nil || !runtimecontext.ValidCheckpoint(next) {
		return ErrInvalidRequest
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if strings.TrimSpace(state.checkpoint.ID) != strings.TrimSpace(expectedCheckpointID) ||
		next.ParentCheckpointID != state.checkpoint.ID || next.ScopeID != state.checkpoint.ScopeID {
		return ErrConflict
	}
	state.checkpoint = runtimecontext.CloneCheckpoint(next)
	return nil
}

func contextStaticFingerprint(config ConfigSnapshot, seed *ContextSeed, catalog tools.Catalog) (string, error) {
	if seed == nil {
		return "", ErrInvalidRequest
	}
	definitions, err := buildContextTools(catalog, config.ToolKeys)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(struct {
		Environment  VersionRef               `json:"environment"`
		Instructions string                   `json:"instructions"`
		Model        string                   `json:"model"`
		ModelOptions json.RawMessage          `json:"modelOptions"`
		Tools        []contextToolFingerprint `json:"tools"`
		Commands     []CommandDescriptor      `json:"commands"`
		Skills       []SkillSnapshot          `json:"skills"`
		MemoryPolicy string                   `json:"memoryPolicy,omitempty"`
	}{
		Environment:  config.Environment,
		Instructions: strings.TrimSpace(strings.Join([]string{config.Instructions, seed.Instructions}, "\n\n")),
		Model:        strings.TrimSpace(config.Model), ModelOptions: append(json.RawMessage(nil), config.ModelOptions...), Tools: definitions,
		Commands: cloneCommandDescriptors(config.Commands), Skills: append([]SkillSnapshot(nil), config.Skills...),
		MemoryPolicy: strings.TrimSpace(config.MemoryPolicy),
	})
	if err != nil {
		return "", err
	}
	return runtimecontext.StaticFingerprint(string(payload)), nil
}

type contextToolFingerprint struct {
	Key         string          `json:"key"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema"`
}

func buildContextTools(catalog tools.Catalog, keys []string) ([]contextToolFingerprint, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	if catalog == nil {
		return nil, ErrInvalidRequest
	}
	definitions, err := catalog.List(keys)
	if err != nil {
		return nil, err
	}
	result := make([]contextToolFingerprint, len(definitions))
	for index, definition := range definitions {
		result[index] = contextToolFingerprint{
			Key: strings.TrimSpace(definition.Key), Description: strings.TrimSpace(definition.Description),
			Schema: canonicalContextJSON(definition.InputSchema),
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Key < result[right].Key })
	return result, nil
}

func canonicalContextJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`true`)
	}
	var normalized any
	if json.Unmarshal(value, &normalized) != nil {
		return append(json.RawMessage(nil), value...)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return append(json.RawMessage(nil), value...)
	}
	return encoded
}

func normalizeContextSeed(seed *ContextSeed) (*ContextSeed, error) {
	if seed == nil {
		return nil, ErrInvalidRequest
	}
	result := &ContextSeed{
		SourcePath: append([]string(nil), seed.SourcePath...), Instructions: strings.TrimSpace(seed.Instructions),
		Entries: runtimecontext.CloneEntries(seed.Entries), ResetCacheIdentity: seed.ResetCacheIdentity,
	}
	for index := range result.SourcePath {
		result.SourcePath[index] = strings.TrimSpace(result.SourcePath[index])
	}
	if len(result.SourcePath) == 0 || len(result.SourcePath) != len(result.Entries) {
		return nil, ErrInvalidRequest
	}
	for index, sourceID := range result.SourcePath {
		if sourceID == "" || strings.TrimSpace(result.Entries[index].SourceID) != sourceID {
			return nil, ErrInvalidRequest
		}
	}
	return result, nil
}

func contextCheckpointRef(checkpoint runtimecontext.Checkpoint) ContextCheckpointRef {
	return ContextCheckpointRef{
		ID: checkpoint.ID, Generation: checkpoint.Generation, Revision: checkpoint.Revision,
		LineageHash: checkpoint.LineageHash, CoveredThroughSourceID: checkpoint.CoveredThroughSourceID,
		ContentHash: checkpoint.ContentHash,
	}
}

func cloneContextCheckpoint(value runtimecontext.Checkpoint) runtimecontext.Checkpoint {
	return runtimecontext.CloneCheckpoint(value)
}

func validContextCheckpoint(value runtimecontext.Checkpoint) bool {
	return runtimecontext.ValidCheckpoint(value)
}

func sameContextCheckpointRef(checkpoint runtimecontext.Checkpoint, ref ContextCheckpointRef) bool {
	return contextCheckpointRef(checkpoint) == ref
}

var errContextLineage = errors.New("invalid context checkpoint lineage")
