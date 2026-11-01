package harness

import (
	"context"
	"errors"
	"fmt"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// Local Run IDs are not execution-owner evidence for raw Feature descendants.
// Every edge must be vouched for by its statically composed Feature, all the
// way back to a Harness Invocation. Matching public task fields alone cannot
// distinguish identical Runs in independent Kernels.
type localRelationVerifier interface {
	VerifyLocalChild(*kernel.Runtime, kernel.Snapshot, runrelation.Relation, kernel.Snapshot, bool) bool
}

type localAgentRouteVerifier interface {
	UsesLocalAgentRunner(*kernel.Runtime, *agent.Runner) bool
}

// Approval is physical Agent continuation, not merely a same-Kernel read.
// Native Invocation owners use their configured Agent; delegated/raw Feature
// children must retain the original fixed Agent object and all its hooks.
func (runner *Runner) verifyLocalApprovalRun(ctx context.Context, candidate kernel.Snapshot) error {
	if err := runner.verifyLocalRelationRun(ctx, candidate, false); err != nil {
		return err
	}
	var feature any
	if runner.relationReader != nil {
		relation, err := runner.relationReader.GetByChild(ctx, candidate.Run.ID)
		if err != nil && !errors.Is(err, runrelation.ErrNotFound) {
			return err
		}
		if err == nil {
			switch relation.Kind {
			case runrelation.KindTeamMember:
				feature = runner.teams
			case runrelation.KindGroupChatSpeaker:
				feature = runner.groupChats
			case runrelation.KindWorkflowEffect:
				feature = runner.workflows
			case runrelation.KindDelegation, runrelation.KindCapability:
				// Dedicated Invocation below owns these Harness routes.
			}
		}
	}
	if feature == nil {
		owner, err := runner.store.GetInvocationByExecutionRefID(ctx, candidate.Run.ID)
		if err != nil {
			return err
		}
		if !isHandoffDelegationInvocation(owner) {
			return nil
		}
		feature = runner.handoffs
	}
	original, ok := runner.agent.(*agent.Runner)
	verifier, verified := feature.(localAgentRouteVerifier)
	if !ok || !verified || !verifier.UsesLocalAgentRunner(runner.runtime, original) {
		return ErrRuntimeCancellationUnverified
	}
	return nil
}

func (runner *Runner) verifyLocalRelationRun(ctx context.Context, candidate kernel.Snapshot, control bool) error {
	return runner.verifyLocalRelationChain(ctx, candidate, control, make(map[string]bool))
}

func (runner *Runner) verifyLocalRelationChain(ctx context.Context, candidate kernel.Snapshot, control bool, seen map[string]bool) error {
	unverified := func() error {
		return fmt.Errorf("Run %q has no verified local execution owner: %w", candidate.Run.ID, ErrRuntimeCancellationUnverified)
	}
	if candidate.Run.ID == "" || seen[candidate.Run.ID] {
		return unverified()
	}
	seen[candidate.Run.ID] = true
	// A global Invocation lookup must not hide an incoming Feature edge.
	// Another Turn (or an unrelated sibling) may own a local Run whose ID
	// collides with a remote Feature child. Prove that edge first.
	var relation runrelation.Relation
	relationErr := runrelation.ErrNotFound
	if runner.relationReader != nil {
		relation, relationErr = runner.relationReader.GetByChild(ctx, candidate.Run.ID)
	}
	if relationErr != nil && !errors.Is(relationErr, runrelation.ErrNotFound) {
		return errors.Join(unverified(), relationErr)
	}
	rawFeatureChild := relationErr == nil && (relation.Kind == runrelation.KindTeamMember ||
		relation.Kind == runrelation.KindGroupChatSpeaker || relation.Kind == runrelation.KindWorkflowEffect)
	owner, err := runner.store.GetInvocationByExecutionRefID(ctx, candidate.Run.ID)
	if err == nil && !rawFeatureChild {
		if isHandoffDelegationInvocation(owner) {
			coordinator, ok := runner.handoffs.(*handoff.Coordinator)
			if !ok || !coordinator.ObservesLocalKernelRunner(runner.runtime) || (control && !coordinator.OwnsLocalKernelRunner(runner.runtime)) {
				return unverified()
			}
			if _, _, err = runner.localHandoffCancellationAuthority(ctx, owner, candidate); err != nil {
				return unverified()
			}
		}
		return nil
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if runner.relationReader == nil {
		return unverified()
	}
	if relationErr != nil {
		return errors.Join(unverified(), relationErr)
	}
	if relation.ChildRunID != candidate.Run.ID || relation.ParentRunID == "" {
		return unverified()
	}
	parent, err := runner.runtime.Load(ctx, relation.ParentRunID)
	if err != nil {
		return errors.Join(unverified(), err)
	}
	if err = runner.verifyLocalRelationChain(ctx, parent, control, seen); err != nil {
		return err
	}
	var feature any
	switch relation.Kind {
	case runrelation.KindTeamMember:
		feature = runner.teams
	case runrelation.KindGroupChatSpeaker:
		feature = runner.groupChats
	case runrelation.KindWorkflowEffect:
		feature = runner.workflows
	case runrelation.KindDelegation, runrelation.KindCapability:
		return unverified() // These require their own persisted Harness Invocation.
	default:
		return unverified()
	}
	verifier, ok := feature.(localRelationVerifier)
	if !ok || !verifier.VerifyLocalChild(runner.runtime, parent, relation, candidate, control) {
		return unverified()
	}
	return nil
}
