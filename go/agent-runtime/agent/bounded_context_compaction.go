package agent

import (
	"context"
	"encoding/json"
	"errors"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type boundedContextCandidate struct {
	window     runtimecontext.Window
	artifacts  []runtimecontext.Artifact
	assessment runtimecontext.Assessment
}

func (adapter *BoundedContextAdapter) selectCompaction(
	ctx context.Context, previous runtimecontext.Checkpoint, request model.Request,
	window runtimecontext.ModelWindow, policy runtimecontext.Policy,
	original runtimecontext.Assessment, originalWithinHard bool,
) (boundedContextCandidate, bool, error) {
	var best boundedContextCandidate
	bestFound := false
	bestSoft := false
	consider := func(candidateWindow runtimecontext.Window, artifacts []runtimecontext.Artifact) (bool, error) {
		trial := model.CloneRequest(request)
		trial.Messages = runtimecontext.Materialize(candidateWindow)
		assessment, err := adapter.manager.AssessModelRequest(ctx, trial, window, policy)
		if err != nil {
			return false, err
		}
		if !runtimecontext.WithinHardBudget(assessment, policy) ||
			assessment.SerializedBytes >= original.SerializedBytes || assessment.HardTokenEstimate >= original.HardTokenEstimate {
			return false, nil
		}
		soft := assessment.HardTokenEstimate <= assessment.SoftInputTokens
		if !bestFound || (soft && !bestSoft) || (soft == bestSoft && assessment.HardTokenEstimate < best.assessment.HardTokenEstimate) {
			best = boundedContextCandidate{window: candidateWindow, artifacts: artifacts, assessment: assessment}
			bestFound, bestSoft = true, soft
		}
		return soft, nil
	}
	tryPortable := func(recent int) error {
		limited := policy
		limited.PreserveRecentTurns = recent
		// Keep the readable extract when this candidate meets the soft target.
		// A hard-admissible candidate alone does not satisfy that target, and
		// a candidate from a different strategy/tail cannot stop this search.
		// The exact archive and marker remain the fallback for this same tail.
		for _, omitExtract := range []bool{false, true} {
			result, err := adapter.manager.CompactPortable(runtimecontext.PortableCompactionRequest{
				Previous: previous, RunID: request.RunID, Messages: request.Messages, Policy: limited, OmitExtract: omitExtract,
			})
			if errors.Is(err, runtimecontext.ErrBudgetExceeded) {
				return nil
			}
			if err != nil {
				return err
			}
			// An Agent-readable archive must never reveal provider-owned signed
			// replay evidence as plain JSON.
			if boundedArchivesContainProviderReplay(result.Artifacts) {
				return nil
			}
			meetsSoft, err := consider(result.Window, result.Artifacts)
			if err != nil {
				return err
			}
			if meetsSoft {
				return nil
			}
		}
		return nil
	}
	if err := tryPortable(policy.PreserveRecentTurns); err != nil {
		return boundedContextCandidate{}, false, err
	}
	toolResult, err := adapter.manager.CompactToolPressure(previous, request.RunID, request.Messages)
	if err == nil {
		if _, err = consider(toolResult.Window, toolResult.Artifacts); err != nil {
			return boundedContextCandidate{}, false, err
		}
	} else if !errors.Is(err, runtimecontext.ErrBudgetExceeded) {
		return boundedContextCandidate{}, false, err
	}
	// Only under hard pressure may the protected tail shrink below the
	// preferred recent-turn policy.
	if !originalWithinHard && policy.MinimumRecentTurns < policy.PreserveRecentTurns && !bestSoft {
		if err = tryPortable(policy.MinimumRecentTurns); err != nil {
			return boundedContextCandidate{}, false, err
		}
	}
	if !bestFound || (originalWithinHard && !bestSoft) {
		return boundedContextCandidate{}, false, nil
	}
	return best, true, nil
}

func boundedArchivesContainProviderReplay(artifacts []runtimecontext.Artifact) bool {
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
