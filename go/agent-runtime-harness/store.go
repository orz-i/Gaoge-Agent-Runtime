package harness

import (
	"context"
	"time"

	runtimecontext "github.com/orz-i/Gaoge/sdk/go/agent-runtime/context"
)

// Store is the durable Harness state boundary. Implementations must clone values at every boundary.
type Store interface {
	CreateSession(context.Context, Session) (Session, bool, error)
	GetSession(context.Context, string) (Session, error)
	CreateTurn(context.Context, Turn) (Turn, bool, error)
	GetTurn(context.Context, string) (Turn, error)
	UpdateTurn(context.Context, Turn, uint64) (Turn, error)
	CreateInvocation(context.Context, Invocation) (Invocation, bool, error)
	GetInvocation(context.Context, string) (Invocation, error)
	GetInvocationByExecutionRefID(context.Context, string) (Invocation, error)
	UpdateInvocation(context.Context, Invocation, uint64) (Invocation, error)
	RetryInvocation(context.Context, string, uint64, string, time.Time) (Invocation, error)
	ListInvocations(context.Context, string) ([]Invocation, error)
	CreateInteraction(context.Context, Interaction, uint64, uint64) (Interaction, bool, error)
	GetInteraction(context.Context, string) (Interaction, error)
	UpdateInteraction(context.Context, Interaction, uint64) (Interaction, error)
	ListInteractions(context.Context, string) ([]Interaction, error)
	PutConfigSnapshot(context.Context, ConfigSnapshot) (ConfigSnapshot, bool, error)
	GetConfigSnapshot(context.Context, string) (ConfigSnapshot, error)
	PutContextSnapshot(context.Context, runtimecontext.Snapshot) (runtimecontext.Snapshot, bool, error)
	GetContextSnapshot(context.Context, string) (runtimecontext.Snapshot, error)
	AppendItem(context.Context, Item) (Item, bool, error)
	ListItems(context.Context, string, uint64, int) ([]Item, error)
}

// listAllItems is the correctness path for durable Harness facts. ListItems is
// deliberately bounded, so state derivation must consume every page.
func listAllItems(ctx context.Context, store Store, turnID string) ([]Item, error) {
	if store == nil {
		return nil, ErrInvalidRequest
	}
	result := make([]Item, 0)
	var afterSeq uint64
	for {
		page, err := store.ListItems(ctx, turnID, afterSeq, defaultItemListLimit)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			return result, nil
		}
		for _, item := range page {
			if item.Seq <= afterSeq {
				return nil, ErrConflict
			}
			result = append(result, item)
			afterSeq = item.Seq
		}
		if len(page) < defaultItemListLimit {
			return result, nil
		}
	}
}
