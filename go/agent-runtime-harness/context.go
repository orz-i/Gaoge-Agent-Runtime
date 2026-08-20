package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	runtimecontext "github.com/orz-i/Gaoge/sdk/go/agent-runtime/context"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/model"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

var ErrUnsupportedContextItem = errors.New("unsupported harness context item")

// ContextSeed is host-neutral source material for one immutable Runtime Context build.
// Product hosts own source lookup; Harness owns budgeting, compaction and the sealed snapshot.
type ContextSeed struct {
	ThreadPathHash string                `json:"threadPathHash"`
	CurrentTurnID  string                `json:"currentTurnID"`
	Instructions   string                `json:"instructions,omitempty"`
	Items          []runtimecontext.Item `json:"items"`
}

// ContextRef is the content-free durable reference stored on a Harness Turn.
type ContextRef struct {
	ID             string `json:"id"`
	Revision       int    `json:"revision"`
	ThreadPathHash string `json:"threadPathHash"`
	ContentHash    string `json:"contentHash"`
}

type contextSnapshotKey struct{}

// NewContextModelMiddleware injects the exact sealed Context Snapshot carried by Harness Start.
func NewContextModelMiddleware() plugin.ModelMiddleware { return contextModelMiddleware{} }

type contextModelMiddleware struct{}

func (contextModelMiddleware) Name() string { return "harness.context" }

func (contextModelMiddleware) Model(
	ctx context.Context,
	request model.Request,
	emit model.StreamSink,
	next plugin.ModelNext,
) (model.Response, error) {
	snapshot, ok := ctx.Value(contextSnapshotKey{}).(runtimecontext.Snapshot)
	if !ok || strings.TrimSpace(snapshot.ID) == "" {
		return next(ctx, request, emit)
	}
	messages, err := contextMessages(snapshot)
	if err != nil {
		return model.Response{}, err
	}
	request.Messages, err = mergeContextRuntimeMessages(messages, request.Messages)
	if err != nil {
		return model.Response{}, err
	}
	return next(ctx, request, emit)
}

func mergeContextRuntimeMessages(contextMessages, runtimeMessages []model.Message) ([]model.Message, error) {
	if len(contextMessages) == 0 || len(runtimeMessages) == 0 {
		return nil, ErrInvalidRequest
	}
	goalIndex := runtimeGoalIndex(runtimeMessages)
	contextGoal := contextMessages[len(contextMessages)-1]
	if !sameCurrentGoal(contextGoal, runtimeMessages, goalIndex) {
		return nil, ErrInvalidRequest
	}
	merged := mergeRuntimeGuidance(contextMessages, runtimeMessages[:goalIndex])
	return append(merged, model.CloneMessages(runtimeMessages[goalIndex+1:])...), nil
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

func mergeRuntimeGuidance(contextMessages, guidanceMessages []model.Message) []model.Message {
	merged := model.CloneMessages(contextMessages)
	guidance := runtimeGuidance(guidanceMessages)
	if guidance == "" {
		return merged
	}
	if merged[0].Role == model.RoleSystem {
		merged[0].Content = strings.TrimSpace(merged[0].Content) + "\n\n" + guidance
		return merged
	}
	return append([]model.Message{{Role: model.RoleSystem, Content: guidance}}, merged...)
}

func runtimeGuidance(messages []model.Message) string {
	guidance := make([]string, 0, len(messages))
	for _, message := range messages {
		if content := strings.TrimSpace(message.Content); content != "" {
			guidance = append(guidance, content)
		}
	}
	return strings.Join(guidance, "\n\n")
}

func withContextSnapshot(ctx context.Context, snapshot runtimecontext.Snapshot) context.Context {
	return context.WithValue(ctx, contextSnapshotKey{}, snapshot)
}

// withoutContextSnapshot preserves cancellation, deadlines, and unrelated host values while
// preventing a delegated child Agent from inheriting its parent Harness Turn's sealed context.
// Child delegation goals already carry their explicit frozen evidence boundary.
func withoutContextSnapshot(ctx context.Context) context.Context {
	return context.WithValue(ctx, contextSnapshotKey{}, runtimecontext.Snapshot{})
}

func contextMessages(snapshot runtimecontext.Snapshot) ([]model.Message, error) {
	var prompt runtimecontext.Prompt
	if !json.Valid(snapshot.Content) || json.Unmarshal(snapshot.Content, &prompt) != nil {
		return nil, ErrInvalidRequest
	}
	messages := make([]model.Message, 0, len(prompt.Items)+1)
	if instructions := strings.TrimSpace(prompt.Instructions); instructions != "" {
		messages = append(messages, model.Message{Role: model.RoleSystem, Content: instructions})
	}
	for _, item := range prompt.Items {
		message, ok, err := contextItemMessage(item)
		if err != nil {
			return nil, err
		}
		if ok {
			messages = append(messages, message)
		}
	}
	if len(messages) == 0 {
		return nil, ErrInvalidRequest
	}
	return messages, nil
}

func contextItemMessage(item runtimecontext.Item) (model.Message, bool, error) {
	switch item.Kind {
	case runtimecontext.ItemMessage, runtimecontext.ItemSummary:
		role, err := modelRole(item.Role)
		if err != nil {
			return model.Message{}, false, err
		}
		return model.Message{Role: role, Content: item.Content}, true, nil
	case runtimecontext.ItemToolCall, runtimecontext.ItemToolResult:
		// The current Context tool item contract is not losslessly isomorphic to
		// model.Message ToolCalls. Refuse rather than silently dropping arguments/details.
		return model.Message{}, false, ErrUnsupportedContextItem
	default:
		return model.Message{}, false, ErrUnsupportedContextItem
	}
}

func modelRole(role runtimecontext.Role) (model.Role, error) {
	switch role {
	case runtimecontext.RoleSystem:
		return model.RoleSystem, nil
	case runtimecontext.RoleUser:
		return model.RoleUser, nil
	case runtimecontext.RoleAssistant:
		return model.RoleAssistant, nil
	case runtimecontext.RoleTool:
		return model.RoleTool, nil
	default:
		return "", ErrUnsupportedContextItem
	}
}

func buildContextTools(catalog tools.Catalog, keys []string) ([]runtimecontext.ToolDefinition, error) {
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
	result := make([]runtimecontext.ToolDefinition, len(definitions))
	for index, definition := range definitions {
		result[index] = runtimecontext.ToolDefinition{
			Key: definition.Key, Description: definition.Description,
			Schema: append(json.RawMessage(nil), definition.InputSchema...),
		}
	}
	return result, nil
}

func normalizeContextSeed(seed *ContextSeed) (*ContextSeed, error) {
	if seed == nil {
		return nil, ErrInvalidRequest
	}
	result := &ContextSeed{
		ThreadPathHash: strings.TrimSpace(seed.ThreadPathHash),
		CurrentTurnID:  strings.TrimSpace(seed.CurrentTurnID),
		Instructions:   strings.TrimSpace(seed.Instructions),
		Items:          append([]runtimecontext.Item(nil), seed.Items...),
	}
	if result.ThreadPathHash == "" || result.CurrentTurnID == "" || len(result.Items) == 0 {
		return nil, ErrInvalidRequest
	}
	return result, nil
}

// ContextPathHash creates a stable content-free path identity from immutable source identifiers.
func ContextPathHash(parts ...string) string {
	joined := make([]string, 0, len(parts))
	for _, part := range parts {
		joined = append(joined, strings.TrimSpace(part))
	}
	hash := sha256.Sum256([]byte(strings.Join(joined, "\x00")))
	return hex.EncodeToString(hash[:])
}

func contextRef(snapshot runtimecontext.Snapshot) ContextRef {
	return ContextRef{
		ID: snapshot.ID, Revision: snapshot.Revision,
		ThreadPathHash: snapshot.ThreadPathHash, ContentHash: snapshot.ContentHash,
	}
}

func cloneContextSnapshot(value runtimecontext.Snapshot) runtimecontext.Snapshot {
	value.Content = append(json.RawMessage(nil), value.Content...)
	value.ArtifactIDs = append([]string(nil), value.ArtifactIDs...)
	value.Trace.Actions = append([]runtimecontext.TrimAction(nil), value.Trace.Actions...)
	if value.Trace.Summary != nil {
		summary := *value.Trace.Summary
		value.Trace.Summary = &summary
	}
	return value
}

func validContextSnapshot(value runtimecontext.Snapshot) bool {
	return strings.TrimSpace(value.ID) != "" && strings.TrimSpace(value.RunID) != "" && value.Revision > 0 &&
		strings.TrimSpace(value.ThreadPathHash) != "" && strings.TrimSpace(value.ContentHash) != "" &&
		len(value.Content) > 0 && json.Valid(value.Content)
}
