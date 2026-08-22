package harness

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	runtimecontext "github.com/orz-i/Gaoge/sdk/go/agent-runtime/context"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/model"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

// ContextSeed is the complete host transcript ancestry for one Harness Turn.
// SourcePath has no semantic message-count limit; repository paging is an I/O concern only.
type ContextSeed struct {
	SourcePath   []string               `json:"sourcePath"`
	Instructions string                 `json:"instructions,omitempty"`
	Entries      []runtimecontext.Entry `json:"entries"`
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

type contextCheckpointKey struct{}

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
	checkpoint, ok := ctx.Value(contextCheckpointKey{}).(runtimecontext.Checkpoint)
	if !ok || strings.TrimSpace(checkpoint.ID) == "" {
		return next(ctx, request, emit)
	}
	messages := runtimecontext.Materialize(checkpoint.Window)
	merged, err := mergeContextRuntimeMessages(messages, request.Messages)
	if err != nil {
		return model.Response{}, err
	}
	request.Messages = merged
	return next(ctx, request, emit)
}

func mergeContextRuntimeMessages(contextMessages, runtimeMessages []model.Message) ([]model.Message, error) {
	if len(contextMessages) == 0 || len(runtimeMessages) == 0 {
		return nil, ErrInvalidRequest
	}
	goalIndex := runtimeGoalIndex(runtimeMessages)
	if goalIndex >= len(runtimeMessages) || runtimeMessages[goalIndex].Role != model.RoleUser {
		return nil, ErrInvalidRequest
	}
	contextGoal := contextMessages[len(contextMessages)-1]
	merged := append(model.CloneMessages(contextMessages), runtimeGuidanceMessages(runtimeMessages[:goalIndex])...)
	if sameCurrentGoal(contextGoal, runtimeMessages, goalIndex) {
		return append(merged, model.CloneMessages(runtimeMessages[goalIndex+1:])...), nil
	}
	// Feature-owned child Agents have an explicit goal that differs from the parent Conversation Turn.
	return append(merged, model.CloneMessages(runtimeMessages[goalIndex:])...), nil
}

func runtimeGoalIndex(messages []model.Message) int {
	index := 0
	for index < len(messages) && messages[index].Role == model.RoleSystem {
		index++
	}
	return index
}

func sameCurrentGoal(contextGoal model.Message, runtimeMessages []model.Message, goalIndex int) bool {
	return goalIndex < len(runtimeMessages) && runtimeMessages[goalIndex].Role == model.RoleUser &&
		contextGoal.Role == model.RoleUser &&
		strings.TrimSpace(runtimeMessages[goalIndex].Content) == strings.TrimSpace(contextGoal.Content)
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
	return context.WithValue(ctx, contextCheckpointKey{}, checkpoint)
}

func withoutContextCheckpoint(ctx context.Context) context.Context {
	return context.WithValue(ctx, contextCheckpointKey{}, runtimecontext.Checkpoint{})
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
		Instructions string                   `json:"instructions"`
		Model        string                   `json:"model"`
		ModelOptions json.RawMessage          `json:"modelOptions"`
		Tools        []contextToolFingerprint `json:"tools"`
	}{
		Instructions: strings.TrimSpace(strings.Join([]string{config.Instructions, seed.Instructions}, "\n\n")),
		Model:        strings.TrimSpace(config.Model), ModelOptions: append(json.RawMessage(nil), config.ModelOptions...), Tools: definitions,
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
		Entries: runtimecontext.CloneEntries(seed.Entries),
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
