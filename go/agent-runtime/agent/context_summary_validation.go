package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func (runner *Runner) SupportsContextSummary() bool {
	return runner != nil && runner.contextSummary != nil && runner.managedContextRecovery == nil
}

type contextSummaryModelKey struct{}

func summaryBudgetConfigured(dependencies Dependencies) bool {
	for _, layer := range dependencies.RunMiddleware {
		if _, ok := layer.(*BoundedBudgetAdapter); ok {
			return true
		}
	}
	for _, layer := range dependencies.ModelMiddleware {
		if _, ok := layer.(*BoundedBudgetAdapter); ok {
			return true
		}
	}
	return false
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
	if state.SummaryNativeRecovery && (state.InputBinding != "" || state.InputCursor != 0) {
		return false // Host input boundaries are not part of the dedicated transfer proof.
	}
	for index, invocation := range state.ModelInvocations {
		if invocation.Purpose == ModelInvocationContextSummary && (index != 0 || state.WorkingStateSummary == nil ||
			invocation.ContextSummary == nil || !reflect.DeepEqual(*state.WorkingStateSummary, invocation.ContextSummary.Policy)) {
			return false
		}
	}
	return true
}

func newContextSummaryInvocation(run kernel.Run, request model.Request, provider, bindingFingerprint, purpose string, binding *ContextSummaryBinding, now time.Time) (ModelInvocation, error) {
	plannedID := request.InvocationID
	request.InvocationID = ""
	hash, err := modelInvocationRequestHash(request, nil, provider, bindingFingerprint, purpose, binding)
	if err != nil {
		return ModelInvocation{}, err
	}
	idSource := run.ID + "\x00" + strconv.FormatUint(run.Revision, 10) + "\x00" + hash
	idDigest := sha256.Sum256([]byte(idSource))
	id := "modelinv_" + hex.EncodeToString(idDigest[:16])
	if plannedID != "" && plannedID != id {
		// A summary counted before intent admission must retain that exact
		// identity, including its Provider binding, when the intent is sealed.
		return ModelInvocation{}, ErrInvalidRequest
	}
	request.InvocationID = id
	result := ModelInvocation{ID: id, RunID: run.ID, SourceRevision: run.Revision, RequestHash: hash,
		Purpose: purpose, ContextSummary: cloneContextSummaryBinding(binding), Provider: provider, BindingFingerprint: bindingFingerprint,
		Model: request.Model, Status: ModelInvocationPending, Request: model.CloneRequest(request), CreatedAt: now.UTC()}
	if !validContextSummaryInvocation(result) {
		return ModelInvocation{}, ErrInvalidRequest
	}
	return result, nil
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

// summaryTurnProjection permits only the one original Accepted-to-Running
// projection. It never treats arbitrary revisions or status changes as retries.
func summaryTurnProjection(original, current SummaryTurn) bool {
	if sameBoundedValue(original, current) {
		return true
	}
	if original.Status != "accepted" || current.Status != "running" || current.Revision != original.Revision+1 || current.UpdatedAt.Before(original.UpdatedAt) {
		return false
	}
	expected := original
	expected.Status, expected.Revision, expected.UpdatedAt = current.Status, current.Revision, current.UpdatedAt
	return sameBoundedValue(expected, current)
}
