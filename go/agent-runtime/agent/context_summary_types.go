package agent

import (
	"context"
	"time"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

const (
	ModelInvocationContextSummary = "context_summary"
	WorkingStateSummaryVersion    = "working-state-summary-v2"
	EventContextSummaryConsumed   = "agent.context_summary.consumed"
)

// WorkingStateSummaryPolicy opts one original Run into a bounded additional
// logical model call. Both limits tighten the existing Context input limits.
type WorkingStateSummaryPolicy struct {
	MaxInputTokens     int64 `json:"maxInputTokens"`
	MaxSerializedBytes int64 `json:"maxSerializedBytes"`
}

func ValidateWorkingStateSummaryPolicy(value *WorkingStateSummaryPolicy) error {
	if value != nil && (value.MaxInputTokens <= 0 || value.MaxSerializedBytes <= 0) {
		return ErrInvalidRequest
	}
	return nil
}

func CloneWorkingStateSummaryPolicy(value *WorkingStateSummaryPolicy) *WorkingStateSummaryPolicy {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// ContextSummaryRoot identifies the original indexed Host root. Ports must
// authenticate it against their existing immutable Invocation, not a new owner.
type ContextSummaryRoot struct {
	InvocationID string `json:"invocationID"`
	InputHash    string `json:"inputHash"`
	Attempt      int    `json:"attempt"`
}

// ContextSummaryCursor persists only the source cursor's bounded identity.
// The source Window remains in its original Context checkpoint Store.
type ContextSummaryCursor struct {
	Turn                     SamplingTurn `json:"turn"`
	ExpectedTurnCheckpointID string       `json:"expectedTurnCheckpointID"`
	ExpectedHeadCheckpointID string       `json:"expectedHeadCheckpointID"`
	UpdatedAt                time.Time    `json:"updatedAt"`
}

type ContextSummaryArtifactRef struct {
	ID          string `json:"id"`
	ContentHash string `json:"contentHash"`
}

// ContextSummaryBinding is an immutable recipe in the original model intent.
// It has no lease, dispatch permission, target hash or self-referential receipt.
type ContextSummaryBinding struct {
	RunID                  string                            `json:"runID"`
	Root                   ContextSummaryRoot                `json:"root"`
	Cursor                 ContextSummaryCursor              `json:"cursor"`
	Source                 runtimecontext.WorkingStateSource `json:"source"`
	PriorVersion           int                               `json:"priorVersion"`
	NextVersion            int                               `json:"nextVersion"`
	Policy                 WorkingStateSummaryPolicy         `json:"policy"`
	ContextPolicy          runtimecontext.Policy             `json:"contextPolicy"`
	ModelWindow            runtimecontext.ModelWindow        `json:"modelWindow"`
	ModelWindowFingerprint string                            `json:"modelWindowFingerprint"`
	RecipeVersion          string                            `json:"recipeVersion"`
	RemovedMessages        int                               `json:"removedMessages"`
	Artifacts              []ContextSummaryArtifactRef       `json:"artifacts"`
	PreviousEvidenceRefs   []string                          `json:"previousEvidenceRefs,omitempty"`
	BusinessRequest        model.Request                     `json:"businessRequest"`
	OriginalAssessment     runtimecontext.Assessment         `json:"originalAssessment"`
}

// ContextSummaryPlan is a read-only Host projection. The native algorithm
// chooses the safe removal boundary, request, artifacts and receipt handling.
type ContextSummaryPlan struct {
	Request              model.Request
	Cursor               SamplingCursor
	ModelWindow          runtimecontext.ModelWindow
	Policy               runtimecontext.Policy
	Owner                bool
	ArtifactReader       bool
	ExplicitWorkingState bool
	Root                 ContextSummaryRoot
	PriorSummaryRun      *kernel.Snapshot
}

type ContextSummaryRead struct {
	SourceCheckpoint runtimecontext.Checkpoint
	Cursor           SamplingCursor
	Root             ContextSummaryRoot
}

// ContextSummaryPorts expose finite existing reads, Artifact writes and
// checkpoint CAS/binding receipts. No Provider or caller completion flag exists.
type ContextSummaryPorts interface {
	PlanSummary(context.Context, model.Request, kernel.Snapshot) (ContextSummaryPlan, error)
	ReadSummary(context.Context, ContextSummaryBinding) (ContextSummaryRead, error)
	CommitSummary(context.Context, SamplingCursor, runtimecontext.Checkpoint) (SamplingCommitReceipt, error)
	BindSummary(context.Context, SamplingCursor, runtimecontext.Checkpoint) (SamplingBindingReceipt, error)
}

// ContextSummaryAdapter is native orchestration under the existing concrete
// bounded Context middleware. It is not a second Run or general callback engine.
type ContextSummaryAdapter struct {
	bounded *BoundedContextAdapter
	ports   ContextSummaryPorts
}

func NewContextSummaryAdapter(bounded *BoundedContextAdapter, ports ContextSummaryPorts) (*ContextSummaryAdapter, error) {
	if bounded == nil || bounded.manager == nil || bounded.ports == nil || bounded.sampling != nil || ports == nil {
		return nil, ErrInvalidRequest
	}
	return &ContextSummaryAdapter{bounded: bounded, ports: ports}, nil
}
