package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type boundedContextCandidate struct {
	window    runtimecontext.Window
	artifacts []runtimecontext.Artifact
	modelCandidate
}

func (adapter *BoundedContextAdapter) selectCompaction(
	ctx context.Context, previous runtimecontext.Checkpoint, request model.Request,
	window runtimecontext.ModelWindow, policy runtimecontext.Policy,
	frame runtimecontext.FrozenAccounting, original modelCandidate,
) (boundedContextCandidate, bool, error) {
	var best boundedContextCandidate
	bestFound := false
	bestSoft := false
	consider := func(candidateWindow runtimecontext.Window, artifacts []runtimecontext.Artifact) (bool, error) {
		trial := model.CloneRequest(request)
		trial.Messages = runtimecontext.Materialize(candidateWindow)
		candidate, err := assessModelCandidate(ctx, adapter, trial, window, policy, frame)
		if err != nil {
			return false, err
		}
		assessment := candidate.assessment
		if candidate.omitLocalTools || !candidate.selectable() ||
			(original.disposition == candidateAdmitted && candidate.disposition == candidateWaiting) {
			return false, nil
		}
		// Ordinary soft/Context pressure must actually shrink the request.
		// Under SharedBudget pressure, joint feasibility is the improvement:
		// the independently measured quota dimension can shrink even when
		// the Context counter uses a different bound.
		if !errors.Is(original.quota.err, budget.ErrExhausted) && (assessment.SerializedBytes >= original.assessment.SerializedBytes ||
			assessment.HardTokenEstimate >= original.assessment.HardTokenEstimate) {
			return false, nil
		}
		soft := candidate.soft()
		better := !bestFound || (candidate.disposition == candidateAdmitted && best.disposition == candidateWaiting)
		if bestFound && candidate.disposition == best.disposition {
			better = (soft && !bestSoft) || (soft == bestSoft && assessment.HardTokenEstimate < best.assessment.HardTokenEstimate)
		}
		if better {
			best = boundedContextCandidate{window: candidateWindow, artifacts: artifacts, modelCandidate: candidate}
			bestFound, bestSoft = true, soft
		}
		return soft && candidate.disposition == candidateAdmitted, nil
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
	if !original.selectable() && policy.MinimumRecentTurns < policy.PreserveRecentTurns && !bestSoft {
		if err = tryPortable(policy.MinimumRecentTurns); err != nil {
			return boundedContextCandidate{}, false, err
		}
	}
	if !bestFound || (original.selectable() && !bestSoft) {
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
