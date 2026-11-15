package harness

import (
	"encoding/json"
	"strings"
	"time"
)

// ItemKind identifies one durable Harness execution fact. Token deltas never become Items.
type ItemKind string

const (
	ItemUserMessage  ItemKind = "user_message"
	ItemAgentRun     ItemKind = "agent_run"
	ItemAgentMessage ItemKind = "agent_message"
	ItemTool         ItemKind = "tool"
	ItemApproval     ItemKind = "approval"
	ItemDelegation   ItemKind = "delegation"
	ItemInvocation   ItemKind = "capability_invocation"
	ItemInteraction  ItemKind = "interaction"
	ItemArtifact     ItemKind = "artifact"
	ItemContext      ItemKind = "context"
	ItemDiagnostic   ItemKind = "diagnostic"
	ItemBudget       ItemKind = "budget"
	ItemSubtask      ItemKind = "subtask"
)

// ItemStatus is one durable item lifecycle state.
type ItemStatus string

const (
	ItemStarted   ItemStatus = "started"
	ItemWaiting   ItemStatus = "waiting"
	ItemCompleted ItemStatus = "completed"
	ItemFailed    ItemStatus = "failed"
	ItemCancelled ItemStatus = "cancelled"
)

// Item records an execution fact and may reference a product-owned entity without copying its body.
type Item struct {
	ID           string          `json:"id"`
	TurnID       string          `json:"turnID"`
	Seq          uint64          `json:"seq"`
	Kind         ItemKind        `json:"kind"`
	Status       ItemStatus      `json:"status"`
	HostRef      *HostRef        `json:"hostRef,omitempty"`
	RunID        string          `json:"runID,omitempty"`
	InvocationID string          `json:"invocationID,omitempty"`
	ParentItemID string          `json:"parentItemID,omitempty"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	CreatedAt    time.Time       `json:"createdAt"`
	UpdatedAt    time.Time       `json:"updatedAt"`
}

func validItem(value Item) bool {
	if strings.TrimSpace(value.ID) == "" || strings.TrimSpace(value.TurnID) == "" || !validItemKind(value.Kind) || !validItemStatus(value.Status) {
		return false
	}
	if value.HostRef != nil {
		if _, err := normalizeHostRef(*value.HostRef); err != nil {
			return false
		}
	}
	return json.Valid(defaultJSON(value.Payload))
}

func validItemKind(value ItemKind) bool {
	switch value {
	case ItemUserMessage, ItemAgentRun, ItemAgentMessage, ItemTool, ItemApproval, ItemDelegation, ItemInvocation, ItemInteraction, ItemArtifact, ItemContext, ItemDiagnostic, ItemBudget, ItemSubtask:
		return true
	default:
		return false
	}
}

func validItemStatus(value ItemStatus) bool {
	switch value {
	case ItemStarted, ItemWaiting, ItemCompleted, ItemFailed, ItemCancelled:
		return true
	default:
		return false
	}
}

func cloneItem(value Item) Item {
	value.Payload = append(json.RawMessage(nil), value.Payload...)
	if value.HostRef != nil {
		ref := *value.HostRef
		value.HostRef = &ref
	}
	return value
}

func cloneItems(values []Item) []Item {
	result := make([]Item, len(values))
	for index, value := range values {
		result[index] = cloneItem(value)
	}
	return result
}
