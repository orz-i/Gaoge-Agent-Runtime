package harness

import (
	"context"
	"strings"
	"time"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
)

// ContextCheckpointCommit atomically persists one immutable Context checkpoint and advances the
// owning Harness Turn reference. The scope head is advanced only when it still equals
// ExpectedHeadCheckpointID. If another top-level owner has already moved the head, the Turn is
// committed as a detached branch instead of failing; branch-path lookup can still reuse its
// source-aligned checkpoints later. The two expected checkpoint IDs are intentionally distinct:
// a newly-created Turn has no checkpoint yet while the Session may already have an active head
// from an earlier Turn.
type ContextCheckpointCommit struct {
	TurnID                   string
	ExpectedTurnRevision     uint64
	ExpectedTurnCheckpointID string
	ExpectedHeadCheckpointID string
	Checkpoint               runtimecontext.Checkpoint
	UpdatedAt                time.Time
}

// ContextCheckpointPathQuery resolves the nearest reusable source-aligned checkpoint for one
// complete host ancestry. SourcePath is a correctness path and has no semantic message-count cap.
type ContextCheckpointPathQuery struct {
	ScopeID           string
	StaticFingerprint string
	SourcePath        []string
}

// NormalizeContextCheckpointPathQuery enforces the Store-wide ancestry lookup contract before
// persistence-specific selection begins. Source identities are immutable path components: empty
// or duplicate identities are malformed rather than a request to silently rewrite the path.
func NormalizeContextCheckpointPathQuery(value ContextCheckpointPathQuery) (ContextCheckpointPathQuery, error) {
	value.ScopeID = strings.TrimSpace(value.ScopeID)
	value.StaticFingerprint = strings.TrimSpace(value.StaticFingerprint)
	value.SourcePath = append([]string(nil), value.SourcePath...)
	if value.ScopeID == "" || value.StaticFingerprint == "" || len(value.SourcePath) == 0 {
		return ContextCheckpointPathQuery{}, ErrInvalidRequest
	}
	seen := make(map[string]struct{}, len(value.SourcePath))
	for index, sourceID := range value.SourcePath {
		sourceID = strings.TrimSpace(sourceID)
		if sourceID == "" {
			return ContextCheckpointPathQuery{}, ErrInvalidRequest
		}
		if _, duplicate := seen[sourceID]; duplicate {
			return ContextCheckpointPathQuery{}, ErrInvalidRequest
		}
		seen[sourceID] = struct{}{}
		value.SourcePath[index] = sourceID
	}
	return value, nil
}

// Store is the durable Harness state boundary. Implementations must clone values at every boundary.
type Store interface {
	CreateSession(context.Context, Session) (Session, bool, error)
	GetSession(context.Context, string) (Session, error)
	CreateTurn(context.Context, Turn) (Turn, bool, error)
	GetTurn(context.Context, string) (Turn, error)
	UpdateTurn(context.Context, Turn, uint64) (Turn, error)
	// CreateInvocation atomically permits one top-level Invocation per Turn.
	// ParentItemID is trimmed; an empty value identifies the root. Replaying
	// the same identity is allowed, but a different root returns ErrConflict.
	CreateInvocation(context.Context, Invocation) (Invocation, bool, error)
	GetInvocation(context.Context, string) (Invocation, error)
	GetInvocationByExecutionRefID(context.Context, string) (Invocation, error)
	UpdateInvocation(context.Context, Invocation, uint64) (Invocation, error)
	RetryInvocation(context.Context, string, uint64, string, time.Time) (Invocation, error)
	ListInvocations(context.Context, string) ([]Invocation, error)
	CreateInteraction(context.Context, Interaction, uint64, uint64) (Interaction, bool, error)
	GetInteraction(context.Context, string) (Interaction, error)
	UpdateInteraction(context.Context, Interaction, uint64) (Interaction, error)
	ResolveInteraction(context.Context, Interaction, uint64) (InteractionResolution, error)
	ListInteractions(context.Context, string) ([]Interaction, error)
	PutConfigSnapshot(context.Context, ConfigSnapshot) (ConfigSnapshot, bool, error)
	GetConfigSnapshot(context.Context, string) (ConfigSnapshot, error)
	PutContextCheckpoint(context.Context, runtimecontext.Checkpoint) (runtimecontext.Checkpoint, bool, error)
	GetContextCheckpoint(context.Context, string) (runtimecontext.Checkpoint, error)
	GetActiveContextCheckpoint(context.Context, string) (runtimecontext.Checkpoint, error)
	FindContextCheckpointForPath(context.Context, ContextCheckpointPathQuery) (runtimecontext.Checkpoint, error)
	CommitContextCheckpoint(context.Context, ContextCheckpointCommit) (Turn, error)
	PutContextArtifact(context.Context, runtimecontext.Artifact) (runtimecontext.Artifact, bool, error)
	GetContextArtifact(context.Context, string) (runtimecontext.Artifact, error)
	// AppendItem is an execution-authority boundary for diagnostic Items:
	// a fresh ExecutionDispatchParentItemID grant MUST atomically reject
	// active cancellation intents, terminal Turn/Invocation owners, and
	// unfinished grants for the same Invocation. A cancellation-intent
	// insertion MUST serialize against these grants on the same Turn lock.
	// Custom Stores must implement the hard-cut semantics; a loose
	// append-only implementation is no longer a safe execution Store.
	AppendItem(context.Context, Item) (Item, bool, error)
	// GetItem retrieves one immutable Item by its globally unique ID.
	// Child-effect ancestry checks must not scan full Turn histories.
	// Missing IDs return ErrNotFound; callers still verify the Turn owner.
	GetItem(context.Context, string) (Item, error)
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
