package consumer

import (
	"context"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
)

// This deliberately spells out the host persistence port instead of embedding
// harness.Store: adding a required method must trigger compatibility review.
type harnessStoreContract interface {
	CreateSession(context.Context, harness.Session) (harness.Session, bool, error)
	GetSession(context.Context, string) (harness.Session, error)
	CreateTurn(context.Context, harness.Turn) (harness.Turn, bool, error)
	GetTurn(context.Context, string) (harness.Turn, error)
	UpdateTurn(context.Context, harness.Turn, uint64) (harness.Turn, error)
	CreateInvocation(context.Context, harness.Invocation) (harness.Invocation, bool, error)
	GetInvocation(context.Context, string) (harness.Invocation, error)
	GetInvocationByExecutionRefID(context.Context, string) (harness.Invocation, error)
	UpdateInvocation(context.Context, harness.Invocation, uint64) (harness.Invocation, error)
	RetryInvocation(context.Context, string, uint64, string, time.Time) (harness.Invocation, error)
	ListInvocations(context.Context, string) ([]harness.Invocation, error)
	CreateInteraction(context.Context, harness.Interaction, uint64, uint64) (harness.Interaction, bool, error)
	GetInteraction(context.Context, string) (harness.Interaction, error)
	UpdateInteraction(context.Context, harness.Interaction, uint64) (harness.Interaction, error)
	ResolveInteraction(context.Context, harness.Interaction, uint64) (harness.InteractionResolution, error)
	ListInteractions(context.Context, string) ([]harness.Interaction, error)
	PutConfigSnapshot(context.Context, harness.ConfigSnapshot) (harness.ConfigSnapshot, bool, error)
	GetConfigSnapshot(context.Context, string) (harness.ConfigSnapshot, error)
	PutContextCheckpoint(context.Context, runtimecontext.Checkpoint) (runtimecontext.Checkpoint, bool, error)
	GetContextCheckpoint(context.Context, string) (runtimecontext.Checkpoint, error)
	GetActiveContextCheckpoint(context.Context, string) (runtimecontext.Checkpoint, error)
	FindContextCheckpointForPath(context.Context, harness.ContextCheckpointPathQuery) (runtimecontext.Checkpoint, error)
	CommitContextCheckpoint(context.Context, harness.ContextCheckpointCommit) (harness.Turn, error)
	PutContextArtifact(context.Context, runtimecontext.Artifact) (runtimecontext.Artifact, bool, error)
	GetContextArtifact(context.Context, string) (runtimecontext.Artifact, error)
	AppendItem(context.Context, harness.Item) (harness.Item, bool, error)
	ListItems(context.Context, string, uint64, int) ([]harness.Item, error)
}

var (
	_ harness.Store                                       = (harnessStoreContract)(nil)
	_ harnessStoreContract                                = (harness.Store)(nil)
	_ harness.Store                                       = (*harness.MemoryStore)(nil)
	_ func(harness.Dependencies) (*harness.Runner, error) = harness.NewRunner
	_ func() *harness.MemoryStore                         = harness.NewMemoryStore
)
