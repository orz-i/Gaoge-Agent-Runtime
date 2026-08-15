package harness

import (
	"context"
	"encoding/json"
	"strings"
)

// ExternalItem records one durable host-owned execution fact without copying
// the host entity body into Harness storage. Only artifact, approval and
// diagnostic facts may cross this boundary; Tool/Message/Delegation lifecycles
// remain Harness-owned.
type ExternalItem struct {
	Key       string
	ParentKey string
	Kind      ItemKind
	Status    ItemStatus
	HostRef   *HostRef
	Payload   json.RawMessage
}

// RecordExternalItem appends or idempotently reloads one host-owned durable
// execution fact for an existing Harness Turn.
func (runner *Runner) RecordExternalItem(
	ctx context.Context,
	turnID string,
	input ExternalItem,
) (Item, error) {
	turnID = strings.TrimSpace(turnID)
	input.Key = strings.TrimSpace(input.Key)
	input.ParentKey = strings.TrimSpace(input.ParentKey)
	if turnID == "" || input.Key == "" || !validExternalItemKind(input.Kind) || !validItemStatus(input.Status) ||
		!json.Valid(defaultJSON(input.Payload)) {
		return Item{}, ErrInvalidRequest
	}
	turn, err := runner.store.GetTurn(ctx, turnID)
	if err != nil {
		return Item{}, err
	}
	if input.HostRef != nil {
		ref, normalizeErr := normalizeHostRef(*input.HostRef)
		if normalizeErr != nil {
			return Item{}, normalizeErr
		}
		input.HostRef = &ref
	}
	now := runner.clock.Now().UTC()
	parentItemID := ""
	if input.ParentKey != "" {
		parentItemID = stableID("hix", turn.ID, input.ParentKey)
	}
	item, _, err := runner.store.AppendItem(ctx, Item{
		ID: stableID("hix", turn.ID, input.Key), TurnID: turn.ID,
		Kind: input.Kind, Status: input.Status, HostRef: input.HostRef,
		ParentItemID: parentItemID,
		Payload:      append(json.RawMessage(nil), input.Payload...), CreatedAt: now, UpdatedAt: now,
	})
	return item, err
}

func validExternalItemKind(kind ItemKind) bool {
	return kind == ItemArtifact || kind == ItemApproval || kind == ItemDiagnostic
}
