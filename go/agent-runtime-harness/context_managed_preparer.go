package harness

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// ModelWindowResolver supplies the routed model's real window for an exact
// sampling request. It must not invent a default; an unknown limit fails closed.
type ModelWindowResolver func(context.Context, model.Request) (runtimecontext.ModelWindow, error)

// NewManagedContextPreparer builds a pre-intent Context projection with
// optional, durable pressure-based rollover. Config.ContextPolicy is read from
// the immutable Harness Turn snapshot; the host resolves model capacity.
//
// Compaction is permitted only for the owning Turn with an authorized
// harness.read_context_artifact Tool. Removed content is sealed in the existing
// Harness Store BEFORE committing the next Checkpoint. An unsuccessful CAS
// may leave harmless orphaned artifacts, never a missing referenced archive.
// The returned request is frozen by Agent in its ModelInvocation before
// dispatch, so a process restart does not recalculate a pending window.
func NewManagedContextPreparer(store Store, manager *runtimecontext.Manager, resolve ModelWindowResolver) (agent.ModelRequestPreparer, error) {
	if store == nil || manager == nil || resolve == nil {
		return nil, ErrInvalidRequest
	}
	preparer := managedContextPreparer{store: store, manager: manager, resolve: resolve}
	return preparer.prepare, nil
}

type managedContextPreparer struct {
	store   Store
	manager *runtimecontext.Manager
	resolve ModelWindowResolver
}

type managedContextCandidate struct {
	window     runtimecontext.Window
	artifacts  []runtimecontext.Artifact
	assessment runtimecontext.Assessment
}

func (preparer managedContextPreparer) prepare(ctx context.Context, incoming model.Request) (model.Request, error) {
	request, err := MaterializeContextWindowRequest(ctx, incoming)
	if err != nil {
		return model.Request{}, err
	}
	binding, bound := CurrentContextWindowBinding(ctx)
	checkpoint, hasCheckpoint := CurrentContextCheckpoint(ctx)
	if !hasCheckpoint {
		// A direct non-Harness run may use the same preparer harmlessly.
		return request, nil
	}
	if !bound {
		return model.Request{}, ErrInvalidRequest
	}
	turn, err := preparer.store.GetTurn(ctx, binding.TurnID)
	if err != nil {
		return model.Request{}, err
	}
	if turn.ContextCheckpointID != checkpoint.ID || !sameContextCheckpointRef(checkpoint, turn.ContextRef) {
		return model.Request{}, ErrConflict
	}
	config, err := preparer.store.GetConfigSnapshot(ctx, turn.ConfigSnapshotID)
	if err != nil {
		return model.Request{}, err
	}
	policy := runtimecontext.NormalizePolicy(config.ContextPolicy)
	window, err := preparer.resolve(ctx, model.CloneRequest(request))
	if err != nil {
		return model.Request{}, err
	}
	assessment, err := preparer.manager.AssessModelRequest(ctx, request, window, policy)
	if err != nil {
		return model.Request{}, err
	}
	withinHard := runtimecontext.WithinHardBudget(assessment, policy)
	if withinHard && assessment.HardTokenEstimate <= assessment.SoftInputTokens {
		return request, nil
	}
	// There must be an authorized artifact reader before any historical
	// evidence is removed from the model-visible transcript.
	if !hasContextArtifactReader(request) {
		if withinHard {
			return request, nil
		}
		return model.Request{}, runtimecontext.ErrBudgetExceeded
	}
	if binding.Access != ContextWindowOwner {
		if withinHard {
			return request, nil
		}
		return model.Request{}, runtimecontext.ErrBudgetExceeded
	}
	choice, ok, err := preparer.selectCompaction(ctx, checkpoint, request, window, policy, assessment, withinHard)
	if err != nil {
		return model.Request{}, err
	}
	if !ok {
		if withinHard {
			return request, nil
		}
		return model.Request{}, runtimecontext.ErrBudgetExceeded
	}
	next, err := preparer.manager.Rollover(ctx, runtimecontext.RolloverRequest{
		Previous: checkpoint, Window: choice.window, Artifacts: choice.artifacts,
		Reason: "model_window_pressure", ModelWindowFingerprint: runtimecontext.ModelWindowFingerprint(window),
		Assessment: &choice.assessment,
	})
	if err != nil {
		return model.Request{}, err
	}
	for _, artifact := range choice.artifacts {
		if _, _, err = preparer.store.PutContextArtifact(ctx, artifact); err != nil {
			return model.Request{}, err
		}
	}
	// The Turn and its Context head already exist before Agent prepares its
	// next model intent. Commit this rollover as one existing Store CAS.
	_, err = preparer.store.CommitContextCheckpoint(ctx, ContextCheckpointCommit{
		TurnID: turn.ID, ExpectedTurnRevision: turn.Revision,
		ExpectedTurnCheckpointID: checkpoint.ID, ExpectedHeadCheckpointID: checkpoint.ID,
		Checkpoint: next, UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		return model.Request{}, err
	}
	if err = ReplaceContextCheckpoint(ctx, checkpoint.ID, next); err != nil {
		return model.Request{}, err
	}
	request.Messages = runtimecontext.Materialize(next.Window)
	return request, nil
}

func (preparer managedContextPreparer) selectCompaction(
	ctx context.Context, previous runtimecontext.Checkpoint, request model.Request,
	window runtimecontext.ModelWindow, policy runtimecontext.Policy,
	original runtimecontext.Assessment, originalWithinHard bool,
) (managedContextCandidate, bool, error) {
	var best managedContextCandidate
	bestFound := false
	bestSoft := false
	consider := func(candidateWindow runtimecontext.Window, artifacts []runtimecontext.Artifact) error {
		trial := model.CloneRequest(request)
		trial.Messages = runtimecontext.Materialize(candidateWindow)
		assessment, err := preparer.manager.AssessModelRequest(ctx, trial, window, policy)
		if err != nil {
			return err
		}
		if !runtimecontext.WithinHardBudget(assessment, policy) ||
			assessment.SerializedBytes >= original.SerializedBytes || assessment.HardTokenEstimate >= original.HardTokenEstimate {
			return nil
		}
		soft := assessment.HardTokenEstimate <= assessment.SoftInputTokens
		if !bestFound || (soft && !bestSoft) || (soft == bestSoft && assessment.HardTokenEstimate < best.assessment.HardTokenEstimate) {
			best = managedContextCandidate{window: candidateWindow, artifacts: artifacts, assessment: assessment}
			bestFound, bestSoft = true, soft
		}
		return nil
	}
	tryPortable := func(recent int) error {
		limited := policy
		limited.PreserveRecentTurns = recent
		result, err := preparer.manager.CompactPortable(runtimecontext.PortableCompactionRequest{
			Previous: previous, RunID: request.RunID, Messages: request.Messages, Policy: limited,
		})
		if errors.Is(err, runtimecontext.ErrBudgetExceeded) {
			return nil
		}
		if err != nil {
			return err
		}
		// An Agent-readable archive must never reveal provider-owned signed
		// replay evidence as plain JSON. Retain that history if no private
		// archival projection is available.
		if archivesContainProviderReplay(result.Artifacts) {
			return nil
		}
		return consider(result.Window, result.Artifacts)
	}
	if err := tryPortable(policy.PreserveRecentTurns); err != nil {
		return managedContextCandidate{}, false, err
	}
	toolResult, err := preparer.manager.CompactToolPressure(previous, request.RunID, request.Messages)
	if err == nil {
		if err = consider(toolResult.Window, toolResult.Artifacts); err != nil {
			return managedContextCandidate{}, false, err
		}
	} else if !errors.Is(err, runtimecontext.ErrBudgetExceeded) {
		return managedContextCandidate{}, false, err
	}
	// Only under hard pressure may the protected tail shrink below the
	// preferred recent-turn policy.
	if !originalWithinHard && policy.MinimumRecentTurns < policy.PreserveRecentTurns && !bestSoft {
		if err = tryPortable(policy.MinimumRecentTurns); err != nil {
			return managedContextCandidate{}, false, err
		}
	}
	if !bestFound || (originalWithinHard && !bestSoft) {
		return managedContextCandidate{}, false, nil
	}
	return best, true, nil
}

func archivesContainProviderReplay(artifacts []runtimecontext.Artifact) bool {
	for _, artifact := range artifacts {
		var messages []model.Message
		if len(artifact.ContentJSON) == 0 || json.Unmarshal(artifact.ContentJSON, &messages) != nil || len(messages) == 0 {
			return true // An unreadable archive cannot prove provider evidence is absent.
		}
		for _, message := range messages {
			if message.ProviderReplay != nil {
				return true
			}
		}
	}
	return false
}

func hasContextArtifactReader(request model.Request) bool {
	for _, definition := range request.Tools {
		if definition.Key == ContextArtifactToolKey {
			return true
		}
	}
	return false
}
