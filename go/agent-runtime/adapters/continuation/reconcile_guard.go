package continuationadapter

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/groupchat"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

// ReconcileGuard inspects feature-owned durable parent state to distinguish
// already-consumed children from still-pending children. This is intentionally
// outside Kernel/continuation so those packages do not interpret feature state.
//
// Unknown relation kinds, missing child identities, and unrecognized Run kinds
// remain eligible: a false negative would suppress a needed recovery wakeup.
// Hosts must explicitly pass this function in SchedulerDependencies.
func ReconcileGuard(_ context.Context, parent kernel.Snapshot, relation runrelation.Relation) (bool, error) {
	switch {
	case parent.Run.Kind == team.RunKind && relation.Kind == runrelation.KindTeamMember:
		view, err := team.ViewState(parent)
		if err != nil {
			return false, err
		}
		for _, member := range view.Members {
			if member.Delegation.ChildRunID != relation.ChildRunID {
				continue
			}
			status := member.Delegation.Status
			return status != handoff.StatusCompleted &&
				status != handoff.StatusFailed && status != handoff.StatusCancelled, nil
		}
	case parent.Run.Kind == groupchat.RunKind && relation.Kind == runrelation.KindGroupChatSpeaker:
		view, err := groupchat.ViewState(parent)
		if err != nil {
			return false, err
		}
		for _, turn := range view.SpeakerTurns {
			if turn.ChildRunID != relation.ChildRunID {
				continue
			}
			status := turn.Status
			return status != groupchat.SpeakerTurnCompleted &&
				status != groupchat.SpeakerTurnFailed && status != groupchat.SpeakerTurnCancelled, nil
		}
	case parent.Run.Kind == workflow.RunKind && relation.Kind == runrelation.KindWorkflowEffect:
		view, err := workflow.ViewState(parent)
		if err != nil {
			return false, err
		}
		for _, effect := range view.Effects {
			if effect.ChildRunID != relation.ChildRunID {
				continue
			}
			return effect.Status != workflow.EffectCompleted && effect.Status != workflow.EffectFailed, nil
		}
	}
	return true, nil
}
