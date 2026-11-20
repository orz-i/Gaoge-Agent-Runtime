package consumer

import (
	"context"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
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
	// Reviewed hard cut: first Accepted and Retry Roots use the same original
	// atomic no-dispatch terminalization capability, not an optional adapter.
	CancelUndispatchedRoot(context.Context, harness.Invocation, harness.Turn) (harness.Invocation, harness.Turn, error)
	ListInvocations(context.Context, string) ([]harness.Invocation, error)
	CreateInteraction(context.Context, harness.Interaction, uint64, uint64) (harness.Interaction, bool, error)
	GetInteraction(context.Context, string) (harness.Interaction, error)
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
	GetItem(context.Context, string) (harness.Item, error)
	// Pre-v1 hard-cut: globally indexed original-parent proof is now
	// mandatory on every Harness Store (no Turn-scoped fallback).
	ListItemChildren(context.Context, string, harness.ItemChildProofScope) ([]harness.Item, error)
	ListItems(context.Context, string, uint64, int) ([]harness.Item, error)
}

type localKernelRunKindCancellerContract interface {
	Cancel(context.Context, string, uint64, string) (kernel.Snapshot, error)
	OwnsKernelRunKind(*kernel.Runtime, kernel.RunKind) bool
}

type cancellationReconcilerContract interface {
	CancellationOwnerID() string
	ReconcileCancellation(context.Context, harness.CancellationEffect) (kernel.Snapshot, harness.CancellationEffectOutcome, error)
}

var (
	_                                                                                        = harness.AgentSessionOptions{LocalTools: []harness.SessionTool{}, ExploreReads: true, Inputs: (agent.RunInputSource)(nil)}
	_                                                                                        = harness.AgentSessionContextOptions{ReadArtifacts: true}
	_                                                                                        = runtimecontext.Artifact{Encoding: runtimecontext.ArtifactEncodingLosslessJSON}
	_ func(harness.TextAgentSessionOptions) (*harness.SessionClient, error)                  = harness.OpenTextAgentSession
	_ func(harness.ToolAgentSessionOptions) (*harness.SessionClient, error)                  = harness.OpenToolAgentSession
	_ func(harness.AgentSessionOptions) (*harness.SessionClient, error)                      = harness.OpenAgentSession
	_                                                                                        = harness.AgentSessionContextOptions{ManagedRecovery: true}
	_                                                                                        = harness.ConfigSnapshot{MaxModelOutputTokens: 1024}
	_ harness.RuntimeCancellationReconciler                                                  = (cancellationReconcilerContract)(nil)
	_ cancellationReconcilerContract                                                         = (harness.RuntimeCancellationReconciler)(nil)
	_ func(context.Context) (harness.CancellationEffect, bool)                               = harness.CancellationEffectFromContext
	_ harness.LocalKernelRunKindCanceller                                                    = (localKernelRunKindCancellerContract)(nil)
	_ localKernelRunKindCancellerContract                                                    = (harness.LocalKernelRunKindCanceller)(nil)
	_ func(context.Context, string, uint64, string) bool                                     = harness.VerifiedSubtaskApprovalDispatch
	_ harness.Store                                                                          = (harnessStoreContract)(nil)
	_ harnessStoreContract                                                                   = (harness.Store)(nil)
	_ harness.Store                                                                          = (*harness.MemoryStore)(nil)
	_ func(harness.Dependencies) (*harness.Runner, error)                                    = harness.NewRunner
	_ func() *harness.MemoryStore                                                            = harness.NewMemoryStore
	_ func(*harness.Runner, context.Context, harness.StartRequest) (harness.Snapshot, error) = (*harness.Runner).StartAgentTurn
	_ func(*harness.Runner, context.Context, string) (harness.Snapshot, error)               = (*harness.Runner).ResumeTurn
	_ func(*harness.SessionClient, context.Context, string) (harness.Snapshot, error)        = (*harness.SessionClient).Resume
)

var (
	_                                                                                                                = harness.ContextSeed{WorkingState: &runtimecontext.WorkingState{Version: 1, Goal: "Preserve the Host task state"}}
	_ func(*runtimecontext.WorkingState) *runtimecontext.WorkingState                                                = runtimecontext.CloneWorkingState
	_ func(*runtimecontext.WorkingState) error                                                                       = runtimecontext.ValidateWorkingState
	_ func(*harness.SessionClient, context.Context, string, string, []string) (harness.ContextSourceBoundary, error) = (*harness.SessionClient).ResolveContextSourceBoundaryForPath
)

var (
	_ = harness.ConfigSnapshot{WorkingStateSummary: &agent.WorkingStateSummaryPolicy{MaxInputTokens: 8192, MaxSerializedBytes: 64 << 10}}
	_ = runtimecontext.WorkingState{Derivation: &runtimecontext.WorkingStateDerivation{Kind: runtimecontext.WorkingStateDerivationSummary}}
	_ = harness.AgentSessionContextOptions{SummaryNativeRecovery: true}
)
