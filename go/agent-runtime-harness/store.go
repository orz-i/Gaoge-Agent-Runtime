package harness

import "context"

// Store is the durable Harness state boundary. Implementations must clone values at every boundary.
type Store interface {
	CreateSession(context.Context, Session) (Session, bool, error)
	GetSession(context.Context, string) (Session, error)
	CreateTurn(context.Context, Turn) (Turn, bool, error)
	GetTurn(context.Context, string) (Turn, error)
	UpdateTurn(context.Context, Turn, uint64) (Turn, error)
	PutConfigSnapshot(context.Context, ConfigSnapshot) (ConfigSnapshot, bool, error)
	GetConfigSnapshot(context.Context, string) (ConfigSnapshot, error)
	AppendItem(context.Context, Item) (Item, bool, error)
	ListItems(context.Context, string, uint64, int) ([]Item, error)
}
