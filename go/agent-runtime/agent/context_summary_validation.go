package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"
	"strings"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func (runner *Runner) SupportsContextSummary() bool {
	return runner != nil && runner.contextSummary != nil && runner.managedContextRecovery == nil
}

// UsesBudgetedContextSummary checks the exact concrete budget owner selected
// for the shared summary/business sampling protocol. It grants no execution
// permission and is used only to reject mismatched Host compositions.
func (runner *Runner) UsesBudgetedContextSummary(adapter *BoundedBudgetAdapter) bool {
	return runner.SupportsContextSummary() &&
		adapter != nil && runner.samplingBudget == adapter && runner.toolBudget == adapter
}

type contextSummaryModelKey struct{}

// Summary uses its original Context hydration/commit owner. Budgeted
// summary is admitted only through the same complete native topology that
// owns business sampling and result consumption, never a partial chain.
func validSummaryComposition(d Dependencies) bool {
	summary := d.ContextSummary
	if summary == nil || summary.bounded == nil || summary.ports == nil ||
		d.ManagedContextRecovery != nil || d.PrepareModelRequest != nil {
		return false
	}
	b := selectedSamplingBudget(d)
	if b == nil {
		return selectedToolBudget(d) == nil && contextOnlyRun(d, summary.bounded) &&
			(d.BoundedContext == nil || d.BoundedContext == summary.bounded)
	}
	return d.BoundedContext == summary.bounded &&
		(d.BoundedBudget == b || completeLegacyBudget(d, summary.bounded) == b)
}

func cloneContextSummaryBinding(value *ContextSummaryBinding) *ContextSummaryBinding {
	if value == nil {
		return nil
	}
	copy := *value
	copy.Artifacts = slices.Clone(value.Artifacts)
	copy.PreviousEvidenceRefs = slices.Clone(value.PreviousEvidenceRefs)
	copy.BusinessRequest = cloneSummaryRequest(value.BusinessRequest)
	return &copy
}

func summarySource(checkpoint runtimecontext.Checkpoint) runtimecontext.WorkingStateSource {
	return runtimecontext.WorkingStateSource{
		CheckpointID: checkpoint.ID, ContentHash: checkpoint.ContentHash,
		CoveredThroughSourceID: checkpoint.CoveredThroughSourceID, CoveredPathHash: checkpoint.CoveredPathHash,
	}
}

func summarySourceMatches(source runtimecontext.WorkingStateSource, checkpoint runtimecontext.Checkpoint) bool {
	return source == summarySource(checkpoint) && runtimecontext.ValidCheckpoint(checkpoint)
}

func summaryCursor(value SummaryCursor) ContextSummaryCursor {
	return ContextSummaryCursor{Turn: value.Turn, ExpectedTurnCheckpointID: value.ExpectedTurnCheckpointID,
		ExpectedHeadCheckpointID: value.ExpectedHeadCheckpointID, UpdatedAt: value.UpdatedAt}
}

func validSummaryRoot(value ContextSummaryRoot) bool {
	return value.InvocationID != "" && len(value.InvocationID) <= 2048 && strings.TrimSpace(value.InvocationID) == value.InvocationID &&
		validBindingFingerprint(value.InputHash) && value.Attempt > 0
}

func validSummaryBinding(value *ContextSummaryBinding) bool {
	if value == nil || jsoncontract.ValidateCanonicalUTF8(value) != nil || value.RunID == "" ||
		len(value.RunID) > runtimecontext.MaxWorkingStateTextBytes || strings.TrimSpace(value.RunID) != value.RunID ||
		!validSummaryRoot(value.Root) || value.RecipeVersion != WorkingStateSummaryVersion ||
		ValidateWorkingStateSummaryPolicy(&value.Policy) != nil || value.PriorVersion < 0 ||
		value.NextVersion != value.PriorVersion+1 || value.NextVersion > runtimecontext.MaxWorkingStateVersion ||
		value.RemovedMessages <= 0 || len(value.Artifacts) == 0 || len(value.Artifacts) > runtimecontext.MaxWorkingStateItems ||
		value.Cursor.Turn.ID == "" || value.Cursor.Turn.SessionID == "" || value.Cursor.Turn.ConfigSnapshotID == "" ||
		value.Cursor.Turn.Revision == 0 || value.Cursor.Turn.ContextCheckpointID != value.Source.CheckpointID ||
		value.Cursor.ExpectedTurnCheckpointID != value.Source.CheckpointID || value.Cursor.UpdatedAt.IsZero() ||
		value.ModelWindowFingerprint != runtimecontext.ModelWindowFingerprint(value.ModelWindow) ||
		!value.BusinessAccounting.MatchesWindow(value.ModelWindow) || !value.BusinessAccounting.MatchesRequest(value.BusinessRequest) ||
		!value.SummaryAccounting.Valid() || !value.SummaryAccounting.MatchesWindow(value.ModelWindow) ||
		len(value.BusinessRequest.Messages) != 0 || value.BusinessRequest.RunID != value.RunID {
		return false
	}
	seen := map[string]bool{}
	for _, artifact := range value.Artifacts {
		if artifact.ID == "" || seen[artifact.ID] || !validBindingFingerprint(artifact.ContentHash) {
			return false
		}
		seen[artifact.ID] = true
	}
	probe := &runtimecontext.WorkingState{Version: value.NextVersion, Source: value.Source, EvidenceRefs: summaryEvidenceRefs(value)}
	return runtimecontext.ValidateWorkingState(probe) == nil
}

func validContextSummaryInvocation(invocation ModelInvocation) bool {
	if invocation.Purpose == "" {
		return invocation.ContextSummary == nil && invocation.SummaryTargetHash == ""
	}
	if invocation.Purpose != ModelInvocationContextSummary || !validSummaryBinding(invocation.ContextSummary) ||
		invocation.ContextSummary.RunID != invocation.RunID ||
		(invocation.SummaryTargetHash != "" && !validBindingFingerprint(invocation.SummaryTargetHash)) {
		return false
	}
	if invocation.Status == ModelInvocationPending || invocation.Status == ModelInvocationCompleted {
		request := invocation.Request
		if plan := invocation.SamplingPlan; plan != nil {
			// Old, unbudgeted intents could acquire only a physical hash at
			// dispatch. Never reinterpret that hash as a budgeted admission.
			if plan.Window.ContextTokens == 0 {
				if plan.LedgerID != "" {
					return false
				}
			} else if plan.Policy != summaryInputPolicy(invocation.ContextSummary) {
				return false
			}
		}
		return invocation.ContextSummary.SummaryAccounting.MatchesRequest(request) &&
			len(request.Tools) == 0 && len(request.HostedTools) == 0 && !request.RequireToolCall &&
			request.HostedToolCallLimit != nil && *request.HostedToolCallLimit == 0 &&
			request.PreviousResponseID == "" && request.PromptCacheKey == "" &&
			sameFrozenOutputContract(request.OutputContract, workingStateSummaryContract())
	}
	return invocation.Status == ModelInvocationConsumed
}

func contextSummaryInputBytesWithinPolicy(invocation ModelInvocation) bool {
	if invocation.Purpose != ModelInvocationContextSummary || invocation.Status == ModelInvocationConsumed {
		return true
	}
	if invocation.ContextSummary == nil {
		return false
	}
	limit := invocation.ContextSummary.Policy.MaxSerializedBytes
	contextLimit := runtimecontext.NormalizePolicy(invocation.ContextSummary.ContextPolicy).MaxSerializedBytes
	if contextLimit < limit {
		limit = contextLimit
	}
	// Active intents retain their complete original request. A restarted
	// reader can enforce the real serialized input bound without invoking a
	// tokenizer or trusting a planning-time assessment from another process.
	size, err := invocation.ContextSummary.SummaryAccounting.SerializedBytes(invocation.Request)
	return err == nil && size <= limit
}

func validContextSummaryState(state runState) bool {
	if state.SummaryNativeRecovery && state.WorkingStateSummary == nil || state.WorkingStateSummary != nil && state.MaxModelOutputTokens <= 0 {
		return false
	}
	for index, invocation := range state.ModelInvocations {
		if invocation.Purpose == ModelInvocationContextSummary && (index != 0 || state.WorkingStateSummary == nil ||
			invocation.ContextSummary == nil || !reflect.DeepEqual(*state.WorkingStateSummary, invocation.ContextSummary.Policy)) {
			return false
		}
	}
	return true
}

func summaryEvidenceRefs(binding *ContextSummaryBinding) []string {
	refs := slices.Clone(binding.PreviousEvidenceRefs)
	for _, artifact := range binding.Artifacts {
		ref := "artifact:" + artifact.ID + "#sha256:" + artifact.ContentHash
		if !slices.Contains(refs, ref) {
			refs = append(refs, ref)
		}
	}
	return refs
}

func workingStateFingerprint(state *runtimecontext.WorkingState) string {
	raw, err := json.Marshal(state)
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

// VerifyWorkingStateSummaryOrigin is a pure check after the Host authenticates
// the old indexed root in the same Session. Consumed requests cannot be rehashed;
// retained native receipt/binding and the exact rebuilt state are checked here.
func VerifyWorkingStateSummaryOrigin(snapshot kernel.Snapshot, state *runtimecontext.WorkingState, sessionID string, actor kernel.ActorRef, thread kernel.ThreadRef) bool {
	if state == nil || state.Derivation == nil || runtimecontext.ValidateWorkingState(state) != nil ||
		state.Derivation.Kind != runtimecontext.WorkingStateDerivationSummary || state.Derivation.PolicyVersion != WorkingStateSummaryVersion ||
		snapshot.Run.ID != state.Derivation.RunID || snapshot.Run.Kind != RunKind || snapshot.Run.ParentRunID != "" ||
		snapshot.Run.Actor != actor || snapshot.Run.Thread != thread {
		return false
	}
	stored, err := decodeState(snapshot.State)
	if err != nil {
		return false
	}
	for _, invocation := range stored.ModelInvocations {
		if invocation.ID != state.Derivation.InvocationID {
			continue
		}
		if invocation.Status != ModelInvocationConsumed || invocation.Purpose != ModelInvocationContextSummary || invocation.ContextSummary == nil ||
			invocation.RequestHash != state.Derivation.RequestHash || invocation.ContextSummary.Cursor.Turn.SessionID != sessionID ||
			invocation.SummaryTargetHash != workingStateFingerprint(state) {
			return false
		}
		rebuilt, rebuildErr := WorkingStateFromSummaryInvocation(invocation)
		return rebuildErr == nil && sameBoundedValue(rebuilt, state)
	}
	return false
}

// summaryTurnProjection compares the exact immutable Context owner and
// reference, allowing only forward live status/diagnostic projections.
func summaryTurnProjection(original, current SummaryTurn) bool {
	if sameBoundedValue(original, current) {
		return true
	}
	// Turn lifecycle/diagnostic projection may advance independently of a
	// frozen summary: an unknown model outcome can be published and later
	// cleared while the original invocation and Context stay unchanged.
	// Do not make projection revision or diagnostics a second source owner.
	// Accept only a forward live projection with every immutable identity and
	// exact Context reference unchanged. The actual Context CAS still uses
	// the freshly read revision; no earlier projection grants write authority.
	if (original.Status != "accepted" && original.Status != "running") ||
		current.Status != "running" || current.Revision <= original.Revision ||
		current.UpdatedAt.Before(original.UpdatedAt) {
		return false
	}
	expected := original
	expected.Status, expected.Revision, expected.UpdatedAt = current.Status, current.Revision, current.UpdatedAt
	expected.ErrorCode, expected.ErrorDetail = current.ErrorCode, current.ErrorDetail
	return sameBoundedValue(expected, current)
}
